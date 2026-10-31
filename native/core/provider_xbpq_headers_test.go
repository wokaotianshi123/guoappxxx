package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 自定义请求头（头部集合 / 播放请求头）能力回归：
// 规则字段与 XBPQ jar、wrxbpq 写源工具字典一致，页面抓取与取直链两段都要带上。

// xbpqHeadersTestDownloader 构造一个只依赖 httptest 的 Downloader，
// 避免依赖各项目测试文件里可能缺失的共享 fixture helper。
func xbpqHeadersTestDownloader(t *testing.T, client *http.Client) *Downloader {
	t.Helper()
	return &Downloader{
		cfg:           Config{dataDir: t.TempDir(), Retries: 1, PageSize: 30, MaxPagesPerSort: 1},
		client:        client,
		limiter:       newRequestLimiter(3, 0),
		providerHosts: map[string]string{},
	}
}

func TestXBPQParseHeaderSet(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want map[string]string
	}{
		{"美元分隔", "Authorization$Bearer abc#X-Api-Key$k1", map[string]string{"Authorization": "Bearer abc", "X-Api-Key": "k1"}},
		{"冒号分隔", "Authorization: Bearer abc", map[string]string{"Authorization": "Bearer abc"}},
		{"等号分隔", "Referer=https://a.test/", map[string]string{"Referer": "https://a.test/"}},
		{"换行分隔", "Cookie$sid=1; theme=dark\nX-From$rule", map[string]string{"Cookie": "sid=1; theme=dark", "X-From": "rule"}},
		{"纯 UA 忽略", "Mozilla/5.0 (Windows NT 10.0)", nil},
		{"空值忽略", "Authorization$#X-A$", nil},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			got := xbpqParseHeaderSet(item.raw)
			if len(got) != len(item.want) {
				t.Fatalf("头数量不符：got=%v want=%v", got, item.want)
			}
			for name, value := range item.want {
				if got[name] != value {
					t.Fatalf("%s 取值不符：got=%q want=%q", name, got[name], value)
				}
			}
		})
	}
}

func TestXBPQHeaderSetMergesPlayOverride(t *testing.T) {
	rule, ok := parseXBPQRule(`{"主页url":"https://a.test","分类url":"https://a.test/list/{cateId}-{catePg}.html","分类":"电影$1",
		"头部集合":"Authorization$Bearer site#Referer$https://a.test/","播放请求头":"Referer$https://play.a.test/"}`)
	if !ok {
		t.Fatal("规则解析失败")
	}
	site := rule.headerSet()
	if site["Authorization"] != "Bearer site" || site["Referer"] != "https://a.test/" {
		t.Fatalf("头部集合解析错误: %v", site)
	}
	play := rule.playHeaderSet()
	if play["Authorization"] != "Bearer site" {
		t.Fatalf("播放请求头应继承头部集合: %v", play)
	}
	if play["Referer"] != "https://play.a.test/" {
		t.Fatalf("播放请求头的 Referer 应覆盖头部集合: %v", play)
	}
	if got := xbpqPlayReferer(rule, "https://fallback.test/"); got != "https://play.a.test/" {
		t.Fatalf("xbpqPlayReferer 未取规则值: %q", got)
	}
}

func TestXBPQHeaderSetReachesRequest(t *testing.T) {
	var gotAuth, gotCustom, gotUA string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotAuth = request.Header.Get("Authorization")
		gotCustom = request.Header.Get("X-Api-Key")
		gotUA = request.Header.Get("User-Agent")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":{"records":[]}}`))
	}))
	defer server.Close()

	d := xbpqHeadersTestDownloader(t, server.Client())

	rule, ok := parseXBPQRule(`{"主页url":"` + server.URL + `","分类url":"` + server.URL + `/list/{cateId}-{catePg}.html","分类":"电影$1",
		"请求头":"User-Agent$RuleUA/1.0","头部集合":"Authorization$Bearer tok#X-Api-Key$k9"}`)
	if !ok {
		t.Fatal("规则解析失败")
	}
	if _, err := d.xbpqFetchWithHeaders(context.Background(), server.URL+"/api", server.URL+"/", rule.userAgent(), rule.headerSet()); err != nil {
		t.Fatalf("抓取失败: %v", err)
	}
	if gotAuth != "Bearer tok" || gotCustom != "k9" {
		t.Fatalf("自定义头未送达：Authorization=%q X-Api-Key=%q", gotAuth, gotCustom)
	}
	if gotUA != "RuleUA/1.0" {
		t.Fatalf("UA 被覆盖异常：%q", gotUA)
	}
}

func TestXBPQResolveMediaUsesPlayReferer(t *testing.T) {
	var sawAuth string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/play":
			sawAuth = request.Header.Get("Authorization")
			writer.Header().Set("Content-Type", "application/json")
			// 用请求里的 Host 拼直链，保证指向同一测试服务器
			_, _ = writer.Write([]byte(`{"data":{"playUrl":"` + "http://" + request.Host + `/index.m3u8"}}`))
		default:
			writer.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = writer.Write([]byte("#EXTM3U\n#EXT-X-VERSION:3\n"))
		}
	}))
	defer server.Close()

	d := xbpqHeadersTestDownloader(t, server.Client())

	rule, ok := parseXBPQRule(`{"主页url":"` + server.URL + `","分类url":"` + server.URL + `/list/{cateId}-{catePg}.html","分类":"电影$1",
		"头部集合":"Authorization$Bearer playtok","播放请求头":"Referer$https://www.example-site.test/","跳转播放链接":"j:data.playUrl"}`)
	if !ok {
		t.Fatal("规则解析失败")
	}
	media, err := d.xbpqResolveOne(context.Background(), server.URL+"/play", server.URL+"/", rule, "测试")
	if err != nil {
		t.Fatalf("取直链失败: %v", err)
	}
	if sawAuth != "Bearer playtok" {
		t.Fatalf("取直链请求没带 Authorization: %q", sawAuth)
	}
	if media.Referer != "https://www.example-site.test/" {
		t.Fatalf("media.Referer 应取 播放请求头 的 Referer: %q", media.Referer)
	}
}
