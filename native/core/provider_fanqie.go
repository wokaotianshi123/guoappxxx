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
	"net/url"
	"sort"
	"strconv"
	"strings"
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

// fanqieOfflineHint 把「连不上本机引擎」翻译成可操作的提示。
// 桌面端 / 本地服务器端没有内嵌引擎，直接连 127.0.0.1:18921 会得到连接失败，
// 原始错误对用户没有任何指引意义。
//
// 关键字按「可能的报错文案」覆盖三层，避免换个平台/换个传输实现就漏判：
//   - Go 标准库：connect: connection refused / dial tcp ... / no such host / EOF
//   - tls-client（Rust 后端）：upstream connect failed: ... (os error 10061)
//   - Windows 本地化文案：由于目标计算机积极拒绝，无法连接。
func fanqieOfflineHint(err error) error {
	if err == nil {
		return nil
	}
	lower := strings.ToLower(err.Error())
	for _, marker := range []string{
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
	} {
		if strings.Contains(lower, marker) {
			return fmt.Errorf("番茄内容引擎未就绪（本机 %s）：Android 版已内置引擎，桌面端/本地服务器端需先启动番茄引擎", fanqieEngineAddress)
		}
	}
	return err
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

// fanqieGetJSON GET 一个本机引擎路由并解出顶层 JSON。
func (d *Downloader) fanqieGetJSON(ctx context.Context, rawURL string) (map[string]any, error) {
	text, err := d.fetchProviderText(ctx, rawURL, d.fanqieBase()+"/")
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
