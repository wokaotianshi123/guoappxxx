package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"duanjuapp/native/core"
)

// 源显示列表：被隐藏的站源不出现在首页站源下拉框里。
// 与 Flutter 端的 hiddenSites 语义一致——隐藏只影响首页显示，
// 自定义源记录、已缓存内容与播放都不受影响。
const visibilityFile = "source-visibility.json"

// sourceVisibilityRevision 标记源显示列表的结构版本。
// 新增内置源时递增，并在 loadVisibility 里把该版本新增的隐藏源补进旧存档，
// 保证升级后的老用户也默认看不到新站源，同时不覆盖用户之后手动开启的选择。
const sourceVisibilityRevision = 2

// revisionHiddenSources 是 revision 2 新增、默认隐藏的内置源。
var revisionHiddenSources = []string{"xchina", "theporny"}

// 从未保存过源显示列表时的默认隐藏集，与 App 端
// SourceSite.defaultHiddenSiteIds 保持一致：
// 默认可见 = 红果、韩小圈、鬼片、A123、爱看机器人，其余内置源默认隐藏。
var defaultHiddenSources = map[string]bool{
	"huangdou": true, "huangju": true, "yeguo": true, "dsd": true,
	"xchina": true, "theporny": true,
	"huangguo-video": true, "huangguoai": true, "cloudfront": true,
	"yaguo": true, "maoguo": true, "fanguo": true, "guanguo": true,
	"heguo": true, "xingguo": true, "huaguo": true, "niuguo": true,
	"wangguo": true, "faguo": true, "wuguo": true,
}

var (
	visibilityMu     sync.Mutex
	visibilityPath   string
	visibilityLoaded bool
	hiddenSources    = map[string]bool{}
)

func setVisibilityDirectory(directory string) {
	visibilityMu.Lock()
	visibilityPath = filepath.Join(directory, visibilityFile)
	visibilityLoaded = false
	hiddenSources = map[string]bool{}
	visibilityMu.Unlock()
}

func loadVisibility() {
	if visibilityLoaded {
		return
	}
	if visibilityPath != "" {
		if raw, err := os.ReadFile(visibilityPath); err == nil {
			var parsed struct {
				Hidden   []string `json:"hidden"`
				Revision int      `json:"revision"`
			}
			if json.Unmarshal(raw, &parsed) == nil {
				hiddenSources = map[string]bool{}
				for _, id := range parsed.Hidden {
					if id = strings.TrimSpace(id); id != "" {
						hiddenSources[id] = true
					}
				}
				visibilityLoaded = true
				if parsed.Revision < sourceVisibilityRevision {
					for _, id := range revisionHiddenSources {
						hiddenSources[id] = true
					}
					_ = saveVisibility()
				}
				return
			}
		}
	}
	// 无存档（或存档不可读）时落到与 App 端一致的默认隐藏集。
	hiddenSources = map[string]bool{}
	for id := range defaultHiddenSources {
		hiddenSources[id] = true
	}
	visibilityLoaded = true
}

func saveVisibility() error {
	if visibilityPath == "" {
		return nil
	}
	ids := []string{}
	for id := range hiddenSources {
		ids = append(ids, id)
	}
	raw, err := json.MarshalIndent(map[string]any{"hidden": ids, "revision": sourceVisibilityRevision}, "", "  ")
	if err != nil {
		return err
	}
	tmp := visibilityPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, visibilityPath)
}

func isSourceHidden(source string) bool {
	visibilityMu.Lock()
	defer visibilityMu.Unlock()
	loadVisibility()
	return hiddenSources[source]
}

// handleSourceVisibility GET 返回全部可用站源与可见状态；
// POST {id, visible} 设置某个站源是否在首页显示。
func handleSourceVisibility(writer http.ResponseWriter, request *http.Request) {
	visibilityMu.Lock()
	defer visibilityMu.Unlock()
	loadVisibility()
	switch request.Method {
	case http.MethodGet:
		items := []map[string]any{}
		for _, source := range sourceNames {
			if sourceAvailable(source.ID) {
				items = append(items, map[string]any{
					"id": source.ID, "name": source.Name, "builtin": true,
					"visible": !hiddenSources[source.ID],
				})
			}
		}
		var customReply struct {
			OK   bool `json:"ok"`
			Data struct {
				Items []map[string]string `json:"items"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(core.NativeRequest(`{"action":"customSources"}`)), &customReply) == nil && customReply.OK {
			for _, custom := range customReply.Data.Items {
				if sourceAvailable(custom["id"]) {
					items = append(items, map[string]any{
						"id": custom["id"], "name": custom["name"], "builtin": false,
						"visible": !hiddenSources[custom["id"]],
					})
				}
			}
		}
		// 与 /api/sources 同款排序：先中文（拼音序）后字母序。
		sort.SliceStable(items, func(i, j int) bool {
			return core.CompareSourceNames(items[i]["name"].(string), items[j]["name"].(string)) < 0
		})
		writeJSON(writer, map[string]any{"items": items})
	case http.MethodPost:
		payload := struct {
			ID      string `json:"id"`
			Visible bool   `json:"visible"`
		}{}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || strings.TrimSpace(payload.ID) == "" {
			writeJSON(writer, map[string]any{"ok": false, "error": "缺少站源标识"})
			return
		}
		if payload.Visible {
			delete(hiddenSources, payload.ID)
		} else {
			hiddenSources[payload.ID] = true
		}
		if err := saveVisibility(); err != nil {
			writeJSON(writer, map[string]any{"ok": false, "error": "保存失败：" + err.Error()})
			return
		}
		writeJSON(writer, map[string]any{"ok": true})
	default:
		http.Error(writer, "只接受 GET/POST 请求", http.StatusMethodNotAllowed)
	}
}
