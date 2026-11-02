package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// 跨源聚合搜索：一次搜索并发打所有可见站源，再按「标题 / 年份 / 集数」归一去重，
// 同一部剧保留多个站源副本，供界面一键切换播放源。

// nativeSearchAcrossResult 是聚合搜索的返回体。
// Items 为归一去重后的主条目（每组一条），每条的 Peers 带上其它站源的同一部剧。
// Sources 为实际成功返回的站源，Failures 记录失败/不支持的站源及原因。
type nativeSearchAcrossResult struct {
	Items    []nativeDrama     `json:"items"`
	Sources  []string          `json:"sources"`
	Failures map[string]string `json:"failures,omitempty"`
	Warning  string            `json:"warning,omitempty"`
}

// errSearchUnsupported 标记「站源没有在线搜索接口」。
// 单源搜索时要把原因告诉用户；跨源聚合搜索时则静默跳过，避免刷一屏无用提示。
var errSearchUnsupported = errors.New("站源暂不支持在线搜索")

// nativeSourceSupportsSearch 判断站源是否提供在线搜索接口。
// 这里只依据站源自身能力，不再把「已加载的本地资源」当作搜索来源。
func nativeSourceSupportsSearch(source string) bool {
	switch source {
	case sourceHongguo, sourceHuangju, sourceGuipian, sourceHanxiaoquan,
		sourceYeguo, sourceDSD, sourceSorani, sourceA123, sourceIkanbot, sourceFanqie:
		return true
	}
	if isDuanjuProviderSource(source) {
		return duanjuSupportsSearch(source)
	}
	return false
}

// nativeSearchSource 对单个站源执行在线搜索。
// 只走站源自身的搜索接口；不支持在线搜索的站源直接返回明确错误，不再回落到本地资源筛选。
func (engine *nativeEngine) nativeSearchSource(ctx context.Context, source, query string, page int) (nativeCatalogResult, error) {
	result := nativeCatalogResult{Items: []nativeDrama{}, Page: page}
	query = strings.TrimSpace(query)
	if query == "" {
		return result, nil
	}
	if page < 1 {
		page = 1
	}
	if !nativeSourceSupportsSearch(source) {
		return result, fmt.Errorf("%w：%s", errSearchUnsupported, duanjuSourceLabel(source))
	}
	d := engine.downloader
	switch {
	case source == sourceHongguo:
		entry, err := d.searchHongguoDramas(ctx, query)
		if err != nil {
			return result, err
		}
		for _, drama := range entry.Dramas {
			result.Items = append(result.Items, nativeNormalize(drama))
		}
		result.Warning, result.Page = entry.Warning, 1
		if entry.Limited && result.Warning == "" {
			result.Warning = "已显示当前可获取的匹配结果，使用更完整的剧名可继续查找"
		}
		return result, nil

	case source == sourceHuangju:
		items, more, err := d.fetchHuangjuCatalogPage(ctx, page, "", query)
		if err != nil {
			return result, err
		}
		for _, drama := range items {
			result.Items = append(result.Items, nativeNormalize(drama))
		}
		result.HasMore = more
		return result, nil

	case source == sourceGuipian:
		items, more, err := d.fetchGuipianCatalogPage(ctx, page, "", query)
		if err != nil {
			return result, err
		}
		for _, drama := range items {
			result.Items = append(result.Items, nativeNormalize(drama))
		}
		result.HasMore = more
		return result, nil

	case source == sourceHanxiaoquan:
		items, more, err := d.fetchHanxiaoquanCatalogPage(ctx, page, "", query)
		if err != nil {
			return result, err
		}
		for _, drama := range items {
			result.Items = append(result.Items, nativeNormalize(drama))
		}
		result.HasMore = more
		return result, nil

	case source == sourceYeguo || source == sourceDSD:
		var items []Drama
		var more bool
		var err error
		if source == sourceYeguo {
			items, more, err = d.fetchYeguoCatalogPage(ctx, page, "", query)
		} else {
			items, more, err = d.fetchDSDCatalogPage(ctx, page, "", query)
		}
		if err != nil {
			return result, err
		}
		for _, drama := range items {
			result.Items = append(result.Items, nativeNormalize(drama))
		}
		result.HasMore = more
		return result, nil

	case source == sourceSorani:
		items, more, err := d.fetchSoraniCatalogPage(ctx, page, "", query)
		if err != nil {
			return result, err
		}
		for _, drama := range items {
			result.Items = append(result.Items, nativeNormalize(drama))
		}
		result.HasMore = more
		return result, nil

	case isDuanjuProviderSource(source):
		items, err := d.searchDuanju(ctx, source, query)
		if err != nil {
			return result, err
		}
		for _, drama := range items {
			result.Items = append(result.Items, nativeNormalize(drama))
		}
		return result, nil

	case source == sourceFanqie:
		items, more, err := d.fetchFanqieSearchPage(ctx, page, query)
		if err != nil {
			return result, err
		}
		for _, drama := range items {
			result.Items = append(result.Items, nativeNormalize(drama))
		}
		result.HasMore = more
		return result, nil

	case source == sourceA123 || source == sourceIkanbot:
		var items []Drama
		var more bool
		var err error
		if source == sourceA123 {
			items, more, err = d.fetchA123CatalogPage(ctx, page, "", query)
		} else {
			items, more, err = d.fetchIkanbotCatalogPage(ctx, page, "", query)
		}
		if err != nil {
			return result, err
		}
		for _, drama := range items {
			result.Items = append(result.Items, nativeNormalize(drama))
		}
		result.HasMore = more
		return result, nil
	}
	return result, fmt.Errorf("%s 暂不支持在线搜索", duanjuSourceLabel(source))
}

// duanjuSourceLabel 返回站源的展示名，供错误提示使用。
func duanjuSourceLabel(source string) string {
	if isDuanjuProviderSource(source) {
		return duanjuSourceName(source)
	}
	if canonicalProviderSource(source) == sourceFanqie {
		return "番茄"
	}
	return source
}

// nativeSearchAcross 并发搜索多个站源并归一去重。
func (engine *nativeEngine) nativeSearchAcross(ctx context.Context, input nativeInput) (nativeSearchAcrossResult, error) {
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return nativeSearchAcrossResult{}, errors.New("请输入搜索内容")
	}
	seen := map[string]bool{}
	var sources []string
	for _, raw := range input.Sources {
		source := canonicalProviderSource(strings.TrimSpace(raw))
		if source == "" || seen[source] {
			continue
		}
		if !isHuangguoProviderSource(source) {
			continue
		}
		seen[source] = true
		sources = append(sources, source)
	}
	if len(sources) == 0 {
		return nativeSearchAcrossResult{}, errors.New("请选择有效站源")
	}

	page := input.Page
	if page < 1 {
		page = 1
	}
	const perSourceTimeout = 25 * time.Second
	concurrency := 6
	if len(sources) < concurrency {
		concurrency = len(sources)
	}

	type outcome struct {
		source string
		items  []nativeDrama
		err    error
	}
	results := make([]outcome, len(sources))
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	for index, source := range sources {
		wg.Add(1)
		go func(index int, source string) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					if engine != nil {
						engine.downloader.recordGoroutinePanic("searchAcross", r)
					}
				}
			}()
			sem <- struct{}{}
			defer func() { <-sem }()
			sourceCtx, cancel := context.WithTimeout(ctx, perSourceTimeout)
			defer cancel()
			found, err := engine.nativeSearchSource(sourceCtx, source, query, page)
			results[index] = outcome{source: source, items: found.Items, err: err}
		}(index, source)
	}
	wg.Wait()

	failures := map[string]string{}
	var succeeded []string
	var all []nativeDrama
	for _, item := range results {
		switch {
		case item.err != nil && errors.Is(item.err, errSearchUnsupported):
			// 该站源没有在线搜索接口，静默跳过。
		case item.err != nil:
			if errors.Is(item.err, context.Canceled) || errors.Is(item.err, context.DeadlineExceeded) {
				failures[item.source] = "搜索超时"
			} else {
				failures[item.source] = publicError(item.err).Error()
			}
		case len(item.items) == 0:
			succeeded = append(succeeded, item.source)
		default:
			succeeded = append(succeeded, item.source)
			all = append(all, item.items...)
		}
	}
	if len(all) == 0 && len(failures) > 0 {
		return nativeSearchAcrossResult{Sources: succeeded, Failures: failures}, errors.New(nativeSearchFailureText(failures))
	}
	items := nativeSearchMerge(all)
	warning := ""
	if len(failures) > 0 {
		warning = nativeSearchFailureText(failures)
	}
	return nativeSearchAcrossResult{Items: items, Sources: succeeded, Failures: failures, Warning: warning}, nil
}

// nativeSearchFailureText 把各站源的失败原因合并成一句提示。
func nativeSearchFailureText(failures map[string]string) string {
	names := make([]string, 0, len(failures))
	for source := range failures {
		names = append(names, duanjuSourceLabel(source))
	}
	sort.Strings(names)
	if len(names) == 1 {
		return fmt.Sprintf("%s：%s", names[0], failures[names[0]])
	}
	return fmt.Sprintf("%s 等 %d 个站源未返回结果", names[0], len(names))
}

// nativeSearchYear 从上线日期里取出年份，取不到返回空串。
func nativeSearchYear(drama nativeDrama) string {
	date := drama.OnlineDate
	for index := 0; index+4 <= len(date); index++ {
		year := date[index : index+4]
		if year[0] != '1' && year[0] != '2' {
			continue
		}
		digits := true
		for i := 0; i < 4; i++ {
			if year[i] < '0' || year[i] > '9' {
				digits = false
				break
			}
		}
		if digits {
			return year
		}
	}
	return ""
}

// nativeSearchKey 按「归一标题 + 年份」生成分组键。
// 标题归一复用 hongguoSearchText：NFKC 折叠、去掉空白与标点、统一小写。
func nativeSearchKey(drama nativeDrama) string {
	return hongguoSearchText(drama.Title) + "|" + nativeSearchYear(drama)
}

// nativeSearchCanMerge 判断两部剧是否可视为同一部（集数维度）。
// 集数缺失或相等直接合并；都有集数时，差异超过 25% 视为不同版本，避免把
// 「12 集短剧」和「80 集长剧」错误合成一条。
func nativeSearchCanMerge(base, other nativeDrama) bool {
	if base.Episodes == 0 || other.Episodes == 0 || base.Episodes == other.Episodes {
		return true
	}
	diff := base.Episodes - other.Episodes
	if diff < 0 {
		diff = -diff
	}
	larger := base.Episodes
	if other.Episodes > larger {
		larger = other.Episodes
	}
	return diff*4 <= larger
}

// nativeSearchScore 评估条目完整度，用于挑选每组的展示主条目。
func nativeSearchScore(drama nativeDrama) int {
	score := 0
	if drama.Cover != "" {
		score += 4
	}
	if drama.Episodes > 0 {
		score += 2
	}
	if drama.Description != "" {
		score += 1
	}
	if drama.OnlineDate != "" {
		score += 1
	}
	if drama.Category != "" {
		score += 1
	}
	return score
}

// nativeSearchMerge 把多源结果按标题/年份/集数归一去重。
// 每组挑一个完整度最高的作为主条目，其它站源副本放进 Peers，供切换播放源使用。
func nativeSearchMerge(items []nativeDrama) []nativeDrama {
	type group struct {
		primary nativeDrama
		peers   []nativeDrama
		score   int
	}
	groups := map[string][]*group{}
	var order []string
	for _, item := range items {
		if item.ID == "" || item.Title == "" {
			continue
		}
		key := nativeSearchKey(item)
		bucket := groups[key]
		var target *group
		for _, entry := range bucket {
			if nativeSearchCanMerge(entry.primary, item) {
				target = entry
				break
			}
		}
		if target == nil {
			// order 只记录键首次出现的顺序，避免同一键下多组时被重复遍历。
			_, known := groups[key]
			target = &group{primary: item, score: nativeSearchScore(item)}
			groups[key] = append(bucket, target)
			if !known {
				order = append(order, key)
			}
			continue
		}
		score := nativeSearchScore(item)
		if score > target.score {
			target.peers = append(target.peers, target.primary)
			target.primary, target.score = item, score
		} else {
			target.peers = append(target.peers, item)
		}
	}
	merged := make([]nativeDrama, 0, len(order))
	for _, key := range order {
		for _, entry := range groups[key] {
			primary := entry.primary
			seenID := map[string]bool{primary.ID: true}
			seenSource := map[string]bool{primary.Source: true}
			peers := make([]nativeDrama, 0, len(entry.peers))
			for _, peer := range entry.peers {
				// 同一站源只保留一个副本，避免同一部剧被重复罗列。
				if seenID[peer.ID] || seenSource[peer.Source] {
					continue
				}
				seenID[peer.ID], seenSource[peer.Source] = true, true
				peers = append(peers, peer)
			}
			sort.Slice(peers, func(i, j int) bool {
				if peers[i].Source != peers[j].Source {
					return duanjuSourceLabel(peers[i].Source) < duanjuSourceLabel(peers[j].Source)
				}
				return peers[i].Title < peers[j].Title
			})
			primary.Peers = peers
			merged = append(merged, primary)
		}
	}
	sort.SliceStable(merged, func(i, j int) bool {
		left, right := nativeSearchScore(merged[i]), nativeSearchScore(merged[j])
		if left != right {
			return left > right
		}
		if merged[i].Episodes != merged[j].Episodes {
			return merged[i].Episodes > merged[j].Episodes
		}
		return merged[i].Title < merged[j].Title
	})
	return merged
}
