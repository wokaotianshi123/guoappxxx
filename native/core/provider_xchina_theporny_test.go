package core

import "testing"

// XChina / Theporny 是内置站源，但只在全站源版本（全剧视界）里可用；
// 裁剪版必须保持「不包含此站源」，否则会绕过 editions 的裁剪。
func TestXChinaAndThepornyAreRestrictedBuiltInSources(t *testing.T) {
	previous := buildAllSources
	t.Cleanup(func() { buildAllSources = previous })

	for _, source := range []string{sourceXChina, sourceTheporny} {
		if !isHuangguoProviderSource(source) {
			t.Fatalf("%s 未登记为内置站源", source)
		}
	}

	buildAllSources = "false"
	for _, source := range []string{sourceXChina, sourceTheporny} {
		if nativeSourceAvailable(source) {
			t.Fatalf("%s 不应在裁剪版可用", source)
		}
	}

	buildAllSources = "true"
	for _, source := range []string{sourceXChina, sourceTheporny} {
		if !nativeSourceAvailable(source) {
			t.Fatalf("%s 在全站源版本应可用", source)
		}
	}
}

func TestXChinaAndThepornySourceIdentity(t *testing.T) {
	for alias, want := range map[string]string{
		"xchina":           sourceXChina,
		"xchina.tube":      sourceXChina,
		"www.xchina.tube":  sourceXChina,
		"theporny":         sourceTheporny,
		"theporny.com":     sourceTheporny,
		"www.theporny.com": sourceTheporny,
	} {
		if got := canonicalProviderSource(alias); got != want {
			t.Fatalf("别名 %s 归一化为 %s，应为 %s", alias, got, want)
		}
	}
	for _, source := range []string{sourceXChina, sourceTheporny} {
		gotSource, gotID, ok := splitProviderDramaID(providerDramaID(source, "123"))
		if !ok || gotSource != source || gotID != "123" {
			t.Fatalf("%s 剧集 ID 解析失败: source=%s id=%s ok=%v", source, gotSource, gotID, ok)
		}
	}
}

func TestXChinaAndThepornyCategoriesAndEndpoints(t *testing.T) {
	if !validXChinaCategory("recent") || validXChinaCategory("not-a-category") {
		t.Fatal("XChina 分类校验异常")
	}
	if kind, ok := validThepornyCategory("c1"); !ok || kind != "c1" {
		t.Fatalf("Theporny 分类校验异常: %s %v", kind, ok)
	}
	if _, ok := validThepornyCategory("not-a-category"); ok {
		t.Fatal("Theporny 接受了无效分类")
	}

	d := &Downloader{cfg: defaultConfig()}
	if got := d.providerBaseURL(sourceXChina); got != xchinaBaseURL {
		t.Fatalf("XChina 默认线路异常: %s", got)
	}
	if got := d.providerBaseURL(sourceTheporny); got != thepornyHosts[0] {
		t.Fatalf("Theporny 默认线路异常: %s", got)
	}
	if len(d.fetchXChinaCategories()) == 0 || len(d.fetchThepornyCategories()) == 0 {
		t.Fatal("内置分类为空")
	}
}
