package core

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// theporny 站点接入。
//
// 协议：所有接口都是对 https://<house>/js 的 POST，body 里带 deviceInfo/app/theLink/uuid/url/version，
// 响应是 {"r": "<base64>"}，用 OpenSSL "Salted__" + AES-256-CBC（EVP_BytesToKey/MD5）解出明文 JSON。
// 该站按 TLS 指纹（JA3/JA4）拦截普通客户端，必须使用 Chrome 指纹客户端，因此这里复用 tls-client。
const (
	thepornySiteURL   = "https://theporny.com"
	thepornyApp       = "theporny.com"
	thepornyVersion   = "260901"
	thepornyLink      = "https://rs.pppsuccess.com"
	thepornyUUID      = "U202610220711518149"
	thepornySecret    = "xxx"
	thepornyVideoMark = "theporny-video://"
	thepornyUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	thepornyEmail     = "61722679@qq.com"
	thepornyPassword  = "9k8t57ri3q"
)

var (
	thepornyHosts = []string{
		"https://v2.cdn199.com", "https://v2.kekecdn.net", "https://v2.xiaoshuo.info",
		"https://v2.madou.ws", "https://v2.papapa.biz", "https://v2.luchu.org", "https://v2.tianmtv.com",
	}

	thepornyLines = []struct {
		Server string
		Name   string
	}{
		{"hd1", "高速1"}, {"hd2", "高速2"}, {"hd3", "高速3"}, {"default1", "免费线路"},
	}

	thepornyCategories = []struct {
		ID   string
		Name string
		Type string
	}{
		{"recent", "最新", "recent"}, {"c0", "国产传媒", "c0"}, {"japan", "日本高清", "japan"},
		{"swag", "台湾Swag", "swag"}, {"c1", "麻豆视频", "c1"}, {"c2", "91制片厂", "c2"},
		{"c3", "玩偶姐姐", "c3"}, {"c4", "小鸟酱", "c4"}, {"c5", "糖心Vlog", "c5"},
		{"c6", "天美传媒", "c6"}, {"c7", "蜜桃传媒", "c7"}, {"c8", "皇家华人", "c8"},
		{"c9", "星空传媒", "c9"}, {"ca", "精东影业", "ca"},
	}
)

type thepornyLogin struct {
	token  string
	at     time.Time
	failed time.Time
}

type thepornyClient struct {
	mu      sync.Mutex
	host    string
	logins  map[string]thepornyLogin
	detail  map[string]map[string]any
	browser map[string]browserHTTPClient
}

func (d *Downloader) theporny() *thepornyClient {
	d.thepornyOnce.Do(func() {
		d.thepornyCore = &thepornyClient{
			host: thepornyHosts[0], logins: map[string]thepornyLogin{},
			detail: map[string]map[string]any{}, browser: map[string]browserHTTPClient{},
		}
	})
	return d.thepornyCore
}

func thepornyDecrypt(value string) ([]byte, error) {
	raw, err := decodeHongguoBase64(value)
	if err != nil || len(raw) < 32 || string(raw[:8]) != "Salted__" {
		return nil, errors.New("Theporny 响应不是有效的加密数据")
	}
	key, iv := xchinaEVPKey([]byte(thepornySecret), raw[8:16])
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(raw)-16)
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, raw[16:])
	return pkcs7Unpad(plain, aes.BlockSize)
}

func (c *thepornyClient) currentHost() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.host == "" {
		c.host = thepornyHosts[0]
	}
	return c.host
}

func (c *thepornyClient) rememberHost(host string) {
	c.mu.Lock()
	c.host = host
	c.mu.Unlock()
}

// browserFor 为每个域名维护一个带 Cookie 的 Chrome 指纹客户端（登录态靠 Cookie 维持）。
func (c *thepornyClient) browserFor(d *Downloader, host string) (browserHTTPClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if client := c.browser[host]; client != nil {
		return client, nil
	}
	idle := 60 * time.Second
	options := []tlsclient.HttpClientOption{
		tlsclient.WithClientProfile(profiles.Chrome_150),
		tlsclient.WithRandomTLSExtensionOrder(),
		tlsclient.WithDisableHttp3(),
		tlsclient.WithNotFollowRedirects(),
		tlsclient.WithTimeoutSeconds(40),
		tlsclient.WithCookieJar(tlsclient.NewCookieJar()),
		tlsclient.WithTransportOptions(&tlsclient.TransportOptions{
			IdleConnTimeout: &idle, MaxIdleConns: 6, MaxIdleConnsPerHost: 4,
			MaxResponseHeaderBytes: 1 << 20,
			// 与标准传输保持一致：Android 端纯 Go 不加载系统信任库，
			// 这里显式挂上「系统信任库 + 打包 CA 全集」，避免指纹客户端握手失败。
			RootCAs: systemCertPoolWithBundle(),
		}),
	}
	if d != nil && d.cfg.InsecureTLS {
		options = append(options, tlsclient.WithInsecureSkipVerify())
	}
	client, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), options...)
	if err != nil {
		return nil, err
	}
	if len(c.browser) >= 8 {
		for key, old := range c.browser {
			old.CloseIdleConnections()
			delete(c.browser, key)
			break
		}
	}
	c.browser[host] = client
	return client, nil
}

func (c *thepornyClient) post(ctx context.Context, d *Downloader, host, path string, extra map[string]any) (any, error) {
	body := map[string]any{
		"deviceInfo": map[string]any{
			"model": "X11", "platform": "web", "operatingSystem": "unknown", "osVersion": "x86.64",
			"manufacturer": "Google Inc.", "isVirtual": false, "webViewVersion": "155.0.0.0",
		},
		"app": thepornyApp, "isStandalone": false, "theLink": thepornyLink,
		"uuid": thepornyUUID, "url": path, "version": thepornyVersion,
	}
	for key, value := range extra {
		body[key] = value
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	client, err := c.browserFor(d, host)
	if err != nil {
		return nil, err
	}
	request, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodPost, strings.TrimRight(host, "/")+"/js", bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("user-agent", thepornyUserAgent)
	request.Header.Set("accept", "*/*")
	request.Header.Set("accept-language", "zh-CN,zh;q=0.9")
	request.Header.Set("content-type", "application/json")
	request.Header.Set("origin", thepornySiteURL)
	request.Header.Set("referer", thepornySiteURL+"/")
	request.Header.Set(fhttp.HeaderOrderKey, "user-agent,accept,accept-language,content-type,origin,referer")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, providerMaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("Theporny 接口 HTTP %d", response.StatusCode)
	}
	return thepornyUnwrap(content)
}

func thepornyUnwrap(content []byte) (any, error) {
	var envelope map[string]any
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&envelope); err != nil {
		return nil, errors.New("Theporny 接口返回格式异常")
	}
	if blocked, ok := envelope["blocked"].(bool); ok && blocked {
		return nil, errors.New("Theporny 请求被拦截")
	}
	if encoded, ok := envelope["r"].(string); ok && encoded != "" {
		plain, err := thepornyDecrypt(encoded)
		if err != nil {
			return nil, err
		}
		var decoded any
		inner := json.NewDecoder(bytes.NewReader(plain))
		inner.UseNumber()
		if err := inner.Decode(&decoded); err != nil {
			return nil, errors.New("Theporny 加密响应解析失败")
		}
		return decoded, nil
	}
	var decoded any
	inner := json.NewDecoder(bytes.NewReader(content))
	inner.UseNumber()
	if err := inner.Decode(&decoded); err != nil {
		return nil, errors.New("Theporny 接口返回格式异常")
	}
	return decoded, nil
}

func (c *thepornyClient) login(ctx context.Context, d *Downloader, host string) error {
	decoded, err := c.post(ctx, d, host, "/sevenVideoUserLogin", map[string]any{
		"userEmail": thepornyEmail, "password": thepornyPassword,
	})
	if err != nil {
		return err
	}
	row, ok := decoded.(map[string]any)
	if !ok {
		return errors.New("Theporny 登录响应异常")
	}
	token := strings.TrimSpace(mapString(row, "token"))
	if token == "" {
		return errors.New("Theporny 登录未返回令牌")
	}
	c.mu.Lock()
	if c.logins == nil {
		c.logins = map[string]thepornyLogin{}
	}
	c.logins[host] = thepornyLogin{token: token, at: time.Now()}
	c.mu.Unlock()
	return nil
}

// ensureLogin 按域名判断登录态：Cookie 是每域名独立的，不能跨域名复用。
// 登录失败后 60 秒内不再重试，避免每条线路都卡在一次失败登录上。
func (c *thepornyClient) ensureLogin(ctx context.Context, d *Downloader, host string) {
	c.mu.Lock()
	state := c.logins[host]
	c.mu.Unlock()
	if state.token != "" && time.Since(state.at) < 6*time.Hour {
		return
	}
	if !state.failed.IsZero() && time.Since(state.failed) < time.Minute {
		return
	}
	if err := c.login(ctx, d, host); err != nil {
		c.mu.Lock()
		state.failed = time.Now()
		c.logins[host] = state
		c.mu.Unlock()
	}
}

// call 依次尝试各线路域名，成功即记住该域名。
func (c *thepornyClient) call(ctx context.Context, d *Downloader, path string, extra map[string]any) (any, error) {
	hosts := append([]string{c.currentHost()}, thepornyHosts...)
	var lastErr error
	for _, host := range hosts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if path != "/sevenVideoUserLogin" {
			c.ensureLogin(ctx, d, host)
		}
		decoded, err := c.post(ctx, d, host, path, extra)
		if err != nil {
			lastErr = err
			continue
		}
		c.rememberHost(host)
		return decoded, nil
	}
	if lastErr == nil {
		lastErr = errors.New("Theporny 接口暂时不可用")
	}
	return nil, lastErr
}

func thepornyThumb(row map[string]any) string {
	for _, key := range []string{"thumbnails", "thumbNails", "thumbnail"} {
		switch value := row[key].(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		case []any:
			for _, entry := range value {
				address, ok := entry.(string)
				if ok && strings.HasPrefix(strings.TrimSpace(address), "http") {
					return strings.TrimSpace(address)
				}
			}
		}
	}
	return ""
}

func thepornyDrama(row map[string]any) Drama {
	id := strings.TrimSpace(firstNonEmpty(mapString(row, "id"), mapString(row, "vId")))
	if id == "" {
		return Drama{}
	}
	title := strings.TrimSpace(mapString(row, "title"))
	if title == "" {
		title = id
	}
	online := mapString(row, "time")
	if len(online) >= 10 {
		online = online[:10]
	}
	return Drama{
		ID:          providerDramaID(sourceTheporny, id),
		Source:      sourceTheporny,
		SourceID:    id,
		Title:       title,
		Name:        title,
		Cover:       thepornyThumb(row),
		Remark:      mapString(row, "durationStr"),
		Intro:       strings.ReplaceAll(mapString(row, "keywords_gem"), ",", " / "),
		ChannelName: "Theporny",
		TypeName:    mapString(row, "videoType"),
		OnlineDate:  online,
	}
}

func thepornyRows(value any) []map[string]any {
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

func validThepornyCategory(category string) (string, bool) {
	category = strings.TrimSpace(category)
	for _, entry := range thepornyCategories {
		if entry.ID == category {
			return entry.Type, true
		}
	}
	return "", false
}

func (d *Downloader) fetchThepornyCatalogPage(ctx context.Context, page int, category, query string) ([]Drama, bool, error) {
	if page < 1 {
		page = 1
	}
	client := d.theporny()
	var decoded any
	var err error
	if strings.TrimSpace(query) != "" {
		decoded, err = client.call(ctx, d, "/searchSevenVideos", map[string]any{
			"page": page, "keywords": strings.TrimSpace(query), "filterCategory": "all",
			"filterTimeInDays": "all", "filterViews": "all", "filterDurationInMins": "all",
			"sortBy": "time", "keywordLogic": "and",
		})
	} else {
		kind, ok := validThepornyCategory(category)
		if !ok {
			kind = "recent"
		}
		decoded, err = client.call(ctx, d, "/sevenVideos?page="+strconv.Itoa(page)+"&type="+kind, nil)
	}
	if err != nil {
		return nil, false, err
	}
	rows := thepornyRows(decoded)
	items := make([]Drama, 0, len(rows))
	for _, row := range rows {
		if drama := thepornyDrama(row); drama.ID != "" {
			items = append(items, drama)
		}
	}
	return items, len(items) >= 20, nil
}

func (d *Downloader) fetchThepornyCategories() []nativeCategory {
	all := make([]nativeCategory, 0, len(thepornyCategories))
	for _, entry := range thepornyCategories {
		all = append(all, nativeCategory{ID: entry.ID, Name: entry.Name})
	}
	return all
}

func (d *Downloader) fetchThepornyDetail(ctx context.Context, sourceID string) (Drama, []Chapter, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" || strings.ContainsAny(sourceID, "/?#") {
		return Drama{}, nil, errors.New("Theporny 视频 ID 无效")
	}
	client := d.theporny()
	row, err := client.videoRow(ctx, d, sourceID)
	if err != nil {
		return Drama{}, nil, err
	}
	drama := thepornyDrama(row)
	if drama.ID == "" {
		drama = Drama{
			ID: providerDramaID(sourceTheporny, sourceID), Source: sourceTheporny,
			SourceID: sourceID, Title: sourceID, Name: sourceID, ChannelName: "Theporny",
		}
	}
	client.rememberDetail(sourceID, row)
	chapters := []Chapter{{
		ID:             providerChapterID(sourceTheporny, sourceID, "main"),
		Source:         sourceTheporny,
		Title:          "正片",
		VideoURL:       thepornyVideoMark + sourceID,
		CurrentEpisode: rawEpisode(1),
	}}
	return drama, chapters, nil
}

func thepornyPlaylist(row map[string]any) string {
	for _, candidate := range anyList(row["m3u8s"]) {
		address := strings.TrimSpace(fmt.Sprint(candidate))
		if strings.HasPrefix(address, "//") {
			address = "https:" + address
		}
		if isProviderHTTPMediaURL(address) {
			return address
		}
	}
	return ""
}

func (d *Downloader) resolveThepornyMedia(ctx context.Context, task Task) (providerMedia, error) {
	videoID := strings.TrimSpace(strings.TrimPrefix(task.Chapter.VideoURL, thepornyVideoMark))
	if videoID == "" {
		if _, sourceID, ok := splitProviderDramaID(task.DramaID); ok {
			videoID = sourceID
		}
	}
	if videoID == "" || strings.ContainsAny(videoID, "/?#") {
		return providerMedia{}, errors.New("Theporny 播放请求缺少有效的视频 ID")
	}
	client := d.theporny()
	var lastErr error
	for _, line := range thepornyLines {
		decoded, err := client.call(ctx, d, "/sevenVideos/"+videoID, map[string]any{"server": line.Server})
		if err != nil {
			lastErr = err
			continue
		}
		row, ok := decoded.(map[string]any)
		if !ok {
			continue
		}
		if address := thepornyPlaylist(row); address != "" {
			return providerMedia{URL: address, Referer: thepornySiteURL + "/"}, nil
		}
	}
	if cached := client.cachedDetail(videoID); cached != nil {
		if address := thepornyPlaylist(cached); address != "" {
			return providerMedia{URL: address, Referer: thepornySiteURL + "/"}, nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("Theporny 该视频未返回可播放地址")
	}
	return providerMedia{}, lastErr
}

// videoRow 依次尝试各线路拉详情，成功即缓存；全部失败时退回缓存。
func (c *thepornyClient) videoRow(ctx context.Context, d *Downloader, videoID string) (map[string]any, error) {
	var lastErr error
	for _, line := range thepornyLines {
		decoded, err := c.call(ctx, d, "/sevenVideos/"+videoID, map[string]any{"server": line.Server})
		if err != nil {
			lastErr = err
			continue
		}
		row, ok := decoded.(map[string]any)
		if !ok {
			continue
		}
		if mapString(row, "title") != "" || len(anyList(row["m3u8s"])) > 0 {
			c.rememberDetail(videoID, row)
			return row, nil
		}
	}
	if cached := c.cachedDetail(videoID); cached != nil {
		return cached, nil
	}
	if lastErr == nil {
		lastErr = errors.New("Theporny 未返回该视频资料")
	}
	return nil, lastErr
}

func (c *thepornyClient) rememberDetail(videoID string, row map[string]any) {
	c.mu.Lock()
	if len(c.detail) >= 200 {
		c.detail = map[string]map[string]any{}
	}
	c.detail[videoID] = row
	c.mu.Unlock()
}

func (c *thepornyClient) cachedDetail(videoID string) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.detail[videoID]
}
