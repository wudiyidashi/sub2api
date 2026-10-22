package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/cpr"
)

// RevokeStaleKeys 的读取/远端失败保持记录供下轮恢复，不能用空查询结果清除授权。
func (s *CPRPoolService) RevokeStaleKeys(ctx context.Context) error {
	if s == nil || s.keys == nil || s.accounts == nil || s.settings == nil || s.provisioner == nil {
		return nil
	}
	settings, err := s.settings.GetAll(ctx)
	if err != nil {
		return err
	}
	for storageKey, value := range settings {
		if !strings.HasPrefix(storageKey, cpr.KeyBindingPrefix) {
			continue
		}
		name := strings.TrimPrefix(storageKey, cpr.KeyBindingPrefix)
		var binding cpr.PersistedKeyBinding
		if json.Unmarshal([]byte(value), &binding) != nil || binding.ID == "" || binding.Scope.APIKeyID <= 0 {
			continue
		}
		valid, err := s.keyBindingIsCurrent(ctx, name, binding)
		if err != nil {
			return err
		}
		if valid {
			continue
		}
		if err := s.provisioner.Revoke(ctx, name, binding.ID); err != nil {
			return err
		}
		if err := s.settings.Delete(ctx, storageKey); err != nil {
			return err
		}
	}
	return nil
}

func (s *CPRPoolService) keyBindingIsCurrent(ctx context.Context, name string, binding cpr.PersistedKeyBinding) (bool, error) {
	key, err := s.keys.GetByID(ctx, binding.Scope.APIKeyID)
	if errors.Is(err, ErrAPIKeyNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if key == nil || key.Status != StatusActive || key.IsExpired() || key.GroupID == nil || *key.GroupID != binding.Scope.GroupID {
		return false, nil
	}
	if key.User != nil && key.User.Status != StatusActive {
		return false, nil
	}
	accounts, err := s.accounts.ListByGroup(ctx, *key.GroupID)
	if err != nil {
		return false, err
	}
	for i := range accounts {
		account := &accounts[i]
		if !account.IsCPRManaged() || !account.Schedulable || account.Status != StatusActive || account.Type != AccountTypeAPIKey {
			continue
		}
		pool, err := account.CPRPoolBinding()
		if err != nil {
			return false, err
		}
		if pool.Provider != binding.Scope.Provider {
			continue
		}
		current, err := (cpr.Scope{Deployment: s.config.DeploymentID, APIKeyID: key.ID, GroupID: *key.GroupID, Provider: pool.Provider, AccountGroupIDs: pool.AccountGroupIDs}).Key([]byte(s.config.BridgeSecret))
		if err != nil {
			return false, err
		}
		if current.Name() == name {
			return true, nil
		}
	}
	return false, nil
}
