package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const SettingKeyTeamStatsConfig = "team_stats_config"

type TeamStatsGroup struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Color     string  `json:"color"`
	APIKeyIDs []int64 `json:"apiKeyIds"`
	Note      string  `json:"note,omitempty"`
}

type TeamStatsConfig struct {
	Timezone        string           `json:"timezone"`
	RefreshInterval int              `json:"refreshInterval"`
	Currency        string           `json:"currency"`
	CostSource      string           `json:"costSource"`
	Theme           string           `json:"theme"`
	Groups          []TeamStatsGroup `json:"groups"`
	TZVersion       int              `json:"tzVersion"`
}

func DefaultTeamStatsConfig() *TeamStatsConfig {
	return &TeamStatsConfig{
		Timezone: "Asia/Shanghai", RefreshInterval: 60, Currency: "USD",
		CostSource: "actual", Theme: "auto", Groups: []TeamStatsGroup{}, TZVersion: 2,
	}
}

var teamStatsIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
var teamStatsColorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

func (cfg *TeamStatsConfig) Validate() error {
	invalid := func(message string) error { return infraerrors.BadRequest("INVALID_TEAM_STATS_CONFIG", message) }
	if cfg.Timezone == "" || len(cfg.Timezone) > 100 {
		return invalid("invalid timezone")
	}
	if _, err := time.LoadLocation(cfg.Timezone); err != nil {
		return invalid("invalid timezone")
	}
	if cfg.RefreshInterval != 0 && (cfg.RefreshInterval < 30 || cfg.RefreshInterval > 86400) {
		return invalid("refresh interval must be 0 or between 30 and 86400 seconds")
	}
	if cfg.Currency != "USD" || (cfg.CostSource != "actual" && cfg.CostSource != "total") ||
		(cfg.Theme != "auto" && cfg.Theme != "light" && cfg.Theme != "dark") || cfg.TZVersion != 2 {
		return invalid("invalid display preferences")
	}
	if len(cfg.Groups) > 100 {
		return invalid("at most 100 groups are allowed")
	}
	groups := make(map[string]bool)
	keys := make(map[int64]bool)
	for i := range cfg.Groups {
		g := &cfg.Groups[i]
		g.Name = strings.TrimSpace(g.Name)
		if !teamStatsIDPattern.MatchString(g.ID) || groups[g.ID] || g.Name == "" ||
			utf8.RuneCountInString(g.Name) > 100 || !teamStatsColorPattern.MatchString(g.Color) ||
			utf8.RuneCountInString(g.Note) > 1000 || len(g.APIKeyIDs) > 1000 {
			return invalid("invalid or duplicate group")
		}
		groups[g.ID] = true
		for _, id := range g.APIKeyIDs {
			if id <= 0 || id > 9007199254740991 || keys[id] {
				return invalid("API key IDs must be positive and assigned to only one group")
			}
			keys[id] = true
			if len(keys) > 1000 {
				return invalid("at most 1000 API keys are allowed across all groups")
			}
		}
		if g.APIKeyIDs == nil {
			g.APIKeyIDs = []int64{}
		}
	}
	if cfg.Groups == nil {
		cfg.Groups = []TeamStatsGroup{}
	}
	return nil
}

func (s *SettingService) GetTeamStatsConfig(ctx context.Context) (*TeamStatsConfig, error) {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyTeamStatsConfig)
	if errors.Is(err, ErrSettingNotFound) {
		return DefaultTeamStatsConfig(), nil
	}
	if err != nil {
		return nil, err
	}
	cfg := DefaultTeamStatsConfig()
	if err := json.Unmarshal([]byte(raw), cfg); err != nil {
		return nil, fmt.Errorf("decode team statistics configuration: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("stored team statistics configuration: %w", err)
	}
	return cfg, nil
}

func (s *SettingService) UpdateTeamStatsConfig(ctx context.Context, cfg *TeamStatsConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, SettingKeyTeamStatsConfig, string(raw))
}
