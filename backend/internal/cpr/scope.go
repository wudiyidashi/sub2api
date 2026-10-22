package cpr

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

// Scope 是业务鉴权之后得到的 CPR 账号池授权，不接受客户端请求头声明的账号范围。
type Scope struct {
	Deployment      string
	APIKeyID        int64
	GroupID         int64
	Provider        string
	AccountGroupIDs []string
}

type ScopedKey struct {
	name   string
	value  string
	groups []string
	scope  Scope
}

func (k ScopedKey) Secret() string   { return k.value }
func (k ScopedKey) Name() string     { return k.name }
func (k ScopedKey) Groups() []string { return slices.Clone(k.groups) }
func (k ScopedKey) String() string   { return k.name }
func (k ScopedKey) GoString() string { return k.name }
func (k ScopedKey) Scope() Scope     { scope := k.scope; scope.AccountGroupIDs = k.Groups(); return scope }
func (k ScopedKey) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Name   string   `json:"name"`
		Groups []string `json:"groups"`
	}{Name: k.name, Groups: k.Groups()})
}

func ProviderForPlatform(platform string) (string, error) {
	switch platform {
	case "openai":
		return "openai", nil
	case "grok":
		return "xai", nil
	default:
		return "", errors.New("platform is not supported by CPR account management")
	}
}

func (s Scope) Key(secret []byte) (ScopedKey, error) {
	if len(secret) < 32 || strings.TrimSpace(s.Deployment) == "" || s.APIKeyID <= 0 || s.GroupID <= 0 || (s.Provider != "openai" && s.Provider != "xai") {
		return ScopedKey{}, errors.New("invalid CPR authorization scope")
	}
	groups := slices.Clone(s.AccountGroupIDs)
	// CPR 的空账号组代表全账号；业务授权为空时必须失败，不能生成无限范围的 Key。
	if len(groups) == 0 || len(groups) > 1000 {
		return ScopedKey{}, errors.New("CPR authorization requires an explicit nonempty account pool")
	}
	for _, id := range groups {
		if id == "" || len(id) > 128 || strings.TrimSpace(id) != id {
			return ScopedKey{}, errors.New("invalid CPR account group")
		}
		for _, r := range id {
			valid := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
			if !valid {
				return ScopedKey{}, errors.New("invalid CPR account group")
			}
		}
	}
	slices.Sort(groups)
	groups = slices.Compact(groups)
	s.AccountGroupIDs = groups
	raw, err := json.Marshal(s)
	if err != nil {
		return ScopedKey{}, errors.New("invalid CPR authorization scope")
	}
	sum := hmac.New(sha256.New, secret)
	_, _ = sum.Write([]byte("sub2api:cpr:client-key:v1:"))
	_, _ = sum.Write(raw)
	digest := sum.Sum(nil)
	return ScopedKey{
		name:   "sub2api-" + hex.EncodeToString(digest[:16]),
		value:  "sk-sub2api-" + hex.EncodeToString(digest),
		groups: groups,
		scope:  s,
	}, nil
}
