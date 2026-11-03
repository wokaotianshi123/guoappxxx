package core

// 番茄站源（sourceFanqie）：内容来自番茄短剧（fqnovel / novelfm 上游）。
//
// 上游签名、设备池与视频缓存都由本机内嵌的番茄内容引擎（libfanqie_shim，
// 127.0.0.1:18921 的 loopback REST 服务）完成；这里只做 HTTP 转发与字段映射：
//
//	目录   GET /api/v1/recommend/homepage?tab_type=8|24        → cell_id + session
//	       GET /api/v1/recommend/series-feed?cell_id=..        → 视频卡片流
//	搜索   GET /api/v1/search?query=..&tab_type=11             → search_tabs[11]
//	详情   GET /api/v1/series/{series_id}                      → 全集 video_list
//	播放   GET /api/v1/videos/{vid}                            → /src/{vid}.mp4
//
// 播放地址是本机引擎的相对路径（/src/...），带 Range 支持，直接交给播放器。
//
// 引擎可用性：Android 内置 libfanqie_shim，由 Dart 侧 FanqieBackend 拉起；
// 桌面端 / 本地服务器端没有该引擎时，请求会得到明确提示（见 fanqieOfflineHint），
// 不会静默失败成「站源暂未返回剧集」。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const fanqieDefaultBaseURL = "http://127.0.0.1:18921"

// fanqieEngineAddress 是不带 scheme 的引擎地址，专供用户可见的提示文案使用：
// 错误信息出口会做 URL 脱敏（rePublicURL 匹配 `https?://[^\s"'<>]+`），
// 带 scheme 的地址会把紧跟其后的中文一起吞掉，用户只会看到半截话。
const fanqieEngineAddress = "127.0.0.1:18921"

// 番茄频道对应的官方 tab_type。
const (
	fanqieTabTypeKanju = 8  // 看剧（真人短剧）
	fanqieTabTypeManju = 24 // 漫剧
)

// 暴露给发现页的 category ID。
const (
	fanqieCategoryKanju = "kanju"
	fanqieCategoryManju = "manju"
)

var fanqieCategoryTabTypes = map[string]int{
	fanqieCategoryKanju: fanqieTabTypeKanju,
	fanqieCategoryManju: fanqieTabTypeManju,
}

// 每个频道的翻页游标：首页拿到 cell 后记住 cell_id / session / next_offset，
// 翻页直接打第二段 series-feed。
type fanqieFeedState struct {
	cellID     string
	sessionID  string
	nextOffset int
}

func (d *Downloader) fanqieBase() string {
	return strings.TrimRight(firstNonEmpty(d.cfg.FanqieURL, fanqieDefaultBaseURL), "/")
}

// fanqieOfflineMarkers 是「连不上本机引擎」在各传输栈上的报错特征。
// 关键字按「可能的报错文案」覆盖三层，避免换个平台/换个传输实现就漏判：
//   - Go 标准库：connect: connection refused / dial tcp ... / no such host / EOF
//   - tls-client（Rust 后端）：upstream connect failed: ... (os error 10061)
//   - Windows 本地化文案：由于目标计算机积极拒绝，无法连接。
//
// 重试判定与用户提示必须共用这一份：分头维护迟早会出现「该重试的没重试」
// 或者「把引擎的业务错误当断线重试」。
var fanqieOfflineMarkers = []string{
	"connection refused",
	"connect:",
	"connectex",
	"dial tcp",
	"upstream connect failed",
	"os error 10061",
	"eof",
	"no such host",
	"无法连接",
	"积极拒绝",
}

// fanqieLooksOffline 判断错误是不是「引擎这一侧压根没接上」。
// 引擎正常回了业务错误（比如 401/500）时返回 false——那种重试没有意义。
func fanqieLooksOffline(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	for _, marker := range fanqieOfflineMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// fanqieOfflineHint 把「连不上本机引擎」翻译成可操作的提示。
// 桌面端 / 本地服务器端没有内嵌引擎，直接连 127.0.0.1:18921 会得到连接失败，
// 原始错误对用户没有任何指引意义。
func fanqieOfflineHint(err error) error {
	if !fanqieLooksOffline(err) {
		return err
	}
	// 把原始错误一起带上：光说「未就绪」在真机上没法判断到底是引擎没起来、
	// 端口被占、还是请求被服务端拒了。URL 出口会做脱敏，这里用无 scheme 的地址。
	return fmt.Errorf("番茄内容引擎未就绪（本机 %s，已等待 %s 仍连不上）：%v",
		fanqieEngineAddress, fanqieEngineWarmupWindow, err)
}

func (d *Downloader) fanqieFeedState(tabType int) *fanqieFeedState {
	d.fanqieMu.Lock()
	defer d.fanqieMu.Unlock()
	if d.fanqieFeeds == nil {
		d.fanqieFeeds = map[int]*fanqieFeedState{}
	}
	state := d.fanqieFeeds[tabType]
	if state == nil {
		state = &fanqieFeedState{}
		d.fanqieFeeds[tabType] = state
	}
	return state
}

// fanqieLocalClient 是只跟本机引擎对话的最小 HTTP 客户端。
//
// 为什么不复用 d.client：那条链路为外网站源叠了浏览器指纹传输、CDN 择优、系统代理
// 和长连接池。Android 真机实测过一种情况——同一个 APP 进程里 Dart 直接 GET /health
// 拿到 200，而 Go 侧走统一传输的请求却报连接层错误——说明问题在传输栈，不在引擎。
// loopback 请求只需要一条干净的直连：不代理、不指纹、不复用连接。
var fanqieLocalClient = &http.Client{
	Transport: &http.Transport{
		Proxy:             nil,
		DialContext:       (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: -1}).DialContext,
		DisableKeepAlives: true,
	},
	// 比 providerTimeout 宽松：详情/取流要等引擎向上游签发，冷启动时更慢。
	Timeout: 30 * time.Second,
}

// fanqieDirectPreferred 记住「统一传输在 loopback 上不可靠」这个事实：一旦直连
// 救过一次，后续请求就直接走直连，不再先白等一轮退避重试。
var fanqieDirectPreferred atomic.Bool

// fanqieEngineWarmupWindow 是「引擎正在起」的等待上限。
//
// 桌面版和本地服务版都是由宿主先把引擎拉成独立进程，再从 loopback 发请求；
// 从进程创建到 /health 能应答之间有一段空窗（读过设备池、载入缓存后更久）。
// 以前这个窗口里的第一个请求直接拿到 connection refused，被翻译成
// 「引擎未就绪」——用户随后手动 curl 却是 200，于是最典型的反馈就是
// 「引擎明明活着，应用却说未就绪」。
//
// 这里在放弃之前先等这个窗口：引擎确实没装/被杀了，最多多等这么久；
// 引擎只是在起，则第一个请求就能正常返回。
const fanqieEngineWarmupWindow = 12 * time.Second

// fanqieFetchText 取一个本机引擎路由的响应体。
//
// 冷启动窗口内「连不上」会退避重试（200ms 起、指数增长，总时长受
// fanqieEngineWarmupWindow 约束）；引擎回了业务错误则立即上抛。
func (d *Downloader) fanqieFetchText(ctx context.Context, rawURL, referer string) (string, error) {
	deadline := time.Now().Add(fanqieEngineWarmupWindow)
	delay := 200 * time.Millisecond
	for {
		text, err := d.fanqieFetchTextOnce(ctx, rawURL, referer)
		if err == nil {
			return text, nil
		}
		// 只有「根本没接上」才值得等：业务错误重试只会把问题放大。
		if !fanqieLooksOffline(err) || ctx.Err() != nil || time.Now().Add(delay).After(deadline) {
			return "", err
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return "", err
		}
		delay *= 2
	}
}

// fanqieFetchTextOnce 是单次尝试：先走应用统一传输（保留限速与诊断），
// 失败后用一个干净的直连客户端重试一次。两次都失败则把两条错误一起抛出来——
// 「未就绪」这种提示只有带上两侧的原始错误，才能在真机上分清是引擎没起、
// 传输栈被劫持，还是引擎 handler 自己挂了。
func (d *Downloader) fanqieFetchTextOnce(ctx context.Context, rawURL, referer string) (string, error) {
	if fanqieDirectPreferred.Load() {
		if text, err := fanqieDirectText(ctx, rawURL, referer); err == nil {
			return text, nil
		}
		// 直连这条路也可能断（引擎重启），退回去再试一次，别把用户卡死。
		return d.fetchProviderText(ctx, rawURL, referer)
	}
	text, err := d.fetchProviderText(ctx, rawURL, referer)
	if err == nil {
		return text, nil
	}
	direct, directErr := fanqieDirectText(ctx, rawURL, referer)
	if directErr == nil {
		fanqieDirectPreferred.Store(true)
		return direct, nil
	}
	return "", fmt.Errorf("%v；直连重试：%v", err, directErr)
}

// fanqieDirectText 用 fanqieLocalClient 打一次本机引擎路由。
func fanqieDirectText(ctx context.Context, rawURL, referer string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", userAgent)
	if referer != "" {
		request.Header.Set("Referer", referer)
	}
	request.Header.Set("Accept", "application/json")
	response, err := fanqieLocalClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return "", fmt.Errorf("引擎返回 HTTP %d：%s", response.StatusCode, snippet)
	}
	return string(body), nil
}

// fanqieGetJSON GET 一个本机引擎路由并解出顶层 JSON。
func (d *Downloader) fanqieGetJSON(ctx context.Context, rawURL string) (map[string]any, error) {
	text, err := d.fanqieFetchText(ctx, rawURL, d.fanqieBase()+"/")
	if err != nil {
		return nil, fanqieOfflineHint(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		return nil, fmt.Errorf("番茄引擎返回格式无效: %w", err)
	}
	if code, _ := decoded["code"].(float64); decoded["code"] != nil && code != 0 {
		message, _ := decoded["message"].(string)
		return nil, fmt.Errorf("番茄引擎错误 %d: %s", int(code), message)
	}
	return decoded, nil
}

func fanqieString(row map[string]any, keys ...string) string {
	return strings.TrimSpace(mapString(row, keys...))
}

// fanqieCategorySchemaName 从 category_schema（JSON 字符串数组）取第一个分类名。
func fanqieCategorySchemaName(row map[string]any) string {
	raw, _ := row["category_schema"].(string)
	if raw == "" {
		return ""
	}
	var entries []map[string]any
	if json.Unmarshal([]byte(raw), &entries) != nil {
		return ""
	}
	for _, entry := range entries {
		if name := fanqieString(entry, "name"); name != "" {
			return name
		}
	}
	return ""
}

// fanqieDramaFromCard 把 feed / 搜索卡片（video_data 节点）映射成 Drama。
func fanqieDramaFromCard(row map[string]any, fallbackChannel string) Drama {
	seriesID := fanqieString(row, "series_id_str", "pseries_id", "series_id", "book_id")
	if seriesID == "" {
		return Drama{}
	}
	channel := fanqieString(row, "channel_name")
	if channel == "" {
		channel = fallbackChannel
	}
	category := fanqieCategorySchemaName(row)
	return Drama{
		ID:           providerDramaID(sourceFanqie, seriesID),
		Source:       sourceFanqie,
		SourceID:     seriesID,
		Title:        fanqieString(row, "series_title", "raw_book_name", "title", "book_name"),
		Name:         fanqieString(row, "series_title", "raw_book_name", "title", "book_name"),
		Desc:         fanqieString(row, "series_intro", "abstract", "description"),
		Intro:        fanqieString(row, "series_intro", "abstract"),
		Cover:        fanqieString(row, "series_cover", "cover", "thumb_url"),
		TotalEpisode: fanqieString(row, "episode_cnt"),
		Category:     category,
		ChannelName:  channel,
		Heat:         fanqieString(row, "rec_text", "play_cnt", "series_play_cnt"),
	}
}

// fanqieHomeCell 打第一段首页，拿频道的 cell_id 与 session。
func (d *Downloader) fanqieHomeCell(ctx context.Context, tabType int) (*fanqieFeedState, error) {
	home, err := d.fanqieGetJSON(ctx, fmt.Sprintf("%s/api/v1/recommend/homepage?tab_type=%d&offset=0", d.fanqieBase(), tabType))
	if err != nil {
		return nil, err
	}
	data, _ := home["data"].(map[string]any)
	if data == nil {
		return nil, errors.New("番茄首页数据为空")
	}
	tabs, _ := data["tab_item"].([]any)
	for _, raw := range tabs {
		tab, _ := raw.(map[string]any)
		if tab == nil {
			continue
		}
		if int(mapFloat(tab, "tab_type")) != tabType {
			continue
		}
		cells, _ := tab["cell_data"].([]any)
		if len(cells) == 0 {
			return nil, errors.New("番茄首页频道尚未就绪，请稍后刷新")
		}
		cell, _ := cells[0].(map[string]any)
		if cell == nil {
			continue
		}
		cellID := fanqieString(cell, "cell_id_str", "cell_id")
		if cellID == "" {
			continue
		}
		session, _ := tab["session_id"].(string)
		state := d.fanqieFeedState(tabType)
		d.fanqieMu.Lock()
		state.cellID = cellID
		state.sessionID = session
		state.nextOffset = 0
		d.fanqieMu.Unlock()
		return state, nil
	}
	return nil, errors.New("番茄首页未包含目标频道")
}

// fanqieFeedPage 拉一页频道视频流。
func (d *Downloader) fanqieFeedPage(ctx context.Context, category string, page int) ([]Drama, bool, error) {
	tabType, ok := fanqieCategoryTabTypes[category]
	if !ok {
		tabType = fanqieTabTypeKanju
	}
	channel := "短剧"
	if tabType == fanqieTabTypeManju {
		channel = "漫剧"
	}
	state := d.fanqieFeedState(tabType)
	if strings.TrimSpace(state.cellID) == "" {
		if _, err := d.fanqieHomeCell(ctx, tabType); err != nil {
			return nil, false, err
		}
	}
	d.fanqieMu.Lock()
	cellID, sessionID, offset := state.cellID, state.sessionID, state.nextOffset
	d.fanqieMu.Unlock()
	if page > 1 && offset <= 0 {
		return nil, false, nil
	}
	changeType := "1" // 翻页 GetMore
	if page <= 1 {
		changeType = "2" // 首屏 ChangeFilter
		offset = 0
	}
	query := url.Values{}
	query.Set("cell_id", cellID)
	query.Set("tab_type", strconv.Itoa(tabType))
	query.Set("client_template", "2")
	query.Set("client_req_type", "2")
	query.Set("offset", strconv.Itoa(offset))
	query.Set("unlimited_selector_change_type", changeType)
	if sessionID != "" {
		query.Set("session_id", sessionID)
	}
	feed, err := d.fanqieGetJSON(ctx, d.fanqieBase()+"/api/v1/recommend/series-feed?"+query.Encode())
	if err != nil {
		// 会话/游标失效：重开一次首页再试一页。
		d.fanqieMu.Lock()
		d.fanqieFeeds[tabType] = &fanqieFeedState{}
		d.fanqieMu.Unlock()
		if page <= 1 {
			if _, retryErr := d.fanqieHomeCell(ctx, tabType); retryErr != nil {
				return nil, false, err
			}
			feed, err = d.fanqieGetJSON(ctx, d.fanqieBase()+"/api/v1/recommend/series-feed?"+query.Encode())
			if err != nil {
				return nil, false, err
			}
		} else {
			return nil, false, err
		}
	}
	data, _ := feed["data"].(map[string]any)
	if data == nil {
		return nil, false, errors.New("番茄视频流数据为空")
	}
	items := fanqieDramasFromCellView(data, channel)
	hasMore := mapBool(data, "has_more")
	if next, ok := data["next_offset"].(float64); ok {
		state = d.fanqieFeedState(tabType)
		d.fanqieMu.Lock()
		state.nextOffset = int(next)
		if session, _ := data["session_id"].(string); session != "" {
			state.sessionID = session
		}
		d.fanqieMu.Unlock()
	}
	return items, hasMore && len(items) > 0, nil
}

func fanqieDramasFromCellView(data map[string]any, channel string) []Drama {
	var out []Drama
	seen := map[string]bool{}
	cellView, _ := data["cell_view"].(map[string]any)
	cells, _ := cellView["cell_data"].([]any)
	for _, rawCell := range cells {
		cell, _ := rawCell.(map[string]any)
		if cell == nil {
			continue
		}
		videos, _ := cell["video_data"].([]any)
		for _, rawVideo := range videos {
			video, _ := rawVideo.(map[string]any)
			if video == nil {
				continue
			}
			drama := fanqieDramaFromCard(video, channel)
			if drama.ID == "" || seen[drama.ID] {
				continue
			}
			seen[drama.ID] = true
			out = append(out, drama)
		}
	}
	return out
}

// fetchFanqieCatalogPage 目录分页入口（app_runtime 的 catalog 分派）。
func (d *Downloader) fetchFanqieCatalogPage(ctx context.Context, category string, page int) ([]Drama, bool, error) {
	return d.fanqieFeedPage(ctx, category, page)
}

// fetchFanqieSearchPage 短剧搜索（tab_type=11）。
func (d *Downloader) fetchFanqieSearchPage(ctx context.Context, page int, query string) ([]Drama, bool, error) {
	offset := (page - 1) * 10
	if offset < 0 {
		offset = 0
	}
	q := url.Values{}
	q.Set("query", query)
	q.Set("tab_type", "11")
	q.Set("offset", strconv.Itoa(offset))
	q.Set("count", "10")
	payload, err := d.fanqieGetJSON(ctx, d.fanqieBase()+"/api/v1/search?"+q.Encode())
	if err != nil {
		return nil, false, err
	}
	tabs, _ := payload["search_tabs"].([]any)
	var items []Drama
	seen := map[string]bool{}
	for _, rawTab := range tabs {
		tab, _ := rawTab.(map[string]any)
		if tab == nil || int(mapFloat(tab, "tab_type")) != 11 {
			continue
		}
		rows, _ := tab["data"].([]any)
		for _, rawRow := range rows {
			row, _ := rawRow.(map[string]any)
			if row == nil {
				continue
			}
			videos, _ := row["video_data"].([]any)
			if len(videos) == 0 {
				continue
			}
			video, _ := videos[0].(map[string]any)
			if video == nil {
				continue
			}
			drama := fanqieDramaFromCard(video, "短剧")
			if drama.ID == "" || seen[drama.ID] {
				continue
			}
			seen[drama.ID] = true
			items = append(items, drama)
		}
		hasMore, _ := tab["has_more"].(bool)
		return items, hasMore && len(items) > 0, nil
	}
	return items, false, nil
}

// fetchFanqieDetail 详情 + 全集（上游一次返回完整 video_list）。
func (d *Downloader) fetchFanqieDetail(ctx context.Context, sourceID string) (Drama, []Chapter, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return Drama{}, nil, errors.New("番茄剧集 ID 无效")
	}
	payload, err := d.fanqieGetJSON(ctx, d.fanqieBase()+"/api/v1/series/"+url.PathEscape(sourceID))
	if err != nil {
		return Drama{}, nil, err
	}
	data, _ := payload["data"].(map[string]any)
	row, _ := data["video_data"].(map[string]any)
	if row == nil {
		return Drama{}, nil, errors.New("番茄详情数据为空")
	}
	got := fanqieDramaFromCard(row, "短剧")
	if fanqieString(row, "series_id_str", "series_id") != sourceID {
		return Drama{}, nil, errors.New("番茄详情与请求剧集不符")
	}
	videos, _ := row["video_list"].([]any)
	chapters := make([]Chapter, 0, len(videos))
	for _, raw := range videos {
		video, _ := raw.(map[string]any)
		if video == nil {
			continue
		}
		vid := fanqieString(video, "vid")
		if vid == "" {
			continue
		}
		index := int(mapFloat(video, "vid_index"))
		title := fanqieString(video, "title")
		if index > 0 {
			title = fmt.Sprintf("第 %d 集：%s", index, title)
		}
		chapters = append(chapters, Chapter{
			ID:       vid,
			Source:   sourceFanqie,
			Title:    title,
			Vertical: boolPtr(true),
		})
	}
	return got, chapters, nil
}

// resolveFanqieMedia 把 vid 换成本机引擎的流媒体地址。
func (d *Downloader) resolveFanqieMedia(ctx context.Context, chapter Chapter, quality int) (providerMedia, error) {
	vid := strings.TrimSpace(chapter.ID)
	if vid == "" {
		return providerMedia{}, errors.New("番茄分集 ID 无效")
	}
	endpoint := d.fanqieBase() + "/api/v1/videos/" + url.PathEscape(vid)
	if quality > 0 {
		// 指定档位：引擎会把该档下载到独立缓存文件（{vid}_{height}.mp4），
		// 切清晰度只是重新解析到另一份缓存，不会互相覆盖。
		endpoint += "?quality=" + strconv.Itoa(quality)
	}
	payload, err := d.fanqieGetJSON(ctx, endpoint)
	if err != nil {
		return providerMedia{}, err
	}
	videoURL := fanqieString(payload, "video_url")
	if videoURL == "" {
		if info, _ := payload["video_info"].(map[string]any); info != nil {
			if inner, _ := info["data"].(map[string]any); inner != nil {
				if list, _ := inner["video_list"].(map[string]any); list != nil {
					if first, _ := list["video_1"].(map[string]any); first != nil {
						videoURL = fanqieString(first, "main_url")
					}
				}
			}
		}
	}
	if videoURL == "" {
		return providerMedia{}, errors.New("番茄未返回可播放的地址")
	}
	if !strings.HasPrefix(videoURL, "http://") && !strings.HasPrefix(videoURL, "https://") {
		videoURL = d.fanqieBase() + "/" + strings.TrimLeft(videoURL, "/")
	}
	media := providerMedia{URL: videoURL, Referer: d.fanqieBase() + "/"}
	// 引擎按「高→低」下发全量档位；单档（甚至空）意味着源本身没有清晰度可选，
	// 此时保持 Quality 为 0，客户端只显示「自动」，不会造出假的选项。
	if heights := fanqieVariantHeights(payload); len(heights) > 1 {
		media.QualityOptions = heights
		media.Quality = int(mapFloat(payload, "quality"))
		if media.Quality == 0 {
			// 引擎没回 quality 字段：用户明确选了档位就回显选中的，
			// 否则（自动档）保持 0，让客户端显示「自动」而不是编一个数字。
			media.Quality = quality
		}
	}
	return media, nil
}

// fanqieVariantHeights 提取清晰度档位（按短边像素，如 1080/720/540/480/360），
// 去重后由高到低排列。
func fanqieVariantHeights(payload map[string]any) []int {
	rows, _ := payload["variants"].([]any)
	seen := map[int]bool{}
	heights := make([]int, 0, len(rows))
	for _, row := range rows {
		entry, _ := row.(map[string]any)
		if entry == nil {
			continue
		}
		height := int(mapFloat(entry, "height"))
		if width := int(mapFloat(entry, "width")); width > 0 && height > width {
			height = width // 竖屏剧按短边算档位
		} else if height <= 0 {
			height = width
		}
		if height <= 0 || seen[height] {
			continue
		}
		seen[height] = true
		heights = append(heights, height)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(heights)))
	return heights
}

// fanqieHelperTypes: 小工具，集中在一处便于复查。

func mapFloat(row map[string]any, key string) float64 {
	if row == nil {
		return 0
	}
	switch v := row[key].(type) {
	case float64:
		return v
	case string:
		n, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return n
	case json.Number:
		n, _ := v.Float64()
		return n
	}
	return 0
}

func mapBool(row map[string]any, key string) bool {
	if row == nil {
		return false
	}
	v, _ := row[key].(bool)
	return v
}

func boolPtr(v bool) *bool { return &v }
