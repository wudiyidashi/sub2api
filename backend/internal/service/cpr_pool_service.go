package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/accountdriver"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/cpr"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// CPRPoolBinding 仅包含账号池路由，不包含上游 Token 或静态内部 Client Key。
type CPRPoolBinding struct {
	GroupID         int64    `json:"group_id"`
	Provider        string   `json:"provider"`
	AccountGroupIDs []string `json:"account_group_ids"`
}

type CPRPoolService struct {
	client      *cpr.Client
	provisioner *cpr.Provisioner
	config      config.CPRManagedConfig
	stop        chan struct{}
	stopOnce    sync.Once
	wg          sync.WaitGroup
	runContext  context.Context
	cancel      context.CancelFunc
	accounts    AccountRepository
	keys        APIKeyRepository
	settings    SettingRepository
}

type cprExecutionBinding struct {
	service   *CPRPoolService
	accountID int64
	key       cpr.ScopedKey
	attempted *atomic.Bool
}

type cprExecutionBindingKey struct{}

func NewCPRPoolService(repo SettingRepository, cfg *config.Config) *CPRPoolService {
	runContext, cancel := context.WithCancel(context.Background())
	s := &CPRPoolService{stop: make(chan struct{}), runContext: runContext, cancel: cancel, settings: repo}
	if cfg == nil || !cfg.AccountDrivers.CPRManaged.Enabled {
		return s
	}
	s.config = cfg.AccountDrivers.CPRManaged
	if s.config.Validate() != nil {
		return s
	}
	client, err := cpr.New(cpr.Config{
		BaseURL: s.config.BaseURL, AdminKey: s.config.AdminAPIKey,
		AdminTimeout: time.Duration(s.config.AdminTimeoutSeconds) * time.Second,
	}, nil)
	if err != nil {
		return s
	}
	s.client = client
	s.provisioner = cpr.NewProvisionerWithContext(runContext, client, repo)
	return s
}

func (a *Account) IsCPRManaged() bool {
	return a != nil && a.EffectiveAccountDriverID() == accountdriver.CPRManagedID
}

func (a *Account) CPRPoolBinding() (*CPRPoolBinding, error) {
	if !a.IsCPRManaged() || a.Type != AccountTypeAPIKey || a.ParentAccountID != nil || a.ProxyID != nil {
		return nil, errors.New("account is not a CPR execution pool")
	}
	if len(a.Credentials) != 0 {
		return nil, errors.New("CPR execution pools must not contain account credentials")
	}
	raw, err := json.Marshal(a.Extra["cpr_pool"])
	if err != nil {
		return nil, errors.New("invalid CPR execution pool binding")
	}
	var binding CPRPoolBinding
	if json.Unmarshal(raw, &binding) != nil || binding.GroupID <= 0 {
		return nil, errors.New("invalid CPR execution pool binding")
	}
	provider, err := cpr.ProviderForPlatform(a.Platform)
	if err != nil || provider != binding.Provider {
		return nil, errors.New("CPR execution pool provider does not match the business platform")
	}
	if len(a.GroupIDs) != 1 || a.GroupIDs[0] != binding.GroupID {
		return nil, errors.New("CPR execution pools must be bound to exactly their business group")
	}
	if _, err := (cpr.Scope{Deployment: "validate", APIKeyID: 1, GroupID: binding.GroupID, Provider: binding.Provider, AccountGroupIDs: binding.AccountGroupIDs}).Key(make([]byte, 32)); err != nil {
		return nil, errors.New("CPR execution pools require explicit account groups")
	}
	for key, value := range a.Extra {
		switch key {
		case "cpr_pool":
		case openAILongContextBillingEnabledKey:
			// 该字段由持久化层自动补齐，属于 Sub2API 的计费开关。
			if _, valid := value.(bool); !valid {
				return nil, errors.New("invalid CPR pool billing setting")
			}
		default:
			return nil, errors.New("CPR execution pools cannot contain native account settings")
		}
	}
	return &binding, nil
}

func (s *CPRPoolService) Prepare(ctx context.Context, account *Account, apiKeyID, groupID int64) (context.Context, *Account, error) {
	if s == nil || s.client == nil || s.provisioner == nil {
		return ctx, nil, errors.New("CPR account management is unavailable")
	}
	if s.accounts == nil {
		return ctx, nil, errors.New("CPR business pool verification is unavailable")
	}
	// 调度缓存不是授权来源；每次执行使用当前配置，停用与缩小账号池立即对新请求生效。
	live, err := s.accounts.GetByID(ctx, account.ID)
	if err != nil || live == nil || !live.IsCPRManaged() || !live.Schedulable || live.Status != StatusActive {
		return ctx, nil, errors.New("CPR business pool is unavailable")
	}
	account = live
	if !slices.Contains(account.GroupIDs, groupID) {
		return ctx, nil, errors.New("CPR execution pool is outside the authorized business group")
	}
	binding, err := account.CPRPoolBinding()
	if err != nil {
		return ctx, nil, err
	}
	if groupID <= 0 || groupID != binding.GroupID {
		return ctx, nil, errors.New("CPR execution pool is outside the authorized business group")
	}
	key, err := (cpr.Scope{
		Deployment: s.config.DeploymentID, APIKeyID: apiKeyID, GroupID: groupID,
		Provider: binding.Provider, AccountGroupIDs: binding.AccountGroupIDs,
	}).Key([]byte(s.config.BridgeSecret))
	if err != nil {
		return ctx, nil, err
	}
	if err := s.provisioner.Ensure(ctx, key); err != nil {
		return ctx, nil, fmt.Errorf("CPR account pool authorization failed: %w", err)
	}
	// 该副本只参与客户端协议转换和下游计费解析，不携带任何真实上游账号身份。
	// Grok 也通过 CPR 的 Responses 数据面执行，不能进入原生 Grok Cookie/Token 路径。
	prepared := *account
	prepared.Platform = PlatformOpenAI
	prepared.Type = AccountTypeAPIKey
	prepared.AccountDriverID = accountdriver.NativeID
	prepared.Proxy = nil
	prepared.ProxyID = nil
	prepared.Credentials = map[string]any{
		"base_url": s.config.BaseURL, "api_key": key.Secret(), "api_protocol": APIProtocolResponses,
	}
	prepared.Extra = map[string]any{"openai_passthrough": true}
	ctx = context.WithValue(ctx, cprExecutionBindingKey{}, cprExecutionBinding{service: s, accountID: account.ID, key: key, attempted: &atomic.Bool{}})
	return ctx, &prepared, nil
}

func cprBindingFromContext(ctx context.Context) (cprExecutionBinding, bool) {
	if ctx == nil {
		return cprExecutionBinding{}, false
	}
	binding, ok := ctx.Value(cprExecutionBindingKey{}).(cprExecutionBinding)
	return binding, ok
}

func (s *CPRPoolService) Start() {
	if s == nil || s.client == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		check := func() {
			ctx, cancel := context.WithTimeout(s.runContext, 10*time.Second)
			defer cancel()
			_, err := s.client.CheckRuntime(ctx)
			if err == nil {
				err = s.client.Admin(ctx, cpr.ListAccounts, url.Values{"page": {"1"}, "pageSize": {"1"}}, nil, nil)
			}
			setCPRDriverAvailable(err == nil)
		}
		check()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		keyCleanup := time.NewTicker(5 * time.Minute)
		defer keyCleanup.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-ticker.C:
				check()
			case <-keyCleanup.C:
				ctx, cancel := context.WithTimeout(s.runContext, 30*time.Second)
				if err := s.RevokeStaleKeys(ctx); err != nil && ctx.Err() == nil {
					logger.LegacyPrintf("service.cpr", "CPR internal authorization cleanup failed: %v", err)
				}
				cancel()
			}
		}
	}()
}

func (s *CPRPoolService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.stop != nil {
			close(s.stop)
		}
	})
	s.wg.Wait()
	s.provisioner.Stop()
	if s.client != nil {
		s.client.CloseIdleConnections()
	}
	setCPRDriverAvailable(false)
}

func (s *SettingService) CPRPools() *CPRPoolService {
	if s == nil {
		return nil
	}
	return s.cprPools
}
