package core

import "errors"

var buildAllSources = "false"

var errNativeBuildSource = errors.New("当前版本不包含此站源")

func nativeSourceAvailable(source string) bool {
	source = canonicalProviderSource(source)
	if source == sourceHongguo {
		return true
	}
	// 番茄是内置站源：目录/详情/取流由本机番茄内容引擎承担，与 buildAllSources
	// 无关，两种构建（全剧视界 / 短剧视界）下都应常驻可用。
	if source == sourceFanqie {
		return true
	}
	if isCustomMaccmsSource(source) {
		return isHuangguoProviderSource(source)
	}
	return buildAllSources == "true" && isHuangguoProviderSource(source)
}

func nativeDramaAvailable(drama nativeDrama) bool {
	source, _, valid := splitProviderDramaID(drama.ID)
	return valid && nativeSourceAvailable(source) &&
		(drama.Source == "" || canonicalProviderSource(drama.Source) == source)
}

func nativeChapterAvailable(drama nativeDrama, chapter Chapter) bool {
	if !nativeDramaAvailable(drama) {
		return false
	}
	source, _, _ := splitProviderDramaID(drama.ID)
	if chapter.Source != "" && canonicalProviderSource(chapter.Source) != source {
		return false
	}
	if chapterSource, _, valid := splitProviderDramaID(chapter.ID); valid && chapterSource != source {
		return false
	}
	return true
}

func nativeDownloadAvailable(job nativeDownloadJob) bool {
	return nativeChapterAvailable(job.Drama, job.Chapter)
}

func nativeAuthorizeInput(input nativeInput) error {
	switch input.Action {
	case "recommendations", "cachedRecommendations", "suggestions", "danmaku":
		if !nativeSourceAvailable(sourceHongguo) {
			return errNativeBuildSource
		}
	case "rankings":
		board, found := findRankingBoard(input.Board)
		if !found || !nativeSourceAvailable(board.Source) {
			return errNativeBuildSource
		}
	case "catalog", "cached", "categories", "sourceStatus", "sourceJob", "cancelSourceJob":
		if !nativeSourceAvailable(input.Source) {
			return errNativeBuildSource
		}
		if input.Action == "sourceJob" && input.Drama.ID != "" &&
			(!nativeDramaAvailable(input.Drama) || sourceFromDramaID(input.Drama.ID) != canonicalProviderSource(input.Source)) {
			return errNativeBuildSource
		}
	case "cover", "prepareCover", "detail", "metadata", "followCheck", "resolve", "preload", "prepareHandoff", "enqueueDownloads", "localPlayback":
		if !nativeDramaAvailable(input.Drama) {
			return errNativeBuildSource
		}
		if (input.Action == "resolve" || input.Action == "preload" || input.Action == "prepareHandoff") && !nativeChapterAvailable(input.Drama, input.Chapter) {
			return errNativeBuildSource
		}
	}
	return nil
}
