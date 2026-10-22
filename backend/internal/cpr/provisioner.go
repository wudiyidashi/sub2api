package cpr

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// KeyBindingStore 只保存内部 Key 的 CPR ID，不保存明文 Key 或真实账号令牌。
type KeyBindingStore interface {
	Set(context.Context, string, string) error
}

const KeyBindingPrefix = "cpr_client_key_"

type PersistedKeyBinding struct {
	ID    string `json:"id"`
	Scope Scope  `json:"scope"`
}

type Provisioner struct {
	client   *Client
	store    KeyBindingStore
	mu       sync.Mutex
	checked  map[string]time.Time
	flight   singleflight.Group
	lifetime context.Context
	closed   bool
	wg       sync.WaitGroup
}

type keyRecord struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Groups  []struct {
		ID string `json:"id"`
	} `json:"groups"`
}

func NewProvisioner(client *Client, store KeyBindingStore) *Provisioner {
	return NewProvisionerWithContext(context.Background(), client, store)
}

func NewProvisionerWithContext(lifetime context.Context, client *Client, store KeyBindingStore) *Provisioner {
	return &Provisioner{client: client, store: store, checked: map[string]time.Time{}, lifetime: lifetime}
}

func (p *Provisioner) Ensure(ctx context.Context, key ScopedKey) error {
	if p == nil || p.client == nil || p.store == nil || key.Name() == "" || key.Secret() == "" || len(key.Groups()) == 0 {
		return errors.New("CPR authorization provisioning is unavailable")
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("CPR authorization provisioning has stopped")
	}
	checked := p.checked[key.Name()]
	p.mu.Unlock()
	if time.Since(checked) < 5*time.Minute {
		return nil
	}
	result := p.flight.DoChan(key.Name(), func() (any, error) {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, errors.New("CPR authorization provisioning has stopped")
		}
		p.wg.Add(1)
		p.mu.Unlock()
		defer p.wg.Done()
		// 同一授权由多个请求复用，但服务停止必须终止仍在创建的远端身份。
		ctx, cancel := context.WithTimeout(p.lifetime, 45*time.Second)
		defer cancel()
		if err := p.ensure(ctx, key); err != nil {
			return nil, err
		}
		p.mu.Lock()
		if len(p.checked) >= 1024 {
			clear(p.checked)
		}
		p.checked[key.Name()] = time.Now()
		p.mu.Unlock()
		return nil, nil
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case done := <-result:
		return done.Err
	}
}

func (p *Provisioner) Stop() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.wg.Wait()
}

func (p *Provisioner) ensure(ctx context.Context, key ScopedKey) error {
	// 查询固定名字便于恢复“远端已创建，但本地保存失败”的中断，不再创建第二份身份。
	var existing struct {
		Items []keyRecord `json:"items"`
	}
	if err := p.client.Admin(ctx, ListClientKeys, url.Values{"search": {key.Name()}, "limit": {"200"}}, nil, &existing); err != nil {
		return err
	}
	for _, record := range existing.Items {
		if record.Name != key.Name() {
			continue
		}
		if err := p.validateExisting(ctx, key, record); err != nil {
			return err
		}
		return p.remember(ctx, key, record.ID)
	}
	var created struct {
		ID           string `json:"id"`
		PlaintextKey string `json:"plaintextKey"`
	}
	input := struct {
		Name              string   `json:"name"`
		CustomKey         string   `json:"customKey"`
		GroupIDs          []string `json:"groupIds"`
		MaxConcurrency    uint64   `json:"maxConcurrency"`
		RequestsPerMinute uint64   `json:"requestsPerMinute"`
	}{Name: key.Name(), CustomKey: key.Secret(), GroupIDs: key.Groups()}
	if err := p.client.Admin(ctx, CreateClientKey, nil, input, &created); err != nil {
		var remote *Error
		if !errors.As(err, &remote) || remote.Status != 409 {
			return err
		}
		// 多宿主同时首用同一授权时，名字和自定义 Key 的唯一约束决定唯一身份。
		existing.Items = nil
		if err := p.client.Admin(ctx, ListClientKeys, url.Values{"search": {key.Name()}, "limit": {"200"}}, nil, &existing); err != nil {
			return err
		}
		for _, record := range existing.Items {
			if record.Name == key.Name() {
				if err := p.validateExisting(ctx, key, record); err != nil {
					return err
				}
				return p.remember(ctx, key, record.ID)
			}
		}
		return errors.New("CPR internal client key conflict could not be recovered")
	}
	if created.ID == "" || subtle.ConstantTimeCompare([]byte(created.PlaintextKey), []byte(key.Secret())) != 1 {
		return errors.New("CPR internal client key does not match its authorization scope")
	}
	return p.remember(ctx, key, created.ID)
}

func (p *Provisioner) validateExisting(ctx context.Context, key ScopedKey, record keyRecord) error {
	var groups []string
	for _, group := range record.Groups {
		groups = append(groups, group.ID)
	}
	slices.Sort(groups)
	groups = slices.Compact(groups)
	expected := slices.Clone(key.Groups())
	slices.Sort(expected)
	expected = slices.Compact(expected)
	if !record.Enabled || record.ID == "" || !slices.Equal(groups, expected) {
		return errors.New("CPR internal client key permissions changed; refusing execution")
	}
	// 同名配置不能被当作身份凭据；恢复时还要核对真实 Key，不只比名字或前缀。
	var revealed struct {
		PlaintextKey string `json:"plaintextKey"`
	}
	if err := p.client.Admin(ctx, RevealClientKey, url.Values{"id": {record.ID}}, nil, &revealed); err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(revealed.PlaintextKey), []byte(key.Secret())) != 1 {
		return errors.New("CPR internal client key identity changed; refusing execution")
	}
	return nil
}

func (p *Provisioner) remember(ctx context.Context, key ScopedKey, id string) error {
	raw, err := json.Marshal(PersistedKeyBinding{ID: id, Scope: key.Scope()})
	if err != nil {
		return errors.New("invalid CPR key binding")
	}
	if err := p.store.Set(ctx, KeyBindingPrefix+key.Name(), string(raw)); err != nil {
		return errors.New("failed to persist CPR key binding")
	}
	return nil
}

// Revoke 删除已失效的内部授权，不修改真实用户 Key 或 CPR 账号状态。
func (p *Provisioner) Revoke(ctx context.Context, name, id string) error {
	if name == "" || id == "" {
		return errors.New("invalid CPR key revocation")
	}
	err := p.client.Admin(ctx, DeleteClientKey, nil, struct {
		ID string `json:"id"`
	}{ID: id}, nil)
	if err != nil {
		var remote *Error
		if !errors.As(err, &remote) || remote.Status != 404 {
			return err
		}
	}
	p.mu.Lock()
	delete(p.checked, name)
	p.mu.Unlock()
	return nil
}
