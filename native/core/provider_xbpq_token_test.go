package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestXBPQTokenRefreshEndpoint(t *testing.T) {
	// 绝对地址原样返回
	rule, _ := parseXBPQRule(`{"主页url":"https://api.test/sorani-cms","刷新地址":"https://api.test/api/auth/refresh","刷新令牌":"r1"}`)
	if got := rule.tokenRefreshEndpoint("https://api.test/sorani-cms"); got != "https://api.test/api/auth/refresh" {
		t.Fatalf("绝对地址: %q", got)
	}

	// 相对路径拼到 base 的 origin
	rel, _ := parseXBPQRule(`{"主页url":"https://api.test/sorani-cms","刷新地址":"/api/auth/refresh","刷新令牌":"r1"}`)
	if got := rel.tokenRefreshEndpoint("https://api.test/sorani-cms"); got != "https://api.test/api/auth/refresh" {
		t.Fatalf("相对路径: %q", got)
	}

	// 未配置 -> 空。未配置时不得触发任何刷新
	plain, _ := parseXBPQRule(`{"主页url":"https://api.test"}`)
	if got := plain.tokenRefreshEndpoint("https://api.test"); got != "" {
		t.Fatalf("未配置应返回空，得到 %q", got)
	}
	if plain.tokenRefreshValue() != "" {
		t.Fatalf("未配置刷新令牌应返回空")
	}
}

func TestXBPQHeaderSetWithToken(t *testing.T) {
	got := xbpqHeaderSetWithToken("Authorization$Bearer OLD#X-Api-Key$k1", "NEW")
	headers := xbpqParseHeaderSet(got)
	if headers["Authorization"] != "Bearer NEW" {
		t.Fatalf("Authorization 未替换: %q", headers["Authorization"])
	}
	if headers["X-Api-Key"] != "k1" {
		t.Fatalf("其它头被丢弃: %q", got)
	}
	// 原来没有 Authorization 时也要补上
	if h := xbpqParseHeaderSet(xbpqHeaderSetWithToken("X-Api-Key$k1", "NEW")); h["Authorization"] != "Bearer NEW" {
		t.Fatalf("缺省 Authorization 未补上: %q", h)
	}
	// 解析-格式化往返稳定
	if again := xbpqHeaderSetWithToken(xbpqHeaderSetWithToken("A$1#B$2", "T"), "T"); xbpqParseHeaderSet(again)["B"] != "2" {
		t.Fatalf("往返不稳定: %q", again)
	}
}

func TestXBPQRuleWithTokenKeepsOtherFields(t *testing.T) {
	rule, ok := parseXBPQRule(`{"主页url":"https://api.test","头部集合":"Authorization$Bearer OLD#X-A$1","刷新令牌":"R1","分类":"电影$1"}`)
	if !ok {
		t.Fatalf("解析失败")
	}
	next := xbpqRuleWithToken(rule, "NEW", "R2")
	if headers := next.playHeaderSet(); headers["Authorization"] != "Bearer NEW" {
		t.Fatalf("新规则 Authorization: %q", headers["Authorization"])
	}
	if next.tokenRefreshValue() != "R2" {
		t.Fatalf("刷新令牌未轮换: %q", next.tokenRefreshValue())
	}
	if next.field("分类") != "电影$1" {
		t.Fatalf("无关字段被破坏: %q", next.field("分类"))
	}
	// 原规则不受影响
	if rule.tokenRefreshValue() != "R1" {
		t.Fatalf("原规则被就地修改: %q", rule.tokenRefreshValue())
	}
}

func TestXBPQRefreshAccessTokenSuccess(t *testing.T) {
	var mu sync.Mutex
	var gotAuth string
	var hits int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		hits++
		mu.Unlock()
		if r.Method != http.MethodPost {
			t.Errorf("刷新应使用 POST，得到 %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"success":true,"data":{"token":"NEW_ACCESS","refreshToken":"NEW_REFRESH","expireTime":"2026-11-03T00:00:00"}}`))
	}))
	defer srv.Close()

	d := xbpqHeadersTestDownloader(t, srv.Client())
	rule, ok := parseXBPQRule(`{"主页url":"` + srv.URL + `","头部集合":"Authorization$Bearer OLD#X-A$1","刷新地址":"/api/auth/refresh","刷新令牌":"R1"}`)
	if !ok {
		t.Fatalf("解析失败")
	}

	next, refreshed := d.xbpqRefreshAccessToken(context.Background(), "custom:test", rule, srv.URL)
	if !refreshed {
		t.Fatalf("刷新应当成功")
	}
	mu.Lock()
	defer mu.Unlock()
	if gotAuth != "Bearer R1" {
		t.Fatalf("刷新请求应带 refresh token，得到 %q", gotAuth)
	}
	if hits != 1 {
		t.Fatalf("刷新请求次数 %d", hits)
	}
	if h := next.playHeaderSet(); h["Authorization"] != "Bearer NEW_ACCESS" {
		t.Fatalf("新令牌未生效: %q", h["Authorization"])
	}
	if next.tokenRefreshValue() != "NEW_REFRESH" {
		t.Fatalf("refresh token 未轮换: %q", next.tokenRefreshValue())
	}
}

func TestXBPQRefreshAccessTokenCacheAvoidsDoubleRefresh(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		_, _ = w.Write([]byte(`{"data":{"token":"NEW_ACCESS","refreshToken":"NEW_REFRESH"}}`))
	}))
	defer srv.Close()

	d := xbpqHeadersTestDownloader(t, srv.Client())
	rule, _ := parseXBPQRule(`{"主页url":"` + srv.URL + `","头部集合":"Authorization$Bearer OLD","刷新地址":"` + srv.URL + `/refresh","刷新令牌":"R1"}`)

	if _, ok := d.xbpqRefreshAccessToken(context.Background(), "custom:cached", rule, srv.URL); !ok {
		t.Fatalf("首次刷新应成功")
	}
	// 第二次仍拿着旧规则（refresh token 已被轮换）：应命中缓存，不再打接口
	again, ok := d.xbpqRefreshAccessToken(context.Background(), "custom:cached", rule, srv.URL)
	if !ok {
		t.Fatalf("缓存命中应返回成功")
	}
	if h := again.playHeaderSet(); h["Authorization"] != "Bearer NEW_ACCESS" {
		t.Fatalf("缓存应给出轮换后的令牌，得到 %q", h["Authorization"])
	}
	if again.tokenRefreshValue() != "NEW_REFRESH" {
		t.Fatalf("缓存应给出轮换后的 refresh token，得到 %q", again.tokenRefreshValue())
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Fatalf("应只请求 1 次，实际 %d", hits)
	}
}

func TestXBPQRefreshAccessTokenRejectsBadResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":401,"message":"令牌无效或已过期","success":false}`))
	}))
	defer srv.Close()

	d := xbpqHeadersTestDownloader(t, srv.Client())
	rule, _ := parseXBPQRule(`{"主页url":"` + srv.URL + `","头部集合":"Authorization$Bearer OLD","刷新地址":"` + srv.URL + `/refresh","刷新令牌":"DEAD"}`)

	if _, ok := d.xbpqRefreshAccessToken(context.Background(), "custom:bad", rule, srv.URL); ok {
		t.Fatalf("响应里没有新令牌时不应报告成功")
	}
}

// 全链路：第一次播放请求被旧令牌拒绝，刷新后重试带上新令牌。
func TestXBPQResolveMediaRetriesWithRefreshedToken(t *testing.T) {
	var mu sync.Mutex
	var playAuths []string

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/refresh", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"token":"NEW_ACCESS","refreshToken":"NEW_REFRESH"}}`))
	})
	mux.HandleFunc("/play", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		playAuths = append(playAuths, auth)
		mu.Unlock()
		if auth != "Bearer NEW_ACCESS" {
			// 旧令牌：站点返回"请先登录"，没有播放地址
			_, _ = w.Write([]byte(`{"code":200,"data":{"canPlay":false,"message":"请先登录","playUrl":null}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"data":{"canPlay":true,"playUrl":"` + srv.URL + `/video/index.mp4"}}`))
	})

	d := xbpqHeadersTestDownloader(t, srv.Client())
	rule, ok := parseXBPQRule(`{
		"主页url":"` + srv.URL + `",
		"头部集合":"Authorization$Bearer OLD",
		"刷新地址":"` + srv.URL + `/refresh",
		"刷新令牌":"R1",
		"跳转播放链接":"j:data.playUrl"
	}`)
	if !ok {
		t.Fatalf("解析失败")
	}

	task := Task{Chapter: Chapter{
		Source:   "custom:retry",
		PageURL:  srv.URL + "/play",
		VideoURL: srv.URL + "/play",
	}}
	// 取直链是否最终成功取决于媒体探测，这里只断言"带了新令牌重试过"
	_, _ = d.xbpqResolveMedia(context.Background(), task, rule, srv.URL, "测试源")

	mu.Lock()
	defer mu.Unlock()
	if len(playAuths) < 2 {
		t.Fatalf("应重试一次播放请求，实际 %d 次: %v", len(playAuths), playAuths)
	}
	if playAuths[0] != "Bearer OLD" {
		t.Fatalf("首次应使用旧令牌，得到 %q", playAuths[0])
	}
	if playAuths[len(playAuths)-1] != "Bearer NEW_ACCESS" {
		t.Fatalf("重试应使用新令牌，得到 %q", playAuths[len(playAuths)-1])
	}
}

func TestXBPQResolveMediaNoRefreshWhenNotConfigured(t *testing.T) {
	var mu sync.Mutex
	plays := 0
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/play", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		plays++
		mu.Unlock()
		_, _ = w.Write([]byte(`{"data":{}}`))
	})

	d := xbpqHeadersTestDownloader(t, srv.Client())
	rule, _ := parseXBPQRule(`{"主页url":"` + srv.URL + `","跳转播放链接":"j:data.playUrl"}`)
	task := Task{Chapter: Chapter{Source: "custom:plain", PageURL: srv.URL + "/play", VideoURL: srv.URL + "/play"}}

	if _, err := d.xbpqResolveMedia(context.Background(), task, rule, srv.URL, "无令牌源"); err == nil {
		t.Fatalf("没有播放地址时应报错")
	}
	mu.Lock()
	defer mu.Unlock()
	if plays != 1 {
		t.Fatalf("未配置刷新令牌时不应重试，实际请求 %d 次", plays)
	}
}
