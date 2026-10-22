package service

import (
	"context"
	"sort"

	"github.com/gin-gonic/gin"
)

func (s *OpenAIGatewayService) CPRModelIDs(ctx context.Context, c *gin.Context, platform string) ([]string, bool, error) {
	if s == nil || s.cprPools == nil || s.cprPools.client == nil || s.cprPools.accounts == nil {
		return nil, false, nil
	}
	groupID := getOpenAIGroupIDFromContext(c)
	if groupID <= 0 {
		return nil, false, nil
	}
	accounts, err := s.cprPools.accounts.ListByGroup(ctx, groupID)
	if err != nil {
		return nil, false, err
	}
	managed := false
	unique := map[string]struct{}{}
	for i := range accounts {
		account := &accounts[i]
		if !account.IsCPRManaged() || account.Type != AccountTypeAPIKey || (platform != PlatformComposite && account.Platform != platform) {
			continue
		}
		managed = true
		if !account.Schedulable || account.Status != StatusActive {
			continue
		}
		executionCtx, _, err := s.prepareCPRGatewayAccount(ctx, c, account)
		if err != nil {
			return nil, true, err
		}
		binding, _ := cprBindingFromContext(executionCtx)
		ids, err := s.cprPools.client.Models(executionCtx, binding.key.Secret())
		if err != nil {
			return nil, true, err
		}
		for _, id := range ids {
			unique[id] = struct{}{}
		}
	}
	ids := make([]string, 0, len(unique))
	for id := range unique {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, managed, nil
}
