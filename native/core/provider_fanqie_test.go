package core

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestFanqieDramaFromCardMapsCoreFields(t *testing.T) {
	row := map[string]any{
		"series_id_str":   "7300000000000000001",
		"series_title":    "合成剧名",
		"series_intro":    "简介文本",
		"series_cover":    "https://example.invalid/cover.jpg",
		"episode_cnt":     "80",
		"channel_name":    "漫剧",
		"category_schema": `[{"name":"都市","id":1}]`,
		"rec_text":        "1.2 亿播放",
	}
	drama := fanqieDramaFromCard(row, "短剧")
	if drama.ID != "fanqie:7300000000000000001" {
		t.Fatalf("drama id = %q", drama.ID)
	}
	if drama.Source != sourceFanqie || drama.SourceID != "7300000000000000001" {
		t.Fatalf("source/sourceID = %q/%q", drama.Source, drama.SourceID)
	}
	if drama.Title != "合成剧名" || drama.Desc != "简介文本" {
		t.Fatalf("title/desc = %q/%q", drama.Title, drama.Desc)
	}
	if drama.Category != "都市" {
		t.Fatalf("category_schema should yield 都市, got %q", drama.Category)
	}
	if drama.ChannelName != "漫剧" {
		t.Fatalf("channel = %q", drama.ChannelName)
	}
}

func TestFanqieDramaFromCardRejectsMissingSeriesID(t *testing.T) {
	if drama := fanqieDramaFromCard(map[string]any{"series_title": "无 ID"}, "短剧"); drama.ID != "" {
		t.Fatalf("card without series id must be dropped, got %q", drama.ID)
	}
	if drama := fanqieDramaFromCard(nil, "短剧"); drama.ID != "" {
		t.Fatalf("nil card must be dropped, got %q", drama.ID)
	}
}

func TestFanqieVariantHeightsDedupesAndSortsDesc(t *testing.T) {
	payload := map[string]any{"variants": []any{
		map[string]any{"width": 720.0, "height": 1280.0},  // 竖屏 → 短边 720
		map[string]any{"width": 1080.0, "height": 1920.0}, // 竖屏 → 短边 1080
		map[string]any{"width": 720.0, "height": 1280.0},  // 重复
		map[string]any{"width": 1280.0, "height": 720.0},  // 横屏 → 720
		map[string]any{"width": 0.0, "height": 0.0},       // 无效
	}}
	heights := fanqieVariantHeights(payload)
	if len(heights) != 2 {
		t.Fatalf("heights = %v, want 2 entries", heights)
	}
	if heights[0] != 1080 || heights[1] != 720 {
		t.Fatalf("heights should descend as [1080 720], got %v", heights)
	}
}

func TestFanqieCategoriesAndSourceIdentity(t *testing.T) {
	if canonicalProviderSource("fanqie") != sourceFanqie ||
		canonicalProviderSource("番茄") != sourceFanqie ||
		canonicalProviderSource("fqnovel.com") != sourceFanqie {
		t.Fatal("fanqie aliases should normalize to sourceFanqie")
	}
	if !isHuangguoProviderSource(sourceFanqie) {
		t.Fatal("fanqie must be recognized as a provider source")
	}
	// 番茄是内置源：两种构建下都必须可用，否则「红果鉴」版本里源会消失。
	if !nativeSourceAvailable(sourceFanqie) {
		t.Fatal("fanqie must stay available regardless of build edition")
	}
	if !validNativeCategory(sourceFanqie, fanqieCategoryKanju) ||
		!validNativeCategory(sourceFanqie, fanqieCategoryManju) {
		t.Fatal("kanju/manju should be valid fanqie categories")
	}
	if validNativeCategory(sourceFanqie, "unknown") {
		t.Fatal("unknown fanqie category must be rejected")
	}
	if !nativeSourceSupportsSearch(sourceFanqie) {
		t.Fatal("fanqie supports online search")
	}
	if label := duanjuSourceLabel(sourceFanqie); label != "番茄" {
		t.Fatalf("fanqie search label = %q", label)
	}
}

func TestFanqieOfflineHintGuidesUser(t *testing.T) {
	// 三个真实出现过的错误形态都要被翻译：Go 标准库、tls-client(Rust)、
	// Windows 本地化文案。少一种就会有平台看到一串机器味儿的原始报错。
	for name, cause := range map[string]error{
		"go":     errFanqieStub{},
		"rust":   errFanqieRustStub{},
		"zh_win": errFanqieChineseStub{},
	} {
		err := fanqieOfflineHint(cause)
		if err == nil || !strings.Contains(err.Error(), "番茄内容引擎未就绪") {
			t.Fatalf("%s: connection failure should carry an actionable hint, got %v", name, err)
		}
		if !strings.Contains(err.Error(), "18921") {
			t.Fatalf("%s: hint should mention the engine port, got %v", name, err)
		}
		// 原始错误必须保留：真机上只看「未就绪」永远查不出是没起来、被拒还是
		// 引擎中途挂了。
		if !strings.Contains(err.Error(), cause.Error()) {
			t.Fatalf("%s: hint should keep the original error, got %v", name, err)
		}
	}
	// 引擎活着但返回业务错误时，不能被误译成「未就绪」。
	real := errors.New("番茄引擎错误 500: upstream busy")
	if got := fanqieOfflineHint(real); got != real {
		t.Fatalf("business errors must pass through, got %v", got)
	}
	// 错误出口会做 URL 脱敏（https?://[^\s"'<>]+ 会连后面中文一起吞掉），
	// 所以提示里不能带 scheme，否则用户只能看到半截话。
	surfaced := publicError(fanqieOfflineHint(errFanqieRustStub{})).Error()
	if !strings.Contains(surfaced, "未就绪") || !strings.Contains(surfaced, "18921") {
		t.Fatalf("hint must survive URL redaction intact, got %q", surfaced)
	}
	if strings.Contains(surfaced, "http://") {
		t.Fatalf("hint should not embed a scheme-prefixed URL: %q", surfaced)
	}
}

// TestFanqieLoopbackURLIsNotRehosted 守住一个关键前提：番茄请求打的是本机
// loopback（127.0.0.1:18921），通用取文链路里的「站源域名改写」绝不能把它
// 换成某个上游域名——否则会出现「本地引擎明明活着，请求却飞到了外网」的怪象。
func TestFanqieLoopbackURLIsNotRehosted(t *testing.T) {
	raw := fanqieDefaultBaseURL + "/api/v1/videos/123?quality=720"
	if source := providerSourceForURL(raw); source != "" {
		t.Fatalf("loopback URL should not resolve to a provider source, got %q", source)
	}
	downloader := &Downloader{}
	candidates := downloader.providerURLCandidates(raw)
	if len(candidates) != 1 || candidates[0] != raw {
		t.Fatalf("loopback URL must be used as-is, got %v", candidates)
	}
}

type errFanqieStub struct{}

func (errFanqieStub) Error() string {
	return "Get \"http://127.0.0.1:18921/x\": dial tcp 127.0.0.1:18921: connect: connection refused"
}

// errFanqieRustStub 是 tls-client（Rust 后端）拒绝连接时的真实文案。
type errFanqieRustStub struct{}

func (errFanqieRustStub) Error() string {
	return "upstream connect failed: No connection could be made because the target machine actively refused it. (os error 10061)"
}

// errFanqieChineseStub 是中文 Windows 上的本地化文案。
type errFanqieChineseStub struct{}

func (errFanqieChineseStub) Error() string {
	return "upstream connect failed: 由于目标计算机积极拒绝，无法连接。 (os error 10061)"
}

// fanqieDirectText 是「统一传输在 loopback 上不可用」时的兜底通道：
// 它必须是干净直连，并且能正常把引擎的 JSON 读回来。
func TestFanqieDirectTextReadsEngineResponse(t *testing.T) {
	var gotReferer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReferer = r.Header.Get("Referer")
		if r.URL.Path != "/api/v1/search" {
			t.Errorf("请求路径 = %q, 期望 /api/v1/search", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"code":0,"data":{"tabs":{}}}`)
	}))
	defer server.Close()

	body, err := fanqieDirectText(context.Background(), server.URL+"/api/v1/search", "http://127.0.0.1:18921/")
	if err != nil {
		t.Fatalf("直连取数失败: %v", err)
	}
	if !strings.Contains(body, `"code":0`) {
		t.Fatalf("响应体 = %q", body)
	}
	if gotReferer != "http://127.0.0.1:18921/" {
		t.Fatalf("Referer = %q", gotReferer)
	}
}

// 非 2xx 要变成一句能贴进提示的错误，不能把响应体当 JSON 继续往下走。
func TestFanqieDirectTextReportsStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "capability required", http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := fanqieDirectText(context.Background(), server.URL+"/api/v1/videos/1", "")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("错误 = %v, 期望带状态码 401", err)
	}
}

// 兜底客户端绝不能走代理：手机上的系统代理 / TUN 正是会把 loopback 请求转走的那一层。
func TestFanqieLocalClientBypassesProxy(t *testing.T) {
	transport, ok := fanqieLocalClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("兜底客户端传输类型 = %T", fanqieLocalClient.Transport)
	}
	// Proxy 为 nil 就是「完全不查代理」，这正是我们想要的；非 nil 时也不能给出代理。
	request := &http.Request{URL: &url.URL{Scheme: "http", Host: "127.0.0.1:18921"}}
	if transport.Proxy != nil {
		proxy, err := transport.Proxy(request)
		if proxy != nil || err != nil {
			t.Fatalf("兜底客户端解析出代理 %v / %v, 期望直连", proxy, err)
		}
	}
	if !transport.DisableKeepAlives {
		t.Fatal("兜底客户端不应复用连接：引擎重启后旧连接会让请求直接 EOF")
	}
}

// 统一传输在 loopback 上报错时（Android 真机出现过的组合：Dart 直连 /health 200，
// Go 走统一传输却报连接层错误），必须能用干净直连把请求救回来，并记住优先直连。
func TestFanqieFetchTextFallsBackToDirect(t *testing.T) {
	fanqieDirectPreferred.Store(false)
	t.Cleanup(func() { fanqieDirectPreferred.Store(false) })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	t.Cleanup(server.Close)

	d := &Downloader{
		cfg:           Config{dataDir: t.TempDir(), Retries: 1, PageSize: 30, MaxPagesPerSort: 1, FanqieURL: server.URL},
		client:        &http.Client{Transport: fanqieBrokenTransport{}},
		limiter:       newRequestLimiter(3, 0),
		providerHosts: map[string]string{},
	}
	text, err := d.fanqieFetchText(context.Background(), server.URL+"/api/v1/search?query=x", server.URL+"/")
	if err != nil {
		t.Fatalf("兜底直连没救回来: %v", err)
	}
	if !strings.Contains(text, `"code":0`) {
		t.Fatalf("响应体 = %q", text)
	}
	if !fanqieDirectPreferred.Load() {
		t.Fatal("直连成功后应记住「优先直连」，否则每次都要先白等一轮失败")
	}
	// 记住之后，即使统一传输依旧坏着，也能直接拿到结果。
	if _, err := d.fanqieFetchText(context.Background(), server.URL+"/api/v1/search?query=y", ""); err != nil {
		t.Fatalf("优先直连失败: %v", err)
	}
}

// fanqieBrokenTransport 模拟「连接被拒/被劫持」的统一传输。
type fanqieBrokenTransport struct{}

func (fanqieBrokenTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial tcp 127.0.0.1:18921: connect: connection refused")
}

type fanqieStubTransport func(*http.Request) (*http.Response, error)

func (stub fanqieStubTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return stub(request)
}

// 引擎是宿主异步拉起的：进程刚创建时 18921 还没人听，第一个请求会拿到
// connection refused。以前的实现立刻把它翻译成「引擎未就绪」，于是出现
// 「手动 curl /health 明明是 200，应用却说引擎没起来」这种自相矛盾的反馈。
// 这里守住新行为：连不上先退避等一等，引擎接上就正常返回。
func TestFanqieFetchTextWaitsForColdEngine(t *testing.T) {
	fanqieDirectPreferred.Store(false)
	t.Cleanup(func() { fanqieDirectPreferred.Store(false) })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	t.Cleanup(server.Close)

	original := fanqieLocalClient
	t.Cleanup(func() { fanqieLocalClient = original })

	var direct int
	fanqieLocalClient = &http.Client{Transport: fanqieStubTransport(func(request *http.Request) (*http.Response, error) {
		direct++
		if direct == 1 {
			return nil, errors.New("dial tcp 127.0.0.1:18921: connect: connection refused")
		}
		return http.DefaultTransport.RoundTrip(request)
	})}

	d := &Downloader{
		cfg:           Config{dataDir: t.TempDir(), Retries: 1, PageSize: 30, MaxPagesPerSort: 1, FanqieURL: server.URL},
		client:        &http.Client{Transport: fanqieBrokenTransport{}},
		limiter:       newRequestLimiter(3, 0),
		providerHosts: map[string]string{},
	}

	started := time.Now()
	text, err := d.fanqieFetchText(context.Background(), server.URL+"/api/v1/search", "")
	if err != nil {
		t.Fatalf("冷启动期间的重试没救回来: %v", err)
	}
	if !strings.Contains(text, `"code":0`) {
		t.Fatalf("响应体 = %q", text)
	}
	if direct < 2 {
		t.Fatalf("引擎连不上时应当重试，实际直连只调用了 %d 次", direct)
	}
	if elapsed := time.Since(started); elapsed < 150*time.Millisecond {
		t.Fatalf("重试之间应当有退避，实际只用了 %s", elapsed)
	}
}

// 引擎已经在正常回业务错误时，不能被当成断线反复重试。
func TestFanqieLooksOfflineIgnoresBusinessErrors(t *testing.T) {
	for _, err := range []error{
		errors.New("番茄引擎错误 500: upstream busy"),
		errors.New("番茄引擎返回格式无效: unexpected end of JSON input"),
		nil,
	} {
		if fanqieLooksOffline(err) {
			t.Fatalf("%v 不该被判定成引擎没接上", err)
		}
	}
	for _, err := range []error{
		errors.New("dial tcp 127.0.0.1:18921: connect: connection refused"),
		errors.New("upstream connect failed: 由于目标计算机积极拒绝，无法连接。 (os error 10061)"),
		errors.New("Get \"http://127.0.0.1:18921/health\": EOF"),
	} {
		if !fanqieLooksOffline(err) {
			t.Fatalf("%v 应当被判定成引擎没接上", err)
		}
	}
}
