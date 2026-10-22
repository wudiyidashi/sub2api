package cpr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

// OpenAdminResource 只用于已列入合同的事件流和头像；不接受任意下载 URL。
func (c *Client) OpenAdminResource(ctx context.Context, operation Operation, query url.Values) (*http.Response, error) {
	method, exists := operationMethods[operation]
	if !exists || method != http.MethodGet || operation.Kind() == "json" {
		return nil, errors.New("unsupported CPR management resource")
	}
	target := *c.base
	target.Path = operation.Path()
	target.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, errors.New("invalid CPR management resource request")
	}
	request.Header.Set("x-api-key", c.adminKey)
	request.Header.Set("X-Request-ID", uuid.NewString())
	client := c.admin
	if operation.Kind() == "stream" {
		client = c.data
	}
	result, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("CPR management resource is unavailable")
	}
	if result.StatusCode >= 200 && result.StatusCode < 300 {
		media, _, _ := strings.Cut(result.Header.Get("Content-Type"), ";")
		valid := false
		if operation.Kind() == "stream" {
			valid = strings.TrimSpace(media) == "text/event-stream"
		} else if operation.Kind() == "download" {
			valid = strings.TrimSpace(media) == "application/json"
		} else {
			switch strings.TrimSpace(media) {
			case "image/png", "image/jpeg", "image/webp", "image/avif", "image/gif":
				valid = true
			}
		}
		if valid {
			return result, nil
		}
		_ = result.Body.Close()
		return nil, errors.New("unexpected CPR management resource type")
	}
	raw, _ := io.ReadAll(io.LimitReader(result.Body, maxAdminResponseBytes))
	_ = result.Body.Close()
	var envelope struct {
		Code uint32 `json:"code"`
	}
	_ = json.Unmarshal(raw, &envelope)
	return nil, &Error{Status: result.StatusCode, Code: envelope.Code}
}
