package cpr

import (
	"context"
	"errors"
	"strings"
)

// 接口和页面同时固定在该版本；升级时必须一并复核管理合同与执行能力。
const SourceCommit = "78bc57c5cf24b01a38e985cdd739bb4ae89ce11f"
const SourceVersion = "3.29.7"

func (c *Client) CheckRuntime(ctx context.Context) (string, error) {
	var runtime struct {
		Version string `json:"version"`
		GitSha  string `json:"gitSha"`
	}
	if err := c.Admin(ctx, Operation("/api/admin/system/version"), nil, nil, &runtime); err != nil {
		return "", err
	}
	if strings.TrimPrefix(runtime.Version, "v") != SourceVersion || (runtime.GitSha != "" && !strings.HasPrefix(SourceCommit, runtime.GitSha)) {
		return runtime.Version, errors.New("CPR runtime does not match the pinned management contract")
	}
	return runtime.Version, nil
}
