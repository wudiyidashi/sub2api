package dto

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountDriverDefaultIncludedInFullAndLiteResponses(t *testing.T) {
	account := &service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": "secret"}}
	full := AccountFromService(account)
	lite := AccountListItemFromAccount(full)
	require.Equal(t, "native", full.AccountDriver.ID)
	require.Zero(t, full.AccountDriver.BindingRevision)
	require.Equal(t, full.AccountDriver, lite.AccountDriver)
	require.NotContains(t, full.Credentials, "access_token")
	require.NotContains(t, lite.Credentials, "access_token")
	require.Nil(t, AccountFromService(nil))
}
