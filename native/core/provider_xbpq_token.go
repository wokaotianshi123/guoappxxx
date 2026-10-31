package core

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
)

// XBPQ 令牌自动续期。
//
// 规则字段（都可省略，省略即不启用）：
//
//	刷新地址     换取新令牌的接口。可写绝对地址，也可写相对路径（/api/auth/refresh）
//	刷新令牌     refresh token 本体
//	令牌路径     从刷新响应里取新 access token 的 JSON 路径，默认 data.token
//	刷新令牌路径  从刷新响应里取新 refresh token 的路径，默认 data.refreshToken
//
// 触发时机：播放直链解析失败时，用 refresh token 换一套新令牌，改写「头部集合」里的
// Authorization 后重试一次。成功后新令牌会写回源存储，下次启动仍然有效。
//
// 注意：refresh token 本身也可能过期，且多数站点刷新后会轮换它。这里把轮换结果落盘，
// 因此不会因为反复使用旧值而失效；但绝对有效期到了仍需要用户重新登录。

const (
	// xbpqTokenCacheTTL 同一 endpoint 的刷新结果复用窗口，避免连续播放多集时
	// 并发刷新互相顶掉（refresh token 轮换后旧值立即作废）。
	xbpqTokenCacheTTL = 10 * time.Minute
	// xbpqDefaultAccessPath / xbpqDefaultRefreshPath 与常见站点（如青空次元）对齐。
	xbpqDefaultAccessPath  = "data.token"
	xbpqDefaultRefreshPath = "data.refreshToken"
)

type xbpqTokenEntry struct {
	access  string
	refresh string
	at      time.Time
}

var (
	xbpqTokenMu    sync.Mutex
	xbpqTokenCache = map[string]xbpqTokenEntry{}
)

// tokenRefreshEndpoint 刷新接口地址；未配置或非法时返回空。
func (r xbpqRule) tokenRefreshEndpoint(base string) string {
	raw := strings.TrimSpace(r.field("刷新地址", "令牌刷新地址", "刷新url", "刷新接口"))
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "/") {
		origin := strings.TrimRight(xbpqHostOrigin(firstNonEmpty(base, r.homeURL())), "/")
		if origin == "" {
			return ""
		}
		return origin + raw
	}
	if !strings.HasPrefix(raw, "http") {
		return ""
	}
	return raw
}

// tokenRefreshValue 规则里配置的 refresh token。
func (r xbpqRule) tokenRefreshValue() string {
	return strings.TrimSpace(r.field("刷新令牌", "refreshToken", "刷新token", "令牌刷新值"))
}

func (r xbpqRule) tokenAccessPath() string {
	if path := strings.TrimSpace(r.field("令牌路径", "新令牌路径", "访问令牌路径")); path != "" {
		return path
	}
	return xbpqDefaultAccessPath
}

func (r xbpqRule) tokenRefreshPath() string {
	if path := strings.TrimSpace(r.field("刷新令牌路径", "新刷新令牌路径")); path != "" {
		return path
	}
	return xbpqDefaultRefreshPath
}

// xbpqFormatHeaderSet 将头部表还原成 `名$值#名$值` 规则写法（与解析侧对称）。
func xbpqFormatHeaderSet(headers map[string]string) string {
	if len(headers) == 0 {
		return ""
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"$"+headers[name])
	}
	return strings.Join(parts, "#")
}

// xbpqHeaderSetWithToken 把头部集合里的 Authorization 换成新令牌，其余头保持不动。
func xbpqHeaderSetWithToken(raw, access string) string {
	headers := xbpqParseHeaderSet(raw)
	if headers == nil {
		headers = map[string]string{}
	}
	headers["Authorization"] = "Bearer " + access
	return xbpqFormatHeaderSet(headers)
}

// xbpqRuleWithToken 返回替换了新令牌的规则副本（不改原规则）。
func xbpqRuleWithToken(rule xbpqRule, access, refresh string) xbpqRule {
	next := xbpqRule{fields: map[string]string{}, order: rule.order}
	for key, value := range rule.fields {
		next.fields[key] = value
	}
	next.fields[xbpqNormalizeKey("头部集合")] = xbpqHeaderSetWithToken(rule.headerSetRaw(), access)
	if refresh != "" {
		next.fields[xbpqNormalizeKey("刷新令牌")] = refresh
	}
	return next
}

// headerSetRaw 规则里「头部集合」字段的原始写法。
func (r xbpqRule) headerSetRaw() string {
	return r.field("头部集合", "头部集合")
}

// xbpqRefreshAccessToken 用 refresh token 换一套新令牌。
// 返回的新规则已带上新 Authorization；未配置或刷新失败时返回 (rule, false)。
func (d *Downloader) xbpqRefreshAccessToken(ctx context.Context, source string, rule xbpqRule, base string) (xbpqRule, bool) {
	refresh := rule.tokenRefreshValue()
	endpoint := rule.tokenRefreshEndpoint(base)
	if refresh == "" || endpoint == "" {
		return rule, false
	}

	cacheKey := endpoint + "|" + canonicalProviderSource(source)

	// 整段串行化：refresh token 会轮换，并发刷新必然有一方用旧值失败。
	xbpqTokenMu.Lock()
	defer xbpqTokenMu.Unlock()

	// 命中窗口内直接复用，不再比对传入的 refresh token：刷新会轮换它，
	// 而同一批连续播放请求手里攥着的仍是旧规则，比对必然落空、再用旧值重刷必然失败。
	if entry, found := xbpqTokenCache[cacheKey]; found && time.Since(entry.at) < xbpqTokenCacheTTL {
		return xbpqRuleWithToken(rule, entry.access, entry.refresh), true
	}

	headers := map[string]string{
		"Authorization": "Bearer " + refresh,
		"Accept":        "application/json",
	}
	// `;post` 复用现有抓取通道；body 为空，令牌走 Authorization 头。
	body, err := d.xbpqFetchWithHeaders(ctx, endpoint+";post", firstNonEmpty(base, rule.homeURL())+"/", rule.userAgent(), headers)
	if err != nil {
		return rule, false
	}

	access := strings.TrimSpace(xbpqJSONStringValue(body, rule.tokenAccessPath()))
	if access == "" || access == refresh {
		// 取不到新令牌：多半是 refresh token 已失效，或响应结构与默认路径不符。
		return rule, false
	}
	nextRefresh := strings.TrimSpace(xbpqJSONStringValue(body, rule.tokenRefreshPath()))
	if nextRefresh == "" {
		nextRefresh = refresh
	}

	xbpqTokenCache[cacheKey] = xbpqTokenEntry{access: access, refresh: nextRefresh, at: time.Now()}
	xbpqPersistRuleTokens(source, access, nextRefresh)
	return xbpqRuleWithToken(rule, access, nextRefresh), true
}

// xbpqPersistRuleTokens 把新令牌写回自定义源存储，避免下次启动又用旧值。
func xbpqPersistRuleTokens(source, access, refresh string) {
	engine := nativeEngineSnapshot()
	if engine == nil || engine.customRegistry == nil {
		return
	}
	id := canonicalProviderSource(source)
	record, found := engine.customRegistry.get(id)
	if !found || strings.TrimSpace(record.Rule) == "" {
		return
	}
	var doc map[string]any
	if json.Unmarshal([]byte(record.Rule), &doc) != nil {
		return
	}
	raw := ""
	if value, exists := doc["头部集合"]; exists {
		raw, _ = value.(string)
	}
	doc["头部集合"] = xbpqHeaderSetWithToken(raw, access)
	doc["刷新令牌"] = refresh
	body, err := json.Marshal(doc)
	if err != nil || len(body) > customSourceMaxBytes {
		return
	}
	_ = engine.customRegistry.updateRule(id, string(body))
}
