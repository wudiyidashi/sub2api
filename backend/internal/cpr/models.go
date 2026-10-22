package cpr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// Models 在与执行相同的 Client Key 范围内发现模型，不使用管理身份读取全库目录。
func (c *Client) Models(ctx context.Context, secret string) ([]string, error) {
	if secret == "" || strings.ContainsAny(secret, "\r\n") {
		return nil, errors.New("invalid CPR model authorization")
	}
	target := *c.base
	target.Path = "/v1/models"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, errors.New("invalid CPR model request")
	}
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := c.admin.Do(request)
	if err != nil {
		return nil, errors.New("CPR model catalog is unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, &Error{Status: response.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxAdminResponseBytes+1))
	if err != nil || len(raw) > maxAdminResponseBytes {
		return nil, errors.New("invalid CPR model catalog")
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &list) != nil || list.Data == nil {
		return nil, errors.New("invalid CPR model catalog")
	}
	ids := make([]string, 0, len(list.Data))
	for _, model := range list.Data {
		if model.ID != "" {
			ids = append(ids, model.ID)
		}
	}
	return ids, nil
}
