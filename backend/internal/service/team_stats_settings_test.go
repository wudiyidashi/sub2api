//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type teamStatsRepoStub struct {
	SettingRepository
	raw string
	err error
}

func (r *teamStatsRepoStub) GetValue(context.Context, string) (string, error) { return r.raw, r.err }
func (r *teamStatsRepoStub) Set(_ context.Context, key, value string) error {
	if key != SettingKeyTeamStatsConfig {
		panic("unexpected setting key")
	}
	r.raw = value
	return r.err
}

func TestTeamStatsConfigPersistence(t *testing.T) {
	repo := &teamStatsRepoStub{err: ErrSettingNotFound}
	svc := NewSettingService(repo, nil)
	cfg, err := svc.GetTeamStatsConfig(context.Background())
	require.NoError(t, err)
	require.Empty(t, cfg.Groups)
	repo.err = nil
	cfg.Groups = []TeamStatsGroup{{ID: "alice", Name: " Alice ", Color: "#123abc", APIKeyIDs: []int64{10, 11}}}
	require.NoError(t, svc.UpdateTeamStatsConfig(context.Background(), cfg))
	loaded, err := svc.GetTeamStatsConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, "Alice", loaded.Groups[0].Name)
	require.Equal(t, []int64{10, 11}, loaded.Groups[0].APIKeyIDs)

	repo.err = errors.New("database unavailable")
	_, err = svc.GetTeamStatsConfig(context.Background())
	require.ErrorContains(t, err, "database unavailable")
	// Failed reads never fall back to an empty configuration that can erase shared groups.
	repo.err = nil
	repo.raw = "invalid-json"
	_, err = svc.GetTeamStatsConfig(context.Background())
	require.Error(t, err)
}

func TestTeamStatsConfigRejectsInvalidChanges(t *testing.T) {
	for _, name := range []string{"timezone", "refresh", "duplicate_key", "duplicate_group", "color", "too_many_groups"} {
		t.Run(name, func(t *testing.T) {
			cfg := DefaultTeamStatsConfig()
			cfg.Groups = []TeamStatsGroup{{ID: "alice", Name: "Alice", Color: "#123abc", APIKeyIDs: []int64{10}}}
			switch name {
			case "timezone":
				cfg.Timezone = "not/a/timezone"
			case "refresh":
				cfg.RefreshInterval = 1
			case "duplicate_key":
				cfg.Groups = append(cfg.Groups, TeamStatsGroup{ID: "bob", Name: "Bob", Color: "#123abc", APIKeyIDs: []int64{10}})
			case "duplicate_group":
				cfg.Groups = append(cfg.Groups, cfg.Groups[0])
			case "color":
				cfg.Groups[0].Color = "url(https://example.com)"
			case "too_many_groups":
				cfg.Groups = make([]TeamStatsGroup, 101)
			}
			repo := &teamStatsRepoStub{raw: "unchanged"}
			svc := NewSettingService(repo, nil)
			require.Error(t, svc.UpdateTeamStatsConfig(context.Background(), cfg))
			require.Equal(t, "unchanged", repo.raw)
		})
	}
}
