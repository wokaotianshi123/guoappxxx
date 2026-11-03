package core

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	xchinaSiteURL   = "https://xchina.tube"
	xchinaApp       = "xchina.tube"
	xchinaVersion   = "260901"
	xchinaPassword  = "xxx"
	xchinaUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	xchinaVideoMark = "xchina-video://"
)

var (
	xchinaBaseURL    = "https://v2.cdn199.com"
	xchinaAPIHosts   = []string{"https://v2.cdn199.com", "https://v2.kekecdn.net", "https://v2.luchu.org", "https://v2.madou.ws", "https://v2.papapa.biz", "https://v2.xiaoshuo.info", "https://v2.xiaoshuo.la"}
	xchinaImageHosts = []string{"https://tp.xiaoxx.com", "https://tp.losers.app", "https://tp.pppsuccess.com", "https://tp.365novel.com", "https://tp.family4k.com", "https://tp.helloye.com"}

	xchinaCategoryEntries = []struct {
		ID   string
		Name string
	}{
		{"recent", "最新视频"}, {"hot", "当前最热"}, {"week_hot", "周榜"}, {"month_hot", "月榜"},
		{"high", "站长推荐"}, {"5mins", "5分钟+"}, {"10mins", "10分钟+"},
		{"c0", "国产视频"}, {"c1", "麻豆视频"}, {"c2", "91制片厂"}, {"c3", "玩偶姐姐"},
		{"c4", "小鸟酱专题"}, {"c5", "糖心Vlog"}, {"c6", "天美传媒"}, {"c7", "蜜桃传媒"},
		{"c8", "皇家华人"}, {"c9", "星空传媒"},
		{"japan", "日本AV"}, {"av", "高清AV"}, {"uncensored", "无码流出"}, {"west", "欧美"},
	}
)

type xchinaClient struct {
	mu     sync.Mutex
	host   string
	image  string
	detail map[string]map[string]any
}

func (d *Downloader) xchina() *xchinaClient {
	d.xchinaOnce.Do(func() {
		d.xchinaCore = &xchinaClient{host: xchinaBaseURL, image: xchinaImageHosts[0], detail: map[string]map[string]any{}}
	})
	return d.xchinaCore
}

func (c *xchinaClient) endpoint() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.host == "" {
		c.host = xchinaBaseURL
	}
	return c.host
}

func (c *xchinaClient) rememberHost(host string) {
	c.mu.Lock()
	c.host = host
	c.mu.Unlock()
}

func (c *xchinaClient) imageURL(id string) string {
	c.mu.Lock()
	host := c.image
	c.mu.Unlock()
	if host == "" {
		host = xchinaImageHosts[0]
	}
	return host + "/" + url.PathEscape(strings.TrimSpace(id)) + ".jpg"
}

func (c *xchinaClient) rememberDetail(id string, row map[string]any) {
	c.mu.Lock()
	if len(c.detail) >= 300 {
		c.detail = map[string]map[string]any{}
	}
	c.detail[id] = row
	c.mu.Unlock()
}

func (c *xchinaClient) cachedDetail(id string) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.detail[id]
}

func xchinaDecrypt(value string) ([]byte, error) {
	raw, err := decodeHongguoBase64(value)
	if err != nil || len(raw) < 32 || string(raw[:8]) != "Salted__" {
		return nil, errors.New("XChina 响应不是有效的加密数据")
	}
	key, iv := xchinaEVPKey([]byte(xchinaPassword), raw[8:16])
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(raw)-16)
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, raw[16:])
	return pkcs7Unpad(plain, aes.BlockSize)
}

func xchinaEVPKey(password, salt []byte) ([]byte, []byte) {
	derived := make([]byte, 0, 48)
	var previous []byte
	for len(derived) < 48 {
		sum := md5.Sum(append(append(append([]byte(nil), previous...), password...), salt...))
		previous = sum[:]
		derived = append(derived, previous...)
	}
	return derived[:32], derived[32:48]
}

func (c *xchinaClient) call(ctx context.Context, d *Downloader, path string, payload map[string]any) (any, error) {
	body := map[string]any{
		"app":          xchinaApp,
		"isStandalone": false,
		"theLink":      "novaluenull",
		"uuid":         "novalue",
		"url":          path,
		"version":      xchinaVersion,
	}
	for key, value := range payload {
		body[key] = value
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	hosts := append([]string{c.endpoint()}, xchinaAPIHosts...)
	var lastErr error
	for _, host := range hosts {
		for attempt := 0; attempt < 3; attempt++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if attempt > 0 {
				select {
				case <-time.After(time.Duration(800+attempt*700) * time.Millisecond):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(host, "/")+"/js", bytes.NewReader(encoded))
			if err != nil {
				return nil, err
			}
			request.Header.Set("User-Agent", xchinaUserAgent)
			request.Header.Set("Accept", "*/*")
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", xchinaSiteURL)
			request.Header.Set("Referer", xchinaSiteURL+"/")
			response, err := d.doPreparedCatalogRequest(request, 20*time.Second, nil)
			if err != nil {
				lastErr = err
				continue
			}
			content, readErr := io.ReadAll(io.LimitReader(response.Body, providerMaxBodyBytes+1))
			response.Body.Close()
			if readErr != nil {
				lastErr = readErr
				continue
			}
			if response.StatusCode != http.StatusOK {
				lastErr = fmt.Errorf("XChina 接口 HTTP %d", response.StatusCode)
				continue
			}
			decoded, err := xchinaUnwrap(content)
			if err != nil {
				lastErr = err
				continue
			}
			c.rememberHost(host)
			return decoded, nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("XChina 接口暂时不可用")
	}
	return nil, lastErr
}

func xchinaUnwrap(content []byte) (any, error) {
	var envelope map[string]any
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&envelope); err != nil {
		return nil, errors.New("XChina 接口返回格式异常")
	}
	if encoded, ok := envelope["r"].(string); ok && encoded != "" {
		plain, err := xchinaDecrypt(encoded)
		if err != nil {
			return nil, err
		}
		var decoded any
		inner := json.NewDecoder(bytes.NewReader(plain))
		inner.UseNumber()
		if err := inner.Decode(&decoded); err != nil {
			return nil, errors.New("XChina 加密响应解析失败")
		}
		return decoded, nil
	}
	if blocked, ok := envelope["blocked"].(bool); ok && blocked {
		return nil, errors.New("XChina 请求被临时限流")
	}
	var decoded any
	inner := json.NewDecoder(bytes.NewReader(content))
	inner.UseNumber()
	if err := inner.Decode(&decoded); err != nil {
		return nil, errors.New("XChina 接口返回格式异常")
	}
	return decoded, nil
}

func xchinaDrama(row map[string]any, client *xchinaClient) Drama {
	id := strings.TrimSpace(mapString(row, "id"))
	if id == "" {
		return Drama{}
	}
	title := strings.TrimSpace(mapString(row, "title"))
	if strings.EqualFold(mapString(row, "vip"), "true") || mapString(row, "vip") == "1" {
		title = "[VIP] " + title
	}
	remark := mapString(row, "durationStr")
	if remark == "" {
		if seconds, err := strconv.Atoi(mapString(row, "duration")); err == nil && seconds >= 60 {
			remark = strconv.Itoa(seconds/60) + "分钟"
		}
	}
	online := mapString(row, "time")
	if len(online) >= 10 {
		online = online[:10]
	}
	drama := Drama{
		ID:          providerDramaID(sourceXChina, id),
		Source:      sourceXChina,
		SourceID:    id,
		Title:       title,
		Name:        title,
		Cover:       client.imageURL(id),
		Remark:      remark,
		Intro:       mapString(row, "keywords_gem"),
		ChannelName: "XChina",
		TypeName:    mapString(row, "videoType"),
		OnlineDate:  online,
	}
	if drama.Title == "" {
		drama.Title = id
		drama.Name = id
	}
	return drama
}

func xchinaRows(value any) []map[string]any {
	var rows []map[string]any
	switch list := value.(type) {
	case []any:
		for _, entry := range list {
			if row, ok := entry.(map[string]any); ok {
				rows = append(rows, row)
			}
		}
	case map[string]any:
		for _, entry := range anyList(list["list"]) {
			if row, ok := entry.(map[string]any); ok {
				rows = append(rows, row)
			}
		}
	}
	return rows
}

func (d *Downloader) fetchXChinaCatalogPage(ctx context.Context, page int, category, query string) ([]Drama, bool, error) {
	if page < 1 {
		page = 1
	}
	client := d.xchina()
	var decoded any
	var err error
	if strings.TrimSpace(query) != "" {
		decoded, err = client.call(ctx, d, "/searchSevenVideos", map[string]any{
			"page": page, "keywords": strings.TrimSpace(query), "filterCategory": "all",
			"filterTimeInDays": "all", "filterViews": "all", "filterDurationInMins": "all", "sortBy": "relevance",
		})
	} else {
		if !validXChinaCategory(category) {
			category = "recent"
		}
		decoded, err = client.call(ctx, d, "/sevenVideos?page="+strconv.Itoa(page)+"&type="+url.QueryEscape(category), nil)
	}
	if err != nil {
		return nil, false, err
	}
	rows := xchinaRows(decoded)
	items := make([]Drama, 0, len(rows))
	for _, row := range rows {
		if drama := xchinaDrama(row, client); drama.ID != "" {
			items = append(items, drama)
		}
	}
	return items, len(items) > 0, nil
}

func validXChinaCategory(category string) bool {
	category = strings.TrimSpace(category)
	if category == "" {
		return false
	}
	for _, entry := range xchinaCategoryEntries {
		if entry.ID == category {
			return true
		}
	}
	return false
}

func (d *Downloader) fetchXChinaCategories() []nativeCategory {
	all := make([]nativeCategory, 0, len(xchinaCategoryEntries))
	for _, entry := range xchinaCategoryEntries {
		all = append(all, nativeCategory{ID: entry.ID, Name: entry.Name})
	}
	return all
}

func (d *Downloader) fetchXChinaDetail(ctx context.Context, sourceID string) (Drama, []Chapter, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" || strings.ContainsAny(sourceID, "/?#") {
		return Drama{}, nil, errors.New("XChina 剧集 ID 无效")
	}
	client := d.xchina()
	decoded, err := client.call(ctx, d, "/sevenVideos/"+url.PathEscape(sourceID), map[string]any{"url_search": ""})
	if err != nil {
		if cached := client.cachedDetail(sourceID); cached != nil {
			decoded = cached
		} else {
			return Drama{}, nil, err
		}
	}
	row, ok := decoded.(map[string]any)
	if !ok || strings.TrimSpace(mapString(row, "id")) == "" {
		return Drama{}, nil, errors.New("XChina 未返回该视频资料")
	}
	client.rememberDetail(sourceID, row)
	drama := xchinaDrama(row, client)
	if drama.ID == "" {
		return Drama{}, nil, errors.New("XChina 视频资料无效")
	}
	chapters := []Chapter{{
		ID:             providerChapterID(sourceXChina, sourceID, "main"),
		Source:         sourceXChina,
		Title:          "正片",
		VideoURL:       xchinaVideoMark + sourceID,
		CurrentEpisode: rawEpisode(1),
	}}
	return drama, chapters, nil
}

func (d *Downloader) resolveXChinaMedia(ctx context.Context, task Task) (providerMedia, error) {
	videoID := strings.TrimSpace(strings.TrimPrefix(task.Chapter.VideoURL, xchinaVideoMark))
	if videoID == "" {
		if _, sourceID, ok := splitProviderDramaID(task.DramaID); ok {
			videoID = sourceID
		}
	}
	if videoID == "" || strings.ContainsAny(videoID, "/?#") {
		return providerMedia{}, errors.New("XChina 播放请求缺少有效的视频 ID")
	}
	client := d.xchina()
	decoded, err := client.call(ctx, d, "/sevenVideos/"+url.PathEscape(videoID), map[string]any{"url_search": ""})
	if err != nil {
		if cached := client.cachedDetail(videoID); cached != nil {
			decoded = cached
		} else {
			return providerMedia{}, err
		}
	}
	row, ok := decoded.(map[string]any)
	if !ok {
		if cached := client.cachedDetail(videoID); cached != nil {
			row = cached
		}
	}
	address := xchinaPlaylist(row)
	if address == "" {
		return providerMedia{}, errors.New("XChina 该视频未返回可播放地址")
	}
	return providerMedia{
		URL:     address,
		Referer: xchinaSiteURL + "/",
		credentials: &providerMediaCredentials{
			referer:   xchinaSiteURL + "/",
			userAgent: xchinaUserAgent,
		},
	}, nil
}

func xchinaPlaylist(row map[string]any) string {
	candidates := anyList(row["m3u8s"])
	if len(candidates) == 0 {
		if single := strings.TrimSpace(mapString(row, "m3u8")); single != "" {
			candidates = []any{single}
		}
	}
	for _, candidate := range candidates {
		address := strings.TrimSpace(fmt.Sprint(candidate))
		if address == "" || address == "<nil>" {
			continue
		}
		if strings.HasPrefix(address, "//") {
			address = "https:" + address
		}
		if strings.HasPrefix(address, "/") {
			address = xchinaSiteURL + address
		}
		if isProviderHTTPMediaURL(address) {
			return address
		}
	}
	return ""
}
