package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// CodexRequestEnvironment 是通过实际账号出口探测得到的地域快照，不含代理凭据
// IP 无法证明操作系统、语言偏好或工作目录，这些内容不属于该快照
type CodexRequestEnvironment struct {
	IP          string    `json:"ip"`
	Country     string    `json:"country"`
	CountryCode string    `json:"country_code"`
	Region      string    `json:"region"`
	City        string    `json:"city"`
	Timezone    string    `json:"timezone"`
	CheckedAt   time.Time `json:"checked_at"`
}
type RequestEnvironmentProber interface {
	ProbeRequestEnvironment(context.Context, string) (*CodexRequestEnvironment, error)
}
type requestEnvironmentEntry struct {
	Identity    string                   `json:"identity"`
	Environment *CodexRequestEnvironment `json:"environment"`
	RetryAt     time.Time                `json:"retry_at,omitempty"`
}
type AccountRequestEnvironmentService struct {
	repo    SettingRepository
	prober  RequestEnvironmentProber
	mu      sync.Mutex
	entries map[string]requestEnvironmentEntry
	flight  singleflight.Group
	now     func() time.Time
	slots   chan struct{}
}

const codexEnvironmentTTL = 15 * time.Minute

var errRequestEnvironmentUnavailable = errors.New("账号出口地域探测失败，无法保证请求时区一致；请检查代理及地域查询连通性")

func NewAccountRequestEnvironmentService(repo SettingRepository, prober RequestEnvironmentProber) *AccountRequestEnvironmentService {
	return &AccountRequestEnvironmentService{repo: repo, prober: prober, entries: make(map[string]requestEnvironmentEntry), now: time.Now, slots: make(chan struct{}, 8)}
}
func accountEnvironmentRoute(account *Account) (string, string, error) {
	if account == nil {
		return "", "", errors.New("account is required")
	}
	if account.ProxyID == nil {
		return "direct", "", nil
	}
	if account.Proxy == nil || account.Proxy.ID != *account.ProxyID {
		return "", "", errors.New("账号代理信息不完整，禁止按直连探测")
	}
	return fmt.Sprintf("proxy_%d", *account.ProxyID), account.Proxy.URL(), nil
}
func validRequestEnvironment(e *CodexRequestEnvironment) bool {
	if e == nil || net.ParseIP(e.IP) == nil || len(e.CountryCode) != 2 || e.Timezone == "" || e.CheckedAt.IsZero() {
		return false
	}
	_, err := time.LoadLocation(e.Timezone)
	return err == nil
}
func (s *AccountRequestEnvironmentService) Resolve(ctx context.Context, account *Account) (*CodexRequestEnvironment, error) {
	return s.resolve(ctx, account, false)
}
func (s *AccountRequestEnvironmentService) Refresh(ctx context.Context, account *Account) (*CodexRequestEnvironment, error) {
	return s.resolve(ctx, account, true)
}
func (s *AccountRequestEnvironmentService) resolve(ctx context.Context, account *Account, force bool) (*CodexRequestEnvironment, error) {
	route, proxy, err := accountEnvironmentRoute(account)
	if err != nil {
		return nil, err
	}
	identitySource := proxy
	if route == "direct" {
		hostname, _ := os.Hostname()
		identitySource = "direct:" + hostname
	}
	digest := sha256.Sum256([]byte(identitySource))
	identity := hex.EncodeToString(digest[:])
	fresh := func(e requestEnvironmentEntry) bool {
		return e.Identity == identity && validRequestEnvironment(e.Environment) && !e.Environment.CheckedAt.After(s.now().Add(time.Minute)) && s.now().Sub(e.Environment.CheckedAt) < codexEnvironmentTTL
	}
	s.mu.Lock()
	entry := s.entries[route]
	s.mu.Unlock()
	if !force && fresh(entry) {
		copy := *entry.Environment
		return &copy, nil
	}
	if !force && entry.Identity == identity && s.now().Before(entry.RetryAt) {
		return nil, errRequestEnvironmentUnavailable
	}
	flightKey := route + identity
	if force {
		flightKey += ":refresh"
	}
	result := s.flight.DoChan(flightKey, func() (any, error) {
		// 等待者取消不取消其他请求共享的探测，探测本身必须有硬期限
		probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 12*time.Second)
		defer cancel()
		if s.repo != nil && !force {
			raw, readErr := s.repo.GetValue(probeCtx, "codex_request_environment_"+route)
			if readErr == nil {
				var saved requestEnvironmentEntry
				if json.Unmarshal([]byte(raw), &saved) == nil && saved.Identity == identity {
					if s.now().Before(saved.RetryAt) {
						s.remember(route, saved)
						return nil, errRequestEnvironmentUnavailable
					}
					if fresh(saved) {
						s.remember(route, saved)
						return saved.Environment, nil
					}
				}
			}
		}
		if s.prober == nil {
			return nil, errRequestEnvironmentUnavailable
		}
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		case <-probeCtx.Done():
			return nil, errRequestEnvironmentUnavailable
		}
		environment, probeErr := s.prober.ProbeRequestEnvironment(probeCtx, proxy)
		if probeErr != nil || environment == nil {
			s.store(probeCtx, route, requestEnvironmentEntry{Identity: identity, RetryAt: s.now().Add(30 * time.Second)})
			return nil, errRequestEnvironmentUnavailable
		}
		environment.CheckedAt = s.now().UTC()
		environment.CountryCode = strings.ToUpper(environment.CountryCode)
		if !validRequestEnvironment(environment) {
			s.store(probeCtx, route, requestEnvironmentEntry{Identity: identity, RetryAt: s.now().Add(30 * time.Second)})
			return nil, errRequestEnvironmentUnavailable
		}
		saved := requestEnvironmentEntry{Identity: identity, Environment: environment}
		// 地域快照持久化失败可以使用本次真实探测值；不能使用过期或旧代理快照
		s.store(probeCtx, route, saved)
		return environment, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-result:
		if r.Err != nil {
			return nil, r.Err
		}
		environment, ok := r.Val.(*CodexRequestEnvironment)
		if !ok || environment == nil {
			return nil, errRequestEnvironmentUnavailable
		}
		copy := *environment
		return &copy, nil
	}
}
func (s *AccountRequestEnvironmentService) store(ctx context.Context, route string, e requestEnvironmentEntry) {
	if s.repo != nil {
		if raw, err := json.Marshal(e); err == nil {
			_ = s.repo.Set(ctx, "codex_request_environment_"+route, string(raw))
		}
	}
	s.remember(route, e)
}
func (s *AccountRequestEnvironmentService) remember(route string, e requestEnvironmentEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) >= 256 {
		clear(s.entries)
	}
	s.entries[route] = e
}
func (s *SettingService) RequestEnvironments() *AccountRequestEnvironmentService {
	if s == nil {
		return nil
	}
	return s.requestEnvironments
}
func (s *SettingService) warmCodexRequestEnvironment(account *Account) {
	if s == nil || s.requestEnvironments == nil || account == nil || !account.UsesOpenAICodexProtocol() {
		return
	}
	// 管理操作已完成，不把探测失败变成账号保存失败；首次请求仍会强制确认地域
	snapshot := *account
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 13*time.Second)
		defer cancel()
		if snapshot.ProxyID != nil && snapshot.Proxy == nil && s.proxyRepo != nil {
			proxy, err := s.proxyRepo.GetByID(ctx, *snapshot.ProxyID)
			if err != nil {
				return
			}
			snapshot.Proxy = proxy
		}
		_, _ = s.requestEnvironments.Resolve(ctx, &snapshot)
	}()
}
