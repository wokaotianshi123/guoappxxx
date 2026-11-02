package core

import (
	"errors"
	"strings"
	"testing"
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
