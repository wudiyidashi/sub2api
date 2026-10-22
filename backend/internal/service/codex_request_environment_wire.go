package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 只更新官方结构化时间上下文与显式地域工具字段，不重建业务消息或整个 JSON
func alignCodexRequestEnvironment(body []byte, e *CodexRequestEnvironment, now time.Time) ([]byte, error) {
	if e == nil || !json.Valid(body) {
		return body, nil
	}
	zone, err := time.LoadLocation(e.Timezone)
	if err != nil {
		return nil, err
	}
	root := gjson.ParseBytes(body)
	normalized := body
	set := func(path string, value any) error {
		next, setErr := sjson.SetBytes(normalized, path, value)
		if setErr == nil {
			normalized = next
		}
		return setErr
	}
	for _, wrapper := range []string{"response", "session"} {
		if nested := root.Get(wrapper); nested.IsObject() && (root.Get("type").String() == "session.update" || root.Get("type").String() == "response.create") {
			aligned, nestedErr := alignCodexRequestEnvironment([]byte(nested.Raw), e, now)
			if nestedErr != nil {
				return nil, nestedErr
			}
			if !bytes.Equal(aligned, []byte(nested.Raw)) {
				next, setErr := sjson.SetRawBytes(normalized, wrapper, aligned)
				if setErr != nil {
					return nil, setErr
				}
				normalized = next
			}
		}
	}
	for i, item := range root.Get("input").Array() {
		tag, kind := "", ""
		switch item.Get("role").String() {
		case "user":
			tag = "environment_context"
			kind = "environments.environment_context"
		case "developer":
			tag = "codex_apps_client_time_context"
			kind = "additional_content.codex_apps_client_time_context"
		default:
			continue
		}
		kinds := item.Get("internal_chat_message_metadata_passthrough.content_item_kinds")
		for j, part := range item.Get("content").Array() {
			if part.Get("type").String() != "input_text" {
				continue
			}
			if kinds.Exists() {
				if !kinds.IsArray() {
					continue
				}
				list := kinds.Array()
				if j >= len(list) || list[j].String() != kind {
					continue
				}
			}
			text := part.Get("text").String()
			aligned, ok := alignCodexEnvironmentContext(text, tag, e, now.In(zone))
			if !ok || aligned == text {
				continue
			}
			if err := set(fmt.Sprintf("input.%d.content.%d.text", i, j), aligned); err != nil {
				return nil, err
			}
		}
	}
	for i, tool := range root.Get("tools").Array() {
		typ := tool.Get("type").String()
		if typ != "web_search" && !strings.HasPrefix(typ, "web_search_") {
			continue
		}
		location := map[string]string{"type": "approximate", "country": e.CountryCode, "timezone": e.Timezone}
		if e.Region != "" {
			location["region"] = e.Region
		}
		if e.City != "" {
			location["city"] = e.City
		}
		// 指定搜索位置是此次账号地域策略的一部分，其他工具选项保持不变
		if err := set(fmt.Sprintf("tools.%d.user_location", i), location); err != nil {
			return nil, err
		}
	}
	return normalized, nil
}

type environmentTextReplacement struct {
	start, end int
	value      string
}

func alignCodexEnvironmentContext(text, tag string, e *CodexRequestEnvironment, now time.Time) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "<"+tag+">") || !strings.HasSuffix(trimmed, "</"+tag+">") {
		return text, false
	}
	// encoding/xml 不加载外部实体；保持 Strict=true 且不注册自定义 Entity 或外部资源解析器
	decoder := xml.NewDecoder(strings.NewReader(text)) // #nosec G709 -- 使用流式 Token 校验内嵌上下文，不解析外部实体
	depth := 0
	start := 0
	name := ""
	closing := 0
	replacements := []environmentTextReplacement{}
	seen := map[string]bool{}
	values := map[string]string{"timezone": e.Timezone, "current_date": now.Format("2006-01-02"), "country": e.CountryCode, "region": e.Region, "city": e.City, "utc_offset": now.Format("-07:00"), "ip": e.IP, "ip_address": e.IP, "country_code": e.CountryCode, "current_datetime": now.Format(time.RFC3339), "current_time": now.Format("15:04:05")}
	for {
		offset := int(decoder.InputOffset())
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return text, false
		}
		switch t := token.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 && t.Name.Local != tag {
				return text, false
			}
			if depth == 2 {
				start = offset
				name = t.Name.Local
			}
		case xml.EndElement:
			if depth == 2 {
				if value, ok := values[name]; ok {
					var escaped bytes.Buffer
					_ = xml.EscapeText(&escaped, []byte(value))
					replacements = append(replacements, environmentTextReplacement{start, int(decoder.InputOffset()), "<" + name + ">" + escaped.String() + "</" + name + ">"})
					seen[name] = true
				}
			}
			if depth == 1 {
				closing = offset
			}
			depth--
		}
	}
	// 已识别的系统上下文中补齐缺失时间字段，普通用户文本永远不会走到这里
	missing := ""
	for _, key := range []string{"current_date", "timezone"} {
		if !seen[key] {
			missing += "<" + key + ">" + values[key] + "</" + key + ">\n"
		}
	}
	if missing != "" {
		replacements = append(replacements, environmentTextReplacement{closing, closing, missing})
	}
	for i := len(replacements) - 1; i >= 0; i-- {
		r := replacements[i]
		text = text[:r.start] + r.value + text[r.end:]
	}
	return text, true
}

type codexEnvironmentContextKey struct{}
type codexEnvironmentSnapshot struct {
	accountID   int64
	proxy       string
	environment *CodexRequestEnvironment
}

func (s *OpenAIGatewayService) pinCodexEnvironment(ctx context.Context, account *Account) (context.Context, error) {
	e, err := s.codexEnvironment(ctx, account)
	if err != nil {
		return ctx, err
	}
	if e == nil {
		return ctx, nil
	}
	_, proxy, err := accountEnvironmentRoute(account)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, codexEnvironmentContextKey{}, codexEnvironmentSnapshot{account.ID, proxy, e}), nil
}
func codexEnvironmentPoolKey(ctx context.Context) string {
	snapshot, ok := ctx.Value(codexEnvironmentContextKey{}).(codexEnvironmentSnapshot)
	if !ok || snapshot.environment == nil {
		return ""
	}
	e := snapshot.environment
	// 地域变化必须隔离已有连接，不能把新出口上下文写入旧出口的 socket
	digest := sha256.Sum256([]byte(snapshot.proxy + "|" + e.IP + "|" + e.Timezone + "|" + e.CountryCode + "|" + e.Region + "|" + e.City))
	return hex.EncodeToString(digest[:])
}
func (s *OpenAIGatewayService) codexEnvironment(ctx context.Context, account *Account) (*CodexRequestEnvironment, error) {
	if account == nil || !account.UsesOpenAICodexProtocol() || s == nil || s.settingService.RequestEnvironments() == nil {
		return nil, nil
	}
	if snapshot, ok := ctx.Value(codexEnvironmentContextKey{}).(codexEnvironmentSnapshot); ok && snapshot.accountID == account.ID {
		_, proxy, routeErr := accountEnvironmentRoute(account)
		if routeErr != nil {
			return nil, routeErr
		}
		if snapshot.proxy == proxy {
			return snapshot.environment, nil
		}
	}
	return s.settingService.RequestEnvironments().Resolve(ctx, account)
}
func (s *OpenAIGatewayService) alignCodexEnvironmentBody(ctx context.Context, account *Account, body []byte) ([]byte, error) {
	e, err := s.codexEnvironment(ctx, account)
	if err != nil {
		return nil, err
	}
	return alignCodexRequestEnvironment(body, e, time.Now())
}
func (s *OpenAIGatewayService) normalizeWSRequestEnvironment(ctx context.Context, body []byte, account *Account, lite bool) ([]byte, bool, error) {
	normalized, changed, err := normalizeOpenAIResponsesWebSocketCompatibilityBodyWithConfig(s.cfg, body, account, lite)
	if err != nil {
		return body, false, err
	}
	aligned, err := s.alignCodexEnvironmentBody(ctx, account, normalized)
	return aligned, changed || !bytes.Equal(aligned, body), err
}
func alignCodexHTTPRequestEnvironment(request *http.Request, account *Account, resolver *AccountRequestEnvironmentService, proxyURL string) error {
	if resolver == nil || account == nil || !account.UsesOpenAICodexProtocol() {
		return nil
	}
	// 限定 ChatGPT Codex 数据面，不改写其他 API Key 或 OAuth 管理接口
	if request.URL == nil || request.URL.Hostname() != "chatgpt.com" || !strings.Contains(request.URL.Path, "/codex/") {
		return nil
	}
	_, configuredProxy, routeErr := accountEnvironmentRoute(account)
	if routeErr != nil {
		return routeErr
	}
	if configuredProxy != proxyURL {
		return fmt.Errorf("codex transport proxy does not match the account environment route")
	}
	var e *CodexRequestEnvironment
	var err error
	if snapshot, ok := request.Context().Value(codexEnvironmentContextKey{}).(codexEnvironmentSnapshot); ok && snapshot.accountID == account.ID {
		_, proxy, routeErr := accountEnvironmentRoute(account)
		if routeErr != nil {
			return routeErr
		}
		if snapshot.proxy == proxy {
			e = snapshot.environment
		}
	}
	if e == nil {
		e, err = resolver.Resolve(request.Context(), account)
	}
	if err != nil {
		return err
	}
	if request.Body == nil {
		return nil
	}
	if request.GetBody == nil {
		return fmt.Errorf("codex request body must be replayable for environment alignment")
	}
	if encoding := request.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return fmt.Errorf("encoded Codex body cannot be aligned before transport compression")
	}
	reader, err := request.GetBody()
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(reader, (256<<20)+1))
	_ = reader.Close()
	if err != nil {
		return err
	}
	if len(raw) > (256 << 20) {
		return fmt.Errorf("codex request exceeds environment alignment limit")
	}
	aligned, err := alignCodexRequestEnvironment(raw, e, time.Now())
	if err != nil {
		return err
	}
	if bytes.Equal(raw, aligned) {
		return nil
	}
	_ = request.Body.Close()
	request.Body = io.NopCloser(bytes.NewReader(aligned))
	request.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(aligned)), nil }
	request.ContentLength = int64(len(aligned))
	request.Header.Del("Content-Length")
	// 下游统计与审计读取最终上游请求，长度必须同步
	request.Header.Del("Content-MD5")
	return nil
}
