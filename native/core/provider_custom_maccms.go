package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// 自定义 maccms 源：用户可在应用内录入符合 MacCMS 模板的站点地址，
// 由本模块在运行时注册为动态站源，套用通用 maccms 解析规则完成列表展示与播放。
// 自定义源以 custom: 前缀作为唯一标识，避免与内置源冲突。

const customSourcePrefix = "custom:"

const customSourceMaxBytes = 256 * 1024

type customMaccmsSource struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Base      string    `json:"base"`
	Rule      string    `json:"rule,omitempty"` // XBPQ 爬虫规则 JSON（可选；为空时走模板家族识别）
	CreatedAt time.Time `json:"createdAt,omitempty"`
	Type      string    `json:"type,omitempty"` // 源类型：web=网页源 / json=JSON源 / 空=自动识别
}

type customMaccmsRegistry struct {
	mu      sync.RWMutex
	path    string
	sources map[string]customMaccmsSource
}

func newCustomMaccmsRegistry(directory string) *customMaccmsRegistry {
	return &customMaccmsRegistry{
		path:    filepath.Join(directory, "custom_sources.json"),
		sources: map[string]customMaccmsSource{},
	}
}

func (registry *customMaccmsRegistry) load() {
	body, err := os.ReadFile(registry.path)
	if err != nil || len(body) > customSourceMaxBytes {
		return
	}
	var records []customMaccmsSource
	if json.Unmarshal(body, &records) != nil {
		return
	}
	registry.mu.Lock()
	registry.sources = map[string]customMaccmsSource{}
	for _, record := range records {
		record.ID = strings.TrimSpace(record.ID)
		record.Base = strings.TrimRight(strings.TrimSpace(record.Base), "/")
		record.Name = strings.TrimSpace(record.Name)
		record.Type = normalizeCustomSourceType(record.Type)
		if !validCustomMaccmsRecord(record) {
			continue
		}
		if _, exists := registry.sources[record.ID]; exists {
			continue
		}
		registry.sources[record.ID] = record
	}
	registry.mu.Unlock()
}

func (registry *customMaccmsRegistry) save() error {
	// 调用方必须已持有 registry.mu 写锁（add/remove/replace 均在 Lock 保护下调用），
	// 此处绝不能再 RLock，否则同一 goroutine 持写锁再取读锁会死锁。
	records := make([]customMaccmsSource, 0, len(registry.sources))
	for _, record := range registry.sources {
		records = append(records, record)
	}
	body, err := json.Marshal(records)
	if err != nil || len(body) > customSourceMaxBytes {
		return err
	}
	return writeNativeCacheFile(registry.path, body)
}

func (registry *customMaccmsRegistry) all() []customMaccmsSource {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	records := make([]customMaccmsSource, 0, len(registry.sources))
	for _, record := range registry.sources {
		records = append(records, record)
	}
	return records
}

func (registry *customMaccmsRegistry) get(id string) (customMaccmsSource, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	record, found := registry.sources[id]
	return record, found
}

func (registry *customMaccmsRegistry) add(name, base, rule, kind string) (customMaccmsSource, error) {
	name = strings.TrimSpace(name)
	base = strings.TrimSpace(base)
	rule = strings.TrimSpace(rule)
	if len(rule) > customSourceMaxBytes {
		return customMaccmsSource{}, errors.New("规则内容过大（上限 256KB）")
	}
	// 录入的是 XBPQ 爬虫规则 JSON：主页地址与缺省名称都从规则里取。
	if rule != "" {
		parsed, ok := parseXBPQRule(rule)
		if !ok {
			return customMaccmsSource{}, errors.New("规则 JSON 无法识别：需包含「主页url」等字段")
		}
		home := parsed.homeURL()
		if home == "" {
			return customMaccmsSource{}, errors.New("规则 JSON 缺少有效的「主页url」")
		}
		if parsedBase := strings.TrimSpace(base); parsedBase == "" || !strings.HasPrefix(parsedBase, "http") {
			base = home
		}
		if name == "" {
			if parsedURL, err := url.Parse(home); err == nil {
				name = strings.TrimPrefix(parsedURL.Host, "www.")
			}
		}
	}
	if name == "" || len([]rune(name)) > 40 {
		return customMaccmsSource{}, errors.New("源名称需要 1 至 40 个字符")
	}
	base = strings.TrimRight(base, "/")
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return customMaccmsSource{}, errors.New("源网址需为 http 或 https 开头的完整地址")
	}
	if parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" {
		return customMaccmsSource{}, errors.New("源网址不能包含用户名、参数或锚点")
	}
	if !strings.Contains(parsed.Host, ".") {
		return customMaccmsSource{}, errors.New("源网址域名无效")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	baseKey := strings.ToLower(base)
	for _, record := range registry.sources {
		if strings.ToLower(record.Base) == baseKey {
			return customMaccmsSource{}, errors.New("该网址已存在，请直接编辑或更换")
		}
	}
	id := customMaccmsID(base)
	record := customMaccmsSource{ID: id, Name: name, Base: base, Rule: rule, CreatedAt: time.Now(), Type: normalizeCustomSourceType(kind)}
	registry.sources[id] = record
	if err := registry.save(); err != nil {
		delete(registry.sources, id)
		return customMaccmsSource{}, err
	}
	return record, nil
}

func (registry *customMaccmsRegistry) remove(id string) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, found := registry.sources[id]; !found {
		return errors.New("自定义源不存在")
	}
	delete(registry.sources, id)
	return registry.save()
}

// updateRule 只替换规则正文并落盘，不动名称/地址/类型。
// 供令牌自动续期等运行期规则修正使用（走 replace 会因 base 校验或 ID 重算失败）。
func (registry *customMaccmsRegistry) updateRule(id, rule string) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	id = strings.TrimSpace(id)
	record, found := registry.sources[id]
	if !found {
		return errors.New("自定义源不存在")
	}
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return errors.New("规则内容为空")
	}
	if len(rule) > customSourceMaxBytes {
		return errors.New("规则内容过大（上限 256KB）")
	}
	record.Rule = rule
	registry.sources[id] = record
	return registry.save()
}

func (registry *customMaccmsRegistry) replace(id, name, base, rule, kind string) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	id = strings.TrimSpace(id)
	existing, found := registry.sources[id]
	if !found {
		return errors.New("自定义源不存在")
	}
	name = strings.TrimSpace(name)
	rule = strings.TrimSpace(rule)
	if len(rule) > customSourceMaxBytes {
		return errors.New("规则内容过大（上限 256KB）")
	}
	if rule != "" {
		parsed, ok := parseXBPQRule(rule)
		if !ok {
			return errors.New("规则 JSON 无法识别：需包含「主页url」等字段")
		}
		if home := parsed.homeURL(); home != "" && !strings.HasPrefix(strings.TrimSpace(base), "http") {
			base = home
		}
	}
	if name == "" || len([]rune(name)) > 40 {
		return errors.New("源名称需要 1 至 40 个字符")
	}
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("源网址需为 http 或 https 开头的完整地址")
	}
	if parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" {
		return errors.New("源网址不能包含用户名、参数或锚点")
	}
	if !strings.Contains(parsed.Host, ".") {
		return errors.New("源网址域名无效")
	}
	baseKey := strings.ToLower(base)
	for otherID, other := range registry.sources {
		if otherID == id {
			continue
		}
		if strings.ToLower(other.Base) == baseKey {
			return errors.New("该网址已被其他源使用")
		}
	}
	newID := customMaccmsID(base)
	kind = normalizeCustomSourceType(kind)
	newType := existing.Type
	if kind != "" {
		newType = kind
	}
	record := customMaccmsSource{ID: newID, Name: name, Base: base, Rule: rule, CreatedAt: existing.CreatedAt, Type: newType}
	if newID != id {
		delete(registry.sources, id)
	}
	registry.sources[newID] = record
	return registry.save()
}

func validCustomMaccmsRecord(record customMaccmsSource) bool {
	if record.ID == "" || record.Name == "" || record.Base == "" {
		return false
	}
	if !strings.HasPrefix(record.ID, customSourcePrefix) {
		return false
	}
	parsed, err := url.Parse(record.Base)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func customMaccmsID(base string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimRight(base, "/"))))
	return customSourcePrefix + hex.EncodeToString(sum[:8])
}

func isCustomMaccmsSource(source string) bool {
	return strings.HasPrefix(canonicalProviderSource(source), customSourcePrefix)
}

// ---- 源类型（web / json / 自动）----
//
// 自定义源可在配置里显式声明源类型，跳过运行时探测直接走对应处理模块：
//   - "web"  ：网页源。目录/详情/搜索/取流都走 MacCMS 网页模板（或 XBPQ 规则），
//     不再探测标准 JSON API 端点（避免对纯网页站反复打 ac=videolist 探测）。
//   - "json" ：JSON 源。走 MacCMS 标准 JSON API 模块；API 不可用时不再回落到
//     网页模板猜测（网页猜测对 JSON 站无意义且拖慢加载），直接报「JSON 接口不可用」。
//   - 空 / "auto"：自动（默认）。保持现状——有 XBPQ 规则走规则，无规则时先探
//     标准 JSON API，探不到再猜网页模板家族。
//
// 类型只影响「走哪个模块」的取舍，不改变各模块内部行为；填错类型会得到
// 明确的「该源类型与站点不符」错误而非静默失败。

const (
	customSourceTypeWeb  = "web"
	customSourceTypeJSON = "json"
)

// normalizeCustomSourceType 规整源类型取值：小写、去空白，仅保留 web/json/auto/空。
func normalizeCustomSourceType(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case customSourceTypeWeb, customSourceTypeJSON, "auto", "":
		return strings.ToLower(strings.TrimSpace(kind))
	default:
		return ""
	}
}

// customSourceRecordOf 取自定义源在注册表里的记录（非自定义源返回 false）。
func customSourceRecordOf(source string) (customMaccmsSource, bool) {
	if !isCustomMaccmsSource(source) {
		return customMaccmsSource{}, false
	}
	engine := nativeEngineSnapshot()
	if engine == nil || engine.customRegistry == nil {
		return customMaccmsSource{}, false
	}
	return engine.customRegistry.get(canonicalProviderSource(source))
}

// customSourceTypeFor 返回某自定义源规整后的源类型（未配置返回 ""=自动）。
func customSourceTypeFor(source string) string {
	record, found := customSourceRecordOf(source)
	if !found {
		return ""
	}
	return normalizeCustomSourceType(record.Type)
}

// ---- 导入导出 ----
//
// 导出格式：{"format":"duanju-custom-sources","version":1,"sites":[{name,url,rule}]}
// 导入兼容：① 本格式；② 纯数组（对象或每行一个网址）；③ TVBox/CatVod 配置
// （sites/whitelist 数组，含 XBPQ 规则对象或 maccms api 地址）。爬虫 jar 条目
// 无法在 Go 核心里执行，导入时逐条给出跳过原因而不是整体失败。

const customSourceExportFormat = "duanju-custom-sources"

const customSourceImportLimit = 200

const customSourceImportMaxBytes = 8 << 20

type customSourceDraft struct {
	Name string
	Base string
	Rule string
	Kind string // 源类型：web / json / 空=自动
}

type customSourceImportResult struct {
	Added   []customMaccmsSource `json:"added"`
	Updated []customMaccmsSource `json:"updated"`
	Skipped []string             `json:"skipped"`
	Failed  []string             `json:"failed"`
}

// exportCustomMaccmsSources 序列化全部自定义源，附带 XBPQ 规则原文。
func (engine *nativeEngine) exportCustomMaccmsSources() (string, error) {
	records := engine.customMaccmsSources()
	sort.Slice(records, func(i, j int) bool {
		left, right := strings.ToLower(records[i].Name), strings.ToLower(records[j].Name)
		if left != right {
			return left < right
		}
		return records[i].ID < records[j].ID
	})
	type exportItem struct {
		Name string `json:"name"`
		URL  string `json:"url"`
		Rule any    `json:"rule,omitempty"`
		Type string `json:"type,omitempty"` // 源类型：web / json；空=自动识别
	}
	items := make([]exportItem, 0, len(records))
	for _, record := range records {
		items = append(items, exportItem{Name: record.Name, URL: record.Base, Rule: exportRuleValue(record.Rule), Type: record.Type})
	}
	payload := struct {
		Format  string       `json:"format"`
		Version int          `json:"version"`
		Count   int          `json:"count"`
		Sites   []exportItem `json:"sites"`
	}{Format: customSourceExportFormat, Version: 1, Count: len(items), Sites: items}
	// Use a dedicated Encoder with SetEscapeHTML(false) so that & < > stay
	// as literal characters instead of being mangled to \u0026 \u003c \u003e.
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(payload); err != nil {
		return "", err
	}
	// Encode appends a trailing newline; trim it for clean output.
	return strings.TrimRight(buf.String(), "\n"), nil
}

func customSourceExportFilename() string {
	return "duanju-custom-sources-" + time.Now().Format("2006-01-02") + ".json"
}

// exportRuleValue 把 XBPQ 规则原文在导出时尽量还原成嵌套 JSON 对象，
// 避免整段被当成字符串、满屏 \" 转义；解析失败则原样作为字符串导出。
// 导入侧 customSourceRuleFromMap 的 ext 分支对对象 / 字符串两种形态都兼容，
// 因此这样导出既能读得更干净，又保证 round-trip 不被破坏。
func exportRuleValue(rule string) any {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return ""
	}
	var decoded any
	if err := json.Unmarshal([]byte(rule), &decoded); err == nil {
		return decoded
	}
	return rule
}

// canonicalXBPQRule 把 XBPQ 规则规整成「键排序紧凑 JSON」，用于判断两条规则
// 是否等价（键序 / 空白 / 字符串转义差异不影响）。规则不是合法 JSON 时按原文比较，
// 使「新增编辑器存原文 → 导出成嵌套对象 → 再导入」能命中更新而非误报已存在。
func canonicalXBPQRule(rule string) string {
	trimmed := strings.TrimSpace(rule)
	if trimmed == "" {
		return ""
	}
	var decoded any
	if json.Unmarshal([]byte(trimmed), &decoded) != nil {
		return trimmed
	}
	body, err := json.Marshal(normalizeXBPQCanonical(decoded))
	if err != nil {
		return trimmed
	}
	return string(body)
}

// normalizeXBPQCanonical 递归把规则规整成键排序的 map / []，保证二次序列化一致。
func normalizeXBPQCanonical(node any) any {
	switch typed := node.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		result := make(map[string]any, len(typed))
		for _, key := range keys {
			result[key] = normalizeXBPQCanonical(typed[key])
		}
		return result
	case []any:
		for index := range typed {
			typed[index] = normalizeXBPQCanonical(typed[index])
		}
		return typed
	default:
		return node
	}
}

// customSourceIsRemoteURL 判断整段内容是否为「一行远程配置地址」。
// 带查询参数、或路径带配置文件后缀（.json/.txt/.bxtv 等）、或路径含 config 字样时判为配置地址；
// 站点根地址（如 https://example.com 或 https://example.com:5200）按单站导入处理，不去抓取。
var customSourceConfigExtensions = []string{".json", ".txt", ".bxtv", ".bxt", ".list", ".conf", ".ini", ".custom", ".cpm", ".xs", ".c"}

func customSourceIsRemoteURL(content string) bool {
	if strings.ContainsAny(content, "\n\r") {
		return false
	}
	if len(content) > 2048 {
		return false
	}
	parsed, err := url.Parse(content)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return false
	}
	if parsed.RawQuery != "" {
		return true
	}
	path := strings.ToLower(parsed.Path)
	if path == "" || path == "/" {
		return false
	}
	if strings.Contains(path, "config") || strings.Contains(path, "tvbox") {
		return true
	}
	for _, extension := range customSourceConfigExtensions {
		if strings.HasSuffix(path, extension) {
			return true
		}
	}
	return false
}

// fetchCustomSourcesRemote 拉取远程配置文件文本（15 秒超时、8MB 上限、GBK 兜底转码）。
func (engine *nativeEngine) fetchCustomSourcesRemote(address string) (string, error) {
	if engine == nil || engine.downloader == nil {
		return "", errors.New("核心尚未就绪，请稍后重试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	body, err := engine.downloader.duanjuDo(ctx, duanjuRequest{
		Method:  "GET",
		Address: address,
		Timeout: 15 * time.Second,
	})
	if err != nil {
		return "", fmt.Errorf("远程地址抓取失败：%v", err)
	}
	if len(body) == 0 {
		return "", errors.New("远程地址返回空内容")
	}
	if len(body) > customSourceImportMaxBytes {
		return "", errors.New("远程文件过大（上限 8MB）")
	}
	text := string(body)
	text = strings.TrimPrefix(text, "\ufeff")
	if !utf8.ValidString(text) {
		if decoded, _, decodeErr := transform.String(simplifiedchinese.GB18030.NewDecoder(), text); decodeErr == nil {
			text = decoded
		}
	}
	return text, nil
}

// resetCustomMaccmsSources 清空全部自定义源，恢复为只有内置源的状态。
func (engine *nativeEngine) resetCustomMaccmsSources() (int, error) {
	registry := engine.customRegistry
	registry.mu.Lock()
	defer registry.mu.Unlock()
	removed := len(registry.sources)
	registry.sources = map[string]customMaccmsSource{}
	if err := registry.save(); err != nil {
		return removed, err
	}
	return removed, nil
}

// importCustomMaccmsSources 解析任意受支持文本并逐条落库；单条失败不影响其余。
// content 若是一行远程地址（http/https），先抓取该地址的文本再解析。
func (engine *nativeEngine) importCustomMaccmsSources(content string) (customSourceImportResult, error) {
	result := customSourceImportResult{Added: []customMaccmsSource{}, Updated: []customMaccmsSource{}, Skipped: []string{}, Failed: []string{}}
	content = strings.TrimPrefix(strings.TrimSpace(content), "\ufeff")
	if content == "" {
		return result, errors.New("导入内容为空")
	}
	if customSourceIsRemoteURL(content) {
		fetched, err := engine.fetchCustomSourcesRemote(content)
		if err != nil {
			return result, err
		}
		content = fetched
	}
	if len(content) > customSourceImportMaxBytes {
		return result, errors.New("导入文件过大（上限 8MB）")
	}
	drafts, notes := customSourceDrafts(content)
	result.Skipped = append(result.Skipped, notes...)
	if len(drafts) == 0 {
		return result, errors.New("未能从文件中识别出任何站点")
	}
	if len(drafts) > customSourceImportLimit {
		result.Skipped = append(result.Skipped, fmt.Sprintf("超出单次导入上限 %d 个，其余已忽略", customSourceImportLimit))
		drafts = drafts[:customSourceImportLimit]
	}
	// 文件内部按网址先去重，避免同一次导入里互相撞「已存在」。
	seen := map[string]bool{}
	for _, draft := range drafts {
		key := strings.ToLower(strings.TrimRight(draft.Base, "/"))
		if draft.Rule != "" {
			// 规则源以「规则 + 名称」区分同域多站，键里带上名称。
			key = key + "|" + draft.Name + "|" + fmt.Sprint(len(draft.Rule))
		}
		if key == "|" || seen[key] {
			continue
		}
		seen[key] = true
		name := customSourceImportName(draft.Name, draft.Base)
		base := customSourceImportBase(draft.Base, draft.Rule)
		if base == "" {
			result.Failed = append(result.Failed, name+"：无法确定站点网址")
			continue
		}
		if existing, found := engine.customRegistry.get(customMaccmsID(base)); found && canonicalXBPQRule(existing.Rule) == canonicalXBPQRule(draft.Rule) {
			record, err := engine.updateCustomMaccmsSource(existing.ID, name, base, draft.Rule, draft.Kind)
			if err != nil {
				result.Failed = append(result.Failed, name+"："+err.Error())
				continue
			}
			result.Updated = append(result.Updated, record)
			continue
		}
		record, err := engine.addCustomMaccmsSource(name, base, draft.Rule, draft.Kind)
		if err != nil {
			message := err.Error()
			if strings.Contains(message, "已存在") {
				result.Skipped = append(result.Skipped, name+"：该网址已在自定义源中")
			} else {
				result.Failed = append(result.Failed, name+"："+message)
			}
			continue
		}
		result.Added = append(result.Added, record)
	}
	return result, nil
}

// customSourceImportName 规整名称：去掉表情符前后缀噪音，截到 40 字符，缺省用域名。
func customSourceImportName(name, base string) string {
	name = strings.TrimSpace(customXBPQCleanName(name))
	runes := []rune(name)
	if len(runes) > 40 {
		name = string(runes[:40])
	}
	if name != "" {
		return name
	}
	if parsed, err := url.Parse(base); err == nil && parsed.Host != "" {
		return strings.TrimPrefix(parsed.Host, "www.")
	}
	return "自定义源"
}

// customSourceImportBase 补齐协议；对 maccms api 地址收敛成站点根地址，
// 但对「加速链接+网址」拼接形态（路径里再嵌一个完整 http(s) 网址）作为整体保留，
// 不能只取 scheme://host 丢掉拼接部分，否则取流会指向错误的加速域名。
func customSourceImportBase(base, rule string) string {
	base = strings.TrimSpace(base)
	if rule != "" {
		if parsed, ok := parseXBPQRule(rule); ok {
			if home := parsed.homeURL(); home != "" {
				return home
			}
		}
	}
	if base == "" {
		return ""
	}
	if !strings.Contains(base, "://") {
		if strings.HasPrefix(base, "//") {
			base = "https:" + base
		} else if strings.Contains(base, "/api.php") || strings.Contains(base, "/provide/vod") {
			base = "https://" + base
		} else {
			base = "https://" + base
		}
	}
	// 加速链接拼接形态：https://加速域名/https://真实网址，必须整体保留。
	if isSplicedAcceleratorURL(base) {
		return strings.TrimRight(base, "/")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" {
		return ""
	}
	// MacCMS 采集端点（路径含 /api.php 或 /provide/vod，如 .../provide/vod/at/xml、.../at/json）
	// 整体保留完整 URL：运行期 maccmsAPIEndpointCandidates 会把「base 本身」排在候选最前
	// 直连探测，从而直接命中该采集接口（含 at/xml 的 XML 形态）。否则收敛成站点根会丢掉
	// 端点路径，at/xml 这种站点默认只返回 XML 的接口就探不到、误判为不可用。
	// 纯网页站点（路径无采集端点特征）仍收敛成站点根，保留原有网页模板兜底行为。
	if isMaccmsCollectionEndpoint(base) {
		return strings.TrimRight(base, "/")
	}
	return parsed.Scheme + "://" + parsed.Host
}

// isMaccmsCollectionEndpoint 判断 base 是否为 MacCMS 采集接口端点 URL：
// 路径里出现 /api.php 或 /provide/vod 即认（覆盖 at/xml、at/json、/from/m3u8 等子路径）。
func isMaccmsCollectionEndpoint(base string) bool {
	path := base
	if index := strings.Index(base, "?"); index >= 0 {
		path = base[:index]
	}
	return strings.Contains(path, "/api.php") || strings.Contains(path, "/provide/vod")
}

// isSplicedAcceleratorURL 判断是否为「加速链接 + 网址」拼接形态。
// 形如 https://cfkua.example.org/https://caiji.example.com：路径本身又是一个
// 完整 http(s) 网址，特征是字符串里出现第二个 ://。这类地址必须整体保留。
func isSplicedAcceleratorURL(base string) bool {
	if strings.Count(base, "://") < 2 {
		return false
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" {
		return false
	}
	rest := strings.TrimLeft(parsed.RawPath, "/")
	if rest == "" {
		rest = strings.TrimLeft(parsed.Path, "/")
	}
	return strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://")
}

var customSourceEmojiPattern = regexp.MustCompile(`[^\p{L}\p{N}\p{Han}\-_·. ]`)

func customXBPQCleanName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\t", " ")
	name = customSourceEmojiPattern.ReplaceAllString(name, "")
	name = strings.Join(strings.Fields(name), " ")
	// 去掉「[X]」「(1.2.3)」这类配置里常见的版本尾缀
	for {
		trimmed := strings.TrimSpace(name)
		if index := strings.LastIndexAny(trimmed, "(["); index > 0 && strings.IndexAny(trimmed, ")]") == len(trimmed)-1 {
			trimmed = strings.TrimSpace(trimmed[:index])
		}
		if trimmed == name {
			break
		}
		name = trimmed
	}
	return name
}

// customSourceDrafts 把任意文本解析成待导入条目；notes 为逐条跳过原因。
func customSourceDrafts(content string) ([]customSourceDraft, []string) {
	var notes []string
	first := content[0]
	if first != '{' && first != '[' {
		return customSourceLineDrafts(content), notes
	}
	var payload any
	if json.Unmarshal([]byte(content), &payload) != nil {
		// JSON 语法问题（注释、字符串内裸控制符、尾逗号）：修复后再试一次
		if repaired := repairJSONForImport(content); json.Unmarshal([]byte(repaired), &payload) != nil {
			if repaired := xbpqTrimTrailingComma(stripJSONComments(content)); json.Unmarshal([]byte(repaired), &payload) != nil {
				return nil, []string{"文件不是合法 JSON"}
			}
		}
	}
	switch typed := payload.(type) {
	case []any:
		drafts := customSourceMapDrafts(typed, &notes)
		return drafts, notes
	case map[string]any:
		// 单条规则 / 单条站点
		draft, note := customSourceDraftFromEntry(typed)
		if note != "" {
			notes = append(notes, note)
		}
		if draft != nil {
			return []customSourceDraft{*draft}, notes
		}
		// 容器形态：sites / source / data / list
		for _, key := range []string{"sites", "source", "sources", "list", "data", "whitelist", "items"} {
			if rows, found := typed[key].([]any); found && len(rows) > 0 {
				drafts := customSourceMapDrafts(rows, &notes)
				return drafts, notes
			}
		}
		if rule := customSourceRuleFromMap(typed); rule != "" {
			return []customSourceDraft{{Rule: rule, Base: customSourceMapBase(typed)}}, notes
		}
	}
	return nil, []string{"文件结构无法识别"}
}

// customSourceLineDrafts 处理纯文本清单：每行一个站点，支持「名称,网址」或裸网址。
func customSourceLineDrafts(content string) []customSourceDraft {
	var drafts []customSourceDraft
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.Trim(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "://") {
			continue
		}
		if strings.HasPrefix(line, "m3u") || strings.HasPrefix(strings.ToLower(line), "http://") && strings.Contains(line, ".m3u") {
			continue
		}
		name := ""
		if head, tail, found := strings.Cut(line, ","); found && looksLikeSiteAddress(tail) {
			name, line = strings.TrimSpace(head), strings.TrimSpace(tail)
		} else if head, tail, found := strings.Cut(line, " "); found && looksLikeSiteAddress(tail) {
			name, line = strings.TrimSpace(head), strings.TrimSpace(tail)
		}
		if !looksLikeSiteAddress(line) {
			continue
		}
		drafts = append(drafts, customSourceDraft{Name: name, Base: line})
	}
	return drafts
}

func looksLikeSiteAddress(candidate string) bool {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" || strings.ContainsAny(candidate, " \t\"'") {
		return false
	}
	if strings.HasPrefix(candidate, "http://") || strings.HasPrefix(candidate, "https://") || strings.HasPrefix(candidate, "//") {
		return true
	}
	host := candidate
	if index := strings.IndexAny(host, "/?"); index >= 0 {
		host = host[:index]
	}
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	return strings.Contains(host, ".") && !strings.Contains(host, "//") && strings.IndexFunc(host, unicode.IsSpace) < 0
}

func customSourceMapDrafts(rows []any, notes *[]string) []customSourceDraft {
	var drafts []customSourceDraft
	for _, row := range rows {
		entry, ok := row.(map[string]any)
		if !ok {
			if text, isString := row.(string); isString && looksLikeSiteAddress(text) {
				drafts = append(drafts, customSourceDraft{Base: text})
			}
			continue
		}
		draft, note := customSourceDraftFromEntry(entry)
		if note != "" && notes != nil {
			*notes = append(*notes, note)
		}
		if draft != nil {
			drafts = append(drafts, *draft)
		}
	}
	return drafts
}

// customSourceKindFromEntry 从导入条目里取「源类型」字符串（web / json，空=自动）。
// 键优先级：sourceType / source_type / 源类型 / type / kind。只有字符串取值生效；
// TVBox 风格条目里 type 常为数字（3=jar 爬虫标记），数字不在此识别，避免冲突。
func customSourceKindFromEntry(entry map[string]any) string {
	for _, key := range []string{"sourceType", "source_type", "源类型", "type", "kind"} {
		value, isString := entry[key].(string)
		if !isString {
			continue
		}
		if kind := normalizeCustomSourceType(value); kind != "" {
			return kind
		}
	}
	return ""
}

// customSourceDraftFromEntry 解析单条配置：优先规则，其次站点地址；爬虫条目给跳过原因。
func customSourceDraftFromEntry(entry map[string]any) (*customSourceDraft, string) {
	name := firstStringField(entry, "name", "名称", "站点名称", "title", "key", "cn")
	base := firstStringField(entry, "url", "base", "地址", "站点地址", "link", "api", "home", "主页url")
	kind := customSourceKindFromEntry(entry)
	// 显式规则字段
	if rule := firstStringField(entry, "rule", "ruleJson", "规则"); rule != "" {
		if parsed, ok := parseXBPQRule(rule); ok {
			candidate := customSourceMapBase(entry)
			if home := parsed.homeURL(); home != "" {
				candidate = home
			}
			return &customSourceDraft{Name: name, Base: candidate, Rule: rule, Kind: kind}, ""
		}
	}
	if rule := customSourceRuleFromMap(entry); rule != "" {
		candidate := customSourceMapBase(entry)
		if parsed, ok := parseXBPQRule(rule); ok {
			if home := parsed.homeURL(); home != "" {
				candidate = home
			}
		}
		return &customSourceDraft{Name: name, Base: candidate, Rule: rule, Kind: kind}, ""
	}
	// 爬虫 jar：Go 核心无法执行，明确跳过
	if spider := firstStringField(entry, "spider", "jar"); spider != "" || entryNumberField(entry, "type") == 3 {
		label := name
		if label == "" {
			label = "未命名条目"
		}
		return nil, label + "：爬虫(jar)规则无法在核心内执行，已跳过"
	}
	if base == "" {
		return nil, ""
	}
	return &customSourceDraft{Name: name, Base: base, Kind: kind}, ""
}

// customSourceMapBase 从条目里取站点根地址（含 maccms api 地址收敛）。
func customSourceMapBase(entry map[string]any) string {
	for _, key := range []string{"url", "base", "地址", "站点地址", "link", "api", "home", "主页url"} {
		if value := xbpqStringValue(entry[key]); strings.TrimSpace(value) != "" {
			if !strings.Contains(value, ";") || strings.Contains(value, "http") {
				return customSourceImportBase(value, "")
			}
		}
	}
	return ""
}

var customSourceRuleKeys = []string{"主页url", "首页url", "分类url", "分类Header", "数组", "播放数组", "搜索url", "线路数组", "请求"}

// customSourceRuleFromMap 判断对象本身（或它的 ext）是否为 XBPQ 规则。
func customSourceRuleFromMap(entry map[string]any) string {
	if customSourceHasRuleKeys(entry) {
		if body, err := json.Marshal(entry); err == nil {
			if _, ok := parseXBPQRule(string(body)); ok {
				return string(body)
			}
		}
	}
	switch ext := entry["ext"].(type) {
	case map[string]any:
		if customSourceHasRuleKeys(ext) {
			if body, err := json.Marshal(ext); err == nil {
				if _, ok := parseXBPQRule(string(body)); ok {
					return string(body)
				}
			}
		}
	case []any:
		for _, item := range ext {
			node, ok := item.(map[string]any)
			if !ok || !customSourceHasRuleKeys(node) {
				continue
			}
			if body, err := json.Marshal(node); err == nil {
				if _, ok := parseXBPQRule(string(body)); ok {
					return string(body)
				}
			}
		}
	case string:
		if parsed, ok := parseXBPQRule(ext); ok {
			_ = parsed
			return ext
		}
	}
	return ""
}

func customSourceHasRuleKeys(entry map[string]any) bool {
	hit := 0
	for _, key := range customSourceRuleKeys {
		if _, found := entry[xbpqNormalizeKey(key)]; found {
			hit++
		}
		if _, found := entry[key]; found {
			hit++
		}
	}
	return hit >= 2
}

func firstStringField(entry map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := xbpqStringValue(entry[key]); strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func entryNumberField(entry map[string]any, key string) int {
	value := entry[key]
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case string:
		if number, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return number
		}
	}
	return -1
}

// repairJSONForImport 把 TVBox 风格的「JS 味」配置修成合法 JSON：
// 字符串外的 // 与 /* */ 注释删除；字符串内的裸制表符/换行/回车转义；
// 尾逗号清理。真实 TVBox 源里三种情况都常见，严格 json.Unmarshal 会全部拒绝。
func repairJSONForImport(text string) string {
	var builder strings.Builder
	builder.Grow(len(text))
	inString := false
	escaped := false
	for index := 0; index < len(text); {
		char := text[index]
		if inString {
			switch {
			case escaped:
				// 上一字符是反斜杠：合法转义原样保留，非法转义（如 \'、\uXxxx）补成双反斜杠
				switch char {
				case '"', '\\', '/', 'b', 'f', 'n', 'r', 't', 'u':
					builder.WriteByte(char)
				default:
					builder.WriteString(`\`)
					builder.WriteByte(char)
				}
				escaped = false
			case char == '\\':
				builder.WriteByte(char)
				escaped = true
			case char == '\\':
				builder.WriteByte(char)
				escaped = true
			case char == '"':
				builder.WriteByte(char)
				inString = false
			case char == '\t':
				builder.WriteString(`\t`)
			case char == '\n':
				builder.WriteString(`\n`)
			case char == '\r':
				builder.WriteString(`\r`)
			default:
				builder.WriteByte(char)
			}
			index++
			continue
		}
		switch char {
		case '"':
			builder.WriteByte(char)
			inString = true
			index++
		case '/':
			if index+1 < len(text) && text[index+1] == '/' {
				for index < len(text) && text[index] != '\n' {
					index++
				}
			} else if index+1 < len(text) && text[index+1] == '*' {
				index += 2
				for index+1 < len(text) && !(text[index] == '*' && text[index+1] == '/') {
					if text[index] == '\n' {
						builder.WriteByte('\n')
					}
					index++
				}
				if index+1 < len(text) {
					index += 2
				} else {
					index = len(text)
				}
			} else {
				builder.WriteByte(char)
				index++
			}
		default:
			builder.WriteByte(char)
			index++
		}
	}
	return xbpqTrimTrailingComma(builder.String())
}

// stripJSONComments 去掉 // 行注释，便于导入手写配置。
var customSourceCommentPattern = regexp.MustCompile(`(?m)^\s*//[^\n]*$`)

func stripJSONComments(text string) string {
	return customSourceCommentPattern.ReplaceAllString(text, "")
}

func (engine *nativeEngine) customMaccmsSources() []customMaccmsSource {
	records := engine.customRegistry.all()
	sort.SliceStable(records, func(i, j int) bool {
		return CompareSourceNames(records[i].Name, records[j].Name) < 0
	})
	return records
}

func (engine *nativeEngine) addCustomMaccmsSource(name, base, rule, kind string) (customMaccmsSource, error) {
	record, err := engine.customRegistry.add(name, base, rule, kind)
	if err == nil {
		engine.registerCustomBrowserHost(record.Base)
	}
	return record, err
}

func (engine *nativeEngine) removeCustomMaccmsSource(id string) error {
	return engine.customRegistry.remove(id)
}

// registerCustomBrowserHost 把自定义源 host 加入浏览器指纹传输白名单。
// 自定义源（含 Cloudflare 前缀加速源）默认走标准 Go 客户端，缺 TLS/HTTP2 浏览器指纹
// 与 Cookie 会话，容易被 CF bot 管理拦在 managed challenge 之外。登记后该 host 的
// 目录/页面请求改走 Chrome 150 指纹传输，与内置 Browser 源一致，并复用源站 Set-Cookie
// 下发的 cf_clearance，从而提升前缀加速源（cfkua.wokaotianshi.eu.org 这类）可用性。
func (engine *nativeEngine) registerCustomBrowserHost(base string) {
	if engine == nil || engine.downloader == nil || engine.downloader.browserTransport == nil {
		return
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" {
		return
	}
	engine.downloader.browserTransport.registerHost(parsed.Hostname())
}

func (engine *nativeEngine) updateCustomMaccmsSource(id, name, base, rule, kind string) (customMaccmsSource, error) {
	if err := engine.customRegistry.replace(id, name, base, rule, kind); err != nil {
		return customMaccmsSource{}, err
	}
	record, found := engine.customRegistry.get(customMaccmsID(strings.TrimRight(strings.TrimSpace(base), "/")))
	if !found {
		record, found = engine.customRegistry.get(id)
		if !found {
			return customMaccmsSource{}, errors.New("自定义源不存在")
		}
	}
	engine.registerCustomBrowserHost(record.Base)
	return record, nil
}

// isCustomMaccmsSourceKnown 判断源是否已在指定引擎的注册表里。
// 引擎创建阶段（如 loadSourceRecords）全局快照尚未就绪，必须走实例自身的注册表。
func (engine *nativeEngine) isCustomMaccmsSourceKnown(source string) bool {
	if !isCustomMaccmsSource(source) || engine == nil || engine.customRegistry == nil {
		return false
	}
	_, found := engine.customRegistry.get(canonicalProviderSource(source))
	return found
}

// resolveCustomMaccmsSource 供站源注册表回退，返回指定自定义源的规格。
func customMaccmsSpecFor(id string) (duanjuSourceSpec, bool) {
	engine := nativeEngineSnapshot()
	if engine == nil {
		return duanjuSourceSpec{}, false
	}
	record, found := engine.customRegistry.get(id)
	if !found {
		return duanjuSourceSpec{}, false
	}
	return duanjuSourceSpec{ID: record.ID, Name: record.Name, Base: record.Base, Kind: "maccms", Searcher: true, Paged: true}, true
}

// nativeEngineSnapshot 读取全局引擎实例，仅用于注册表查询。
func nativeEngineSnapshot() *nativeEngine {
	nativeState.Lock()
	defer nativeState.Unlock()
	return nativeState.engine
}
