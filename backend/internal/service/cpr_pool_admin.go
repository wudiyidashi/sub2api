package service

import (
	"context"
	"errors"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/accountdriver"
	"github.com/Wei-Shaw/sub2api/internal/cpr"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// CPRPoolBindingRepository 的条件更新防止两个管理会话覆盖彼此的授权范围。
type CPRPoolBindingRepository interface {
	UpdateCPRPoolBinding(context.Context, *Account, int64) error
}

type CPRPoolAdminService struct {
	client   *cpr.Client
	accounts AdminAccountRepository
	groups   GroupRepository
}

type CPRPoolView struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	CPRPoolBinding
	Enabled     bool  `json:"enabled"`
	Concurrency int   `json:"concurrency"`
	Revision    int64 `json:"revision"`
}

type CPRPoolInput struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	CPRPoolBinding
	Enabled          bool  `json:"enabled"`
	Concurrency      int   `json:"concurrency"`
	ExpectedRevision int64 `json:"expected_revision"`
}

func NewCPRPoolAdminService(client *cpr.Client, accounts AdminAccountRepository, groups GroupRepository) *CPRPoolAdminService {
	return &CPRPoolAdminService{client: client, accounts: accounts, groups: groups}
}

func cprPoolView(account *Account) (*CPRPoolView, error) {
	binding, err := account.CPRPoolBinding()
	if err != nil {
		return nil, err
	}
	return &CPRPoolView{ID: account.ID, Name: account.Name, CPRPoolBinding: *binding, Enabled: account.Schedulable && account.Status == StatusActive, Concurrency: account.Concurrency, Revision: account.AccountDriverRevision}, nil
}

func (s *CPRPoolAdminService) List(ctx context.Context) ([]CPRPoolView, error) {
	if s.accounts == nil {
		return nil, errors.New("CPR pool management is unavailable")
	}
	var views []CPRPoolView
	for _, platform := range []string{PlatformOpenAI, PlatformGrok} {
		items, err := s.accounts.ListByPlatform(ctx, platform)
		if err != nil {
			return nil, err
		}
		for i := range items {
			if !items[i].IsCPRManaged() || items[i].Type != AccountTypeAPIKey {
				continue
			}
			view, err := cprPoolView(&items[i])
			if err != nil {
				return nil, err
			}
			views = append(views, *view)
		}
	}
	if views == nil {
		views = []CPRPoolView{}
	}
	return views, nil
}

func (s *CPRPoolAdminService) validate(ctx context.Context, input CPRPoolInput) (string, error) {
	if s.client == nil || s.accounts == nil || s.groups == nil {
		return "", errors.New("CPR pool management is unavailable")
	}
	if strings.TrimSpace(input.Name) == "" || len(input.Name) > 128 || input.Concurrency < 0 || input.Concurrency > 10000 {
		return "", infraerrors.BadRequest("CPR_POOL_INVALID", "invalid pool name or concurrency")
	}
	platform := PlatformOpenAI
	if input.Provider == "xai" {
		platform = PlatformGrok
	} else if input.Provider != "openai" {
		return "", infraerrors.BadRequest("CPR_POOL_PROVIDER_INVALID", "unsupported CPR provider")
	}
	group, err := s.groups.GetByID(ctx, input.GroupID)
	if err != nil {
		return "", err
	}
	if group == nil || (group.Platform != platform && group.Platform != PlatformComposite) {
		return "", infraerrors.BadRequest("CPR_POOL_GROUP_INVALID", "pool provider does not match the business group")
	}
	if input.Enabled {
		accounts, err := s.accounts.ListByGroup(ctx, input.GroupID)
		if err != nil {
			return "", err
		}
		for _, account := range accounts {
			if account.Platform == platform && !account.IsCPRManaged() && account.Schedulable && account.Status == StatusActive {
				return "", infraerrors.Conflict("CPR_POOL_NATIVE_OWNERSHIP", "migrate or disable native accounts in this business group before enabling the CPR pool")
			}
		}
	}
	if _, err := (cpr.Scope{Deployment: "validate", APIKeyID: 1, GroupID: input.GroupID, Provider: input.Provider, AccountGroupIDs: input.AccountGroupIDs}).Key([]byte(strings.Repeat("v", 32))); err != nil {
		return "", infraerrors.BadRequest("CPR_POOL_SCOPE_INVALID", "select at least one explicit CPR account group")
	}
	// 账号组支持跨平台混用；业务池要求所选组均只属于本 Provider，避免模型路由跨平台。
	wanted := make(map[string]bool, len(input.AccountGroupIDs))
	for _, id := range input.AccountGroupIDs {
		wanted[id] = true
	}
	for page := 1; len(wanted) > 0 && page <= 1000; page++ {
		var result struct {
			Items []struct {
				ID             string           `json:"id"`
				Enabled        bool             `json:"enabled"`
				ProviderCounts map[string]int64 `json:"providerCounts"`
			} `json:"items"`
			Page struct {
				TotalPages int `json:"totalPages"`
			} `json:"page"`
		}
		if err := s.client.Admin(ctx, cpr.ListGroups, url.Values{"page": {strconv.Itoa(page)}, "pageSize": {"100"}}, nil, &result); err != nil {
			return "", err
		}
		for _, item := range result.Items {
			if !wanted[item.ID] {
				continue
			}
			if !item.Enabled {
				return "", infraerrors.BadRequest("CPR_POOL_GROUP_DISABLED", "selected CPR account group is disabled")
			}
			for provider, count := range item.ProviderCounts {
				if provider != input.Provider && count > 0 {
					return "", infraerrors.BadRequest("CPR_POOL_MIXED_PROVIDER", "selected CPR account group contains another provider")
				}
			}
			if input.Enabled && item.ProviderCounts[input.Provider] == 0 {
				return "", infraerrors.BadRequest("CPR_POOL_EMPTY", "cannot enable an empty CPR account pool")
			}
			delete(wanted, item.ID)
		}
		if page >= result.Page.TotalPages {
			break
		}
	}
	if len(wanted) != 0 {
		return "", infraerrors.BadRequest("CPR_POOL_GROUP_MISSING", "selected CPR account group no longer exists")
	}
	return platform, nil
}

func (s *CPRPoolAdminService) Save(ctx context.Context, input CPRPoolInput) (*CPRPoolView, error) {
	platform, err := s.validate(ctx, input)
	if err != nil {
		return nil, err
	}
	input.Name = strings.TrimSpace(input.Name)
	input.AccountGroupIDs = slices.Clone(input.AccountGroupIDs)
	slices.Sort(input.AccountGroupIDs)
	input.AccountGroupIDs = slices.Compact(input.AccountGroupIDs)
	if input.ID == 0 {
		account := &Account{AccountDriverID: accountdriver.CPRManagedID, AccountDriverRevision: 1, Name: input.Name, Platform: platform, Type: AccountTypeAPIKey, Credentials: map[string]any{}, Extra: map[string]any{"cpr_pool": input.CPRPoolBinding}, Concurrency: input.Concurrency, Status: StatusActive, Schedulable: input.Enabled, GroupIDs: []int64{input.GroupID}}
		if err := ValidateAccountDriverState(account); err != nil {
			return nil, err
		}
		if err := s.accounts.CreateWithAccountGroups(ctx, account, []AccountGroup{{GroupID: input.GroupID}}); err != nil {
			return nil, err
		}
		return cprPoolView(account)
	}
	account, err := s.accounts.GetByID(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	binding, err := account.CPRPoolBinding()
	if err != nil {
		return nil, err
	}
	if binding.GroupID != input.GroupID || binding.Provider != input.Provider {
		return nil, infraerrors.BadRequest("CPR_POOL_IDENTITY_IMMUTABLE", "business group and provider cannot be changed on an existing pool")
	}
	if account.AccountDriverRevision != input.ExpectedRevision {
		return nil, infraerrors.Conflict("CPR_POOL_CHANGED", "pool changed; reload before saving")
	}
	account.Extra = maps.Clone(account.Extra)
	account.Extra["cpr_pool"] = input.CPRPoolBinding
	account.Name, account.Concurrency, account.Schedulable = input.Name, input.Concurrency, input.Enabled
	repo, ok := s.accounts.(CPRPoolBindingRepository)
	if !ok {
		return nil, errors.New("CPR pool conditional persistence is unavailable")
	}
	if err := repo.UpdateCPRPoolBinding(ctx, account, input.ExpectedRevision); err != nil {
		return nil, err
	}
	return cprPoolView(account)
}
