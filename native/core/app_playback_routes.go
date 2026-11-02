package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"time"
)

type nativePlaybackChoice struct {
	danmakuSeries string
	danmakuVideo  string
	media         []providerMedia
	index         int
	qualities     []int
	streamSession string
	adBlock       bool
	created       time.Time
}

func nativePlaybackChoices(media providerMedia, quality int) nativePlaybackChoice {
	choice := nativePlaybackChoice{qualities: []int{}}
	seen := map[string]bool{}
	qualities := map[int]bool{}
	for _, option := range append([]providerMedia{media}, media.Variants...) {
		if !isProviderHTTPMediaURL(option.URL) {
			continue
		}
		identity := option.URL + "\x00" + option.Referer + "\x00" + hex.EncodeToString(option.CENCKey) + "\x00" + hex.EncodeToString(option.HLSKey)
		if seen[identity] {
			continue
		}
		seen[identity] = true
		option.Variants = nil
		choice.media = append(choice.media, option)
		if option.Quality > 0 {
			qualities[option.Quality] = true
		}
	}
	sort.SliceStable(choice.media, func(i, j int) bool { return choice.media[i].Quality > choice.media[j].Quality })
	if len(media.QualityOptions) > 0 {
		// 站源直接给出档位（如番茄：每档单独缓存）——照抄，不要靠线路去反推，
		// 否则会为了列档位而伪造出并不存在的「线路」。
		choice.qualities = append(choice.qualities, media.QualityOptions...)
	} else {
		for value := range qualities {
			choice.qualities = append(choice.qualities, value)
		}
		sort.Sort(sort.Reverse(sort.IntSlice(choice.qualities)))
	}
	if quality > 0 && qualities[quality] {
		filtered := make([]providerMedia, 0, len(choice.media))
		for _, option := range choice.media {
			if option.Quality == quality {
				filtered = append(filtered, option)
			}
		}
		choice.media = filtered
	}
	return choice
}

func (engine *nativeEngine) nativeOpenPlayback(ctx context.Context, choice nativePlaybackChoice) (nativePlan, error) {
	if err := ctx.Err(); err != nil {
		return nativePlan{}, err
	}
	if choice.index < 0 || choice.index >= len(choice.media) {
		return nativePlan{}, errors.New("该集没有其他可用的播放线路")
	}
	media := choice.media[choice.index]
	var err error
	if !isProviderHTTPMediaURL(media.URL) {
		return nativePlan{}, errors.New("站源未返回有效的播放地址")
	}
	tokenBytes := make([]byte, 24)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nativePlan{}, errors.New("无法初始化播放会话")
	}
	plan := nativePlan{
		DanmakuID: choice.danmakuVideo,
		URL:       media.URL, Headers: map[string]string{"User-Agent": userAgent, "Referer": media.Referer},
		DirectURL: nativeCastDirectURL(media),
		Key:       hex.EncodeToString(media.CENCKey), Quality: media.Quality, Qualities: choice.qualities,
		RouteIndex: choice.index, RouteCount: len(choice.media), Session: hex.EncodeToString(tokenBytes),
	}
	if media.credentials != nil && !media.credentials.expires.IsZero() {
		if !time.Now().Before(media.credentials.expires) {
			return nativePlan{}, errors.New("播放凭证已过期，请重新解析播放")
		}
		plan.ExpiresAt = media.credentials.expires.UnixMilli()
	}
	choice.streamSession = ""
	{
		engine.mu.Lock()
		if engine.stream == nil {
			engine.stream, err = newNativeStreamServer(engine.downloader)
		}
		stream := engine.stream
		engine.mu.Unlock()
		if err != nil {
			return nativePlan{}, err
		}
		plan.URL, choice.streamSession = stream.nativeOpen(media, choice.adBlock)
	}
	choice.created = time.Now()
	engine.mu.Lock()
	if engine.playbacks == nil {
		engine.playbacks = map[string]nativePlaybackChoice{}
	}
	var expired []string
	for token, old := range engine.playbacks {
		if time.Since(old.created) > 12*time.Hour {
			expired = append(expired, old.streamSession)
			delete(engine.playbacks, token)
		}
	}
	if len(engine.playbacks) >= 8 {
		oldest := ""
		for token, old := range engine.playbacks {
			if oldest == "" || old.created.Before(engine.playbacks[oldest].created) {
				oldest = token
			}
		}
		expired = append(expired, engine.playbacks[oldest].streamSession)
		delete(engine.playbacks, oldest)
	}
	engine.playbacks[plan.Session] = choice
	stream := engine.stream
	engine.mu.Unlock()
	if stream != nil {
		for _, token := range expired {
			stream.nativeRelease(token)
		}
	}
	if err := ctx.Err(); err != nil {
		engine.nativeReleasePlayback(plan.Session)
		return nativePlan{}, err
	}
	return plan, nil
}

func (engine *nativeEngine) nativeNextPlayback(ctx context.Context, session string) (nativePlan, error) {
	engine.mu.Lock()
	choice, exists := engine.playbacks[session]
	engine.mu.Unlock()
	if !exists {
		return nativePlan{}, errors.New("播放线路已失效，请重新解析播放")
	}
	choice.index++
	return engine.nativeOpenPlayback(ctx, choice)
}

func (engine *nativeEngine) nativeReleasePlayback(session string) {
	engine.mu.Lock()
	choice, exists := engine.playbacks[session]
	delete(engine.playbacks, session)
	stream := engine.stream
	engine.mu.Unlock()
	if exists && stream != nil && choice.streamSession != "" {
		stream.nativeRelease(choice.streamSession)
	}
}

func (engine *nativeEngine) nativeSelectRoute(ctx context.Context, session string, index int) (nativePlan, error) {
	engine.mu.Lock()
	choice, exists := engine.playbacks[session]
	engine.mu.Unlock()
	if !exists {
		return nativePlan{}, errors.New("播放线路已失效，请重新解析播放")
	}
	if index < 0 || index >= len(choice.media) {
		return nativePlan{}, errors.New("该播放线路不可用")
	}
	if index != choice.index {
		choice.index = index
	}
	return engine.nativeOpenPlayback(ctx, choice)
}

// nativeChoosePlayback 把站源返回的媒体整理成可切换的线路列表，并按
// preferred 选中线路。切集时若沿用玩家选中的线路，序号在这一集仍然存在就
// 继续用它；只有这一集没有这条线路（线路数量不够）时才回落到第一条，
// 避免每集都跳回线路 1。
func nativeChoosePlayback(media providerMedia, quality, preferred int) nativePlaybackChoice {
	choice := nativePlaybackChoices(media, quality)
	choice.index = nativePreferredRoute(preferred, len(choice.media))
	return choice
}

// nativePreferredRoute 把线路序号规整到当前这集的线路列表里：
// 序号无效或超出范围时回到第一条线路。
func nativePreferredRoute(preferred, count int) int {
	if preferred <= 0 || preferred >= count {
		return 0
	}
	return preferred
}
