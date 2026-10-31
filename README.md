> ⚠️ **免责声明**：本项目源码来自网上大名鼎鼎的**鱼佬**（原作者）。我只是把它拿来打包、测试着玩，方便自己用。

# 短剧视界 / 全剧视界

Flutter 多端独立短剧 / 影视应用

两版以编译选项区分：默认版（短剧视界）仅红果站源；`--all-sources` 全站源版（全剧视界）额外启用黄豆、剧果、野果、帝果、黄果视频 / 黄果 AI / 黄果旧版等站源。分发包前缀为 `duanjushijie` / `quanjushijie`。


## 使用

| 功能 | 操作 |
| --- | --- |
| 浏览 | 标题栏依次提供排序与筛选、榜单、多选下载和展开搜索。分类保持独立一行，内容区左右滑动切类；支持默认、名称、自然季号、上线日期、热度、播放量排序及连载状态筛选。列表下滑接近底部自动加载下一页，底部「加载更多」为兜底 |
| 搜索 | 红果合并官网与名称索引，输入停顿 300 毫秒显示最多 10 条联想；韩小圈、剧果、野果、鬼片调用各自的在线搜索并按分页继续加载。部分失败保留有效结果；最近 20 次搜索按用户保存 |
| 推荐 | 仅红果分类栏在「全部」后显示「推荐」，可切换真人剧、漫剧、AI 剧并分别记住位置 |
| 详情 | 普通点剧直接进入播放；下载和兜底入口保留详情页。详情页浏览封面、资料、追剧状态与可展开简介；底部固定「立即播放 / 继续播放」和下载入口。选集默认折叠，展开后每组 50 集，可切换范围、定位当前或输入集数跳转；宽屏与电视采用资料、选集分栏 |
| 追剧与历史 | 按想看 / 在看 / 已看 / 有更新筛选；红果已追剧发现新季后进入「有更新」。追剧和历史均可搜索。每个用户独立保存，每 5 秒及退出播放时记录真实进度 |
| 卡片与批量下载 | 卡片「更多」提供收藏、状态和下载选集；电脑右键，电视菜单键。发现页长按卡片或点标题栏「多选下载」，一次最多 50 部 |
| VIP | 全站源版浏览黄豆或全部站源时显示 VIP 图标，仅过滤黄豆；默认隐藏已确认的 VIP。未知、免费、VIP 分别保存，未知不会覆盖已知状态 |
| 手机播放 | 上滑下一集、下滑上一集；长按 350 毫秒临时 3 倍速。竖屏轻点播放 / 暂停，横屏轻点控制条，双击播放 / 暂停。视频下方为「选集 / 简介 / 下载」Tab |
| 画中画 | Android 手机 / 平板播放器接入画中画按钮；进入前隐藏自绘控制层与弹幕，小窗只保留画面。Android TV、Windows、iOS 暂未接入 |
| 画质增强 | 仅 Windows 桌面端显示并运行增强链路（关闭 / 自动 / 省电增强 / 清晰优先）；移动端与电视端隐藏 |
| 弹幕 | 红果在线播放默认开启；播放器侧边圆形「弹」字按钮可关闭、查看状态及失败重试。本地播放不加载弹幕 |
| 下载 | 播放页「下载」Tab 和详情页下载入口均可选择分集与画质；支持批量暂停、继续、重试、删除及清理任务但保留视频。「更新本剧」补新增或缺失分集 |
| 更多 | 站源管理（全站源版可逐个开关站源、自定义源管理）、直播、用户管理、设置与备份、界面模式、关于 |

## 新增能力

以下能力从 `guoapp3` 移植合并，构建脚本、平台工程与既有功能（sorani、luna_exo_player、follow_updater 等）保持原样。

| 能力 | 说明 |
| --- | --- |
| xbpq 自定义源 | 原生核心新增 `provider_xbpq*.go`（XBPQ/TVBox 规则解析：JSON / 模板 / 截取语法）与 `provider_maccms_*` / `provider_duanju_*` / `provider_custom_maccms.go`，支持自定义 MaccMS 站源目录、分类、详情与取流；源排序见 `source_sort.go` |
| 站源管理 | 站源显示（逐源开关）+ 自定义源管理（`custom_sources_screen.dart`，自定义源增删改查与导入导出），入口在「站源管理」页 |
| 直播 | `live_*.dart` 九文件（仓库 / 源 / 解析 / 回放 / 界面）+ 原生 `native/server/livestream.go` 服务端代发，入口在首页「更多→直播」 |
| 播放器去广告 | `app_adfilter.go` 在原生回放链路（`app_stream.go`）内做 m3u8 广告段清洗；播放器侧边新增「去广告」开关，经 `core_bridge` → `playback_loader` / `playback_preloader` 贯穿到原生 |
| 本地服务版 | `native/server`（Go HTTP 服务 + 内嵌 web 前端 hls / flv / artplayer）+ `scripts/build_web.py`，构建出可在浏览器运行的独立服务版，随版本保留 `duanjushijie` / `quanjushijie` 两版 |

构建本地服务版：

~~~sh
python3 scripts/build_web.py                  # 两版都构建
python3 scripts/build_web.py --edition quanjushijie   # 仅全剧视界版
~~~

## 站源与可用性

| 站源 | 浏览与播放 | 搜索 |
| --- | --- | --- |
| 红果 | 真人剧、漫剧、AI 剧及分集 | 联网搜索与官网搜索联想 |
| 韩小圈 | MacCMS 模板：最新韩剧 / 韩国电影 / 韩国综艺 / 韩国动漫，多线路分集 | 站源在线搜索 |
| 鬼片 | MacCMS 站点：鬼片 / 电视剧 / 动漫，多线路分集 | RSS 最新条目标题匹配（站点搜索已停用） |
| 青空 | 番剧、剧场动画、特摄及分集 | 站源在线搜索 |
| 黄豆 | 列表、VIP 标记及分集 | 筛选已加载短剧 |
| 剧果 | 热门、最新、接口分类、详情及可播放分集；签名 Cookie 接入在线播放、预加载和下载 | 站源在线搜索，支持继续加载 |
| 野果 | 接口实际分类、目录、详情及真实分集；按分集重新取流，保留 H.264 / H.265 地址 | 站源在线分页搜索 |
| 帝果 | 网页分类、目录、详情及分集；vplayer 签名解析 | 站源在线分页搜索 |
| 黄果视频 / 黄果 AI / 黄果旧版 | 列表、分类、详情及分集 | 筛选已加载短剧 |

站源可用性、清晰度和区域限制取决于源站及网络；应用不解除源站 VIP 或其他授权限制。

## 安装包与平台状态

| 平台 | 包与状态 |
| --- | --- |
| Android 7.0+ | 三架构（arm64-v8a / armeabi-v7a / x86_64）APK；同一签名可覆盖升级，旧证书版本需卸载重装 |
| Windows 10/11 x64 | 完整 ZIP 解压后运行 `duanjushijie.exe` / `quanjushijie.exe`，保留所有 DLL 与 `data`；局域网原生发现依赖 Windows 10 1903+。媒体层使用打过补丁的本地 `packages/media_kit_video`，修复切换视频或清晰度时的闪退（见下节） |
| iOS 15.1+ | 已加入工程、Go 核心链接、媒体依赖、文件管理与构建脚本；iOS 播放页禁用 media_kit_video 硬件纹理加速以规避 libmpv 渲染崩溃；`CFBundleDisplayName` 使用英文 `DuanJuShijie` / `QuanJuShijie` 以通过 iloader 等侧载工具注册 App ID 时的名称校验，中文名写入 `zh-Hans.lproj/InfoPlist.strings`（开发快照，未经 Xcode 构建验证）；待 Xcode 构建与真机验收，无已签名 IPA |
| macOS 12+ | `scripts/build_macos.py` 出通用 dmg（arm64 + x86_64，Apple 芯片与 Intel 都原生运行）。`macos/` 工程目录不入库，首次构建由 `flutter create --platforms=macos` 生成后再按变体改写 `PRODUCT_NAME` / bundle id；Go 核心编成通用 `libduanju_core.dylib` 放进 `Contents/Frameworks`，Dart 侧按路径 `dlopen`；关闭 App Sandbox（否则 release 包连不上任何站源）；ad-hoc 签名，未公证 |

`INSTALL_FAILED_NO_MATCHING_ABIS` 表示 APK 与设备架构不匹配，请更换对应架构安装包。

### GitHub Actions

工作流只能手动触发，推送和 PR 不再自动运行检查。

发布流程：提升 `pubspec.yaml` 的 `version`，在 `CHANGELOG.md` 写好同名版本一节（标题如 `## 0.2.72+79`，会写入 Release 说明），推送到 `main` / `master`，再在 Actions 中手动运行 **Build app packages**。运行前有三个开关：

| 开关 | 默认 | 作用 |
| --- | --- | --- |
| `with_ios` | 关 | 要不要出 iOS 包。macOS runner 按 10 倍分钟计费，Free 额度一个月只够跑一次，所以默认不出；Android / Windows / 网页版不受影响 |
| `with_macos` | 关 | 要不要出 macOS dmg。同样走 macOS runner、同样 10 倍计费，所以单独一个开关，需要时再勾 |
| `full_variants` | 关 | iOS / macOS 出几个版本。不勾只出全剧视界（`--all-sources`），勾上再补短剧视界，省一半 macOS 计费分钟 |
| `republish` | 开 | 同一版本已有 Release / tag 时覆盖重发；取消勾选则直接失败，不进入构建 |

然后构建四个平台的两版（默认与 `--all-sources`）：

| 产物 | 内容 |
| --- | --- |
| `*-android` | 三种架构 APK，必须使用发布证书签名并通过指纹校验 |
| `*-windows` | Windows x64 ZIP |
| `ios-unsigned` | arm64 iPhone 未签名 IPA，下载后自行签名安装（勾了 `with_ios` 才有） |
| `macos` | 通用 dmg（arm64 + x86_64），ad-hoc 签名，未公证（勾了 `with_macos` 才有） |
| `web` | `guoappweb.zip`，短剧视界 / 全剧视界 本地服务版合集 |

checks（格式、分析、测试不阻塞）、Android、Windows、网页版全部成功后才创建 Release；**iOS、macOS 跳过或失败只影响它们自己**，其余平台照常发布，Release 说明里会注明「本次未包含 iOS / macOS 包」或「只出了全剧视界」。重复发布时先删除同名 Release 与 tag 再按当前提交重建，删除失败则退化为覆盖同名资产。手动发布流程为开发快照，尚未完成首次运行验证。

Android 签名只使用仓库 Secrets：`ANDROID_KEYSTORE_BASE64`、`ANDROID_KEYSTORE_PASSWORD`、`ANDROID_KEY_ALIAS`、`ANDROID_KEY_PASSWORD`；同时需要配置仓库变量 `ANDROID_CERT_SHA256`（证书 SHA-256，冒号和大小写不限）。缺少任意一项都会发布失败。CI 通过环境变量把签名传给 Gradle；本机构建使用 `android/key.properties`。`key.properties` 和证书文件不入库。

## Windows 稳定性

Windows 桌面端此前的闪退有三个互不相干的根因，均已修复。

**1. 播几个视频就闪退（媒体引擎的 C++ 并发缺陷）**

`media_kit_video` 2.0.1 的 Windows 插件里，渲染线程会按 `texture_id_` 取纹理，而 `Resize()`（每次视频分辨率变化都会触发——换集、切清晰度都算）会先清空 `texture_id_`、注销旧纹理、删除表项，之后才注册新纹理并重新赋值。句柄与纹理表不是原子更新，渲染线程读到中间态时 `std::unordered_map::at()` 抛 `std::out_of_range`，异常逃出工作线程即 `std::terminate`，进程被系统 fastfail 杀掉。因此这类闪退**既不产生 WER 转储、也没有崩溃日志**。

上游到 2.0.1 为止没有修复，故把该插件本地化到 `packages/media_kit_video`，用 `dependency_overrides` 覆盖，**只改 Windows C++**（Dart 与其它平台代码与上游一致）：

- 工作线程任务包 `try/catch`，异常只丢一帧，不再 terminate 进程
- 渲染与尺寸检查改为锁内 `find()`，查不到纹理就跳过本帧，并整体 `try/catch`
- `Resize()` 改为**先入表、再发布 `texture_id_`**，彻底消除中间态可见窗口
- 析构保证等待信号一定被唤醒（否则会永久阻塞管理器互斥锁，之后所有播放都无法创建）

改动均标记 `[duanju patch]`，原因与升级注意事项见 `packages/media_kit_video/DUANJU_PATCH.md`。**升级该依赖时必须重打补丁**，否则闪退复现。

**2. 双击后直接闪退、连启动界面都没有**

原生崩溃日志重定向曾把 `os.File.Fd()` 的返回值当作 C 运行时文件描述符传给 `_dup2`，但 Windows 上它是系统句柄（数值远大于文件描述符表范围），触发运行时参数校验后 `__fastfail` 结束进程。发生在初始化第一步，所以窗口来不及创建。已改为纯 Go 实现（Windows 用 `SetStdHandle` 改 `STD_ERROR_HANDLE`），不再触碰 C 运行时。**Windows 上任何 C 调用都不能接收 `Fd()` 的返回值。**

**3. 站源配置并发读写**

原生核心中站源基地址的读写曾缺少互斥保护，并发 map 读写属于不可恢复错误，运行时直接 `exit`，表现为静默退出。已加锁。

崩溃留痕位置：

| 文件 | 内容 |
| --- | --- |
| `<数据目录>/logs/app.log` | 应用运行日志（Windows 在 `%APPDATA%\全剧视界\全剧视界\logs`） |
| `<数据目录>/logs/native_stderr.log` | 原生层致命错误堆栈（仅 Windows 有效） |
| `<数据目录>/crash.log` | Dart 侧未捕获异常 |

若 `native_stderr.log` 一直是 0 字节，先确认它是个可写文件：被设成目录或只读时重定向会失败跳过，且不会有任何提示。

## 开发与构建

Flutter `3.47.4`、Dart `3.12+`、Go `1.24.1+`、Python `3.10+`。
Android 需要 JDK 17、SDK 36、NDK `28.2.13676358`；
Windows 需要 Visual Studio C++ 桌面组件及 MinGW-w64 x64；
iOS 需要 macOS、完整 Xcode 和 CocoaPods；
macOS 需要 macOS 12+、Xcode Command Line Tools、CocoaPods（`brew install cocoapods`），dmg 排版额外建议 `pip install dmgbuild Pillow`（缺了只是退回普通 dmg）。

构建脚本对子进程默认设置 `GOPROXY=https://goproxy.cn,direct`、`GOSUMDB=off`，同名环境变量可覆盖。

~~~sh
python3 scripts/build_android.py                 # 短剧视界
python3 scripts/build_android.py --all-sources   # 全剧视界
python3 scripts/build_android.py --abi arm64-v8a
python scripts/build_windows.py [--all-sources]
~~~

~~~sh
python3 scripts/build_ios.py
python3 scripts/build_ios.py --all-sources
python3 scripts/build_ios.py --core-only [--simulator]
python3 scripts/build_ios.py --export-options /path/to/ExportOptions.plist
~~~

~~~sh
python3 scripts/build_macos.py                          # 短剧视界
python3 scripts/build_macos.py --all-sources            # 全剧视界
python3 scripts/build_macos.py --all-sources --cn-mirrors
~~~

产物在 `dist/android`、`dist/windows`、`dist/ios`、`dist/macos`，短剧视界版以 `duanjushijie-` 开头，全剧视界版以 `quanjushijie-` 开头。

首次 Android 调试先编译对应架构核心，再运行：

~~~sh
python3 scripts/build_native.py --platform android --abi arm64-v8a
flutter pub get --enforce-lockfile
flutter run
~~~

调试全站源版：先给 `build_native.py` 加 `--all-sources`，再 `flutter run --dart-define=ALL_SOURCES=true`；iOS 对应 `build_ios.py --core-only --all-sources`。脚本会同步设置 Dart 常量和 Go 编译参数，应用启动时校验二者一致，避免混装原生库。

播放器使用 [media_kit](https://github.com/media-kit/media-kit) / libmpv，合并和导出使用 [FFmpegKit min-gpl](https://github.com/sk3llo/ffmpeg_kit_flutter)（含 GPL 媒体组件）。FFmpegKit 不参与正常播放或下载的转码。Windows 上 `media_kit_video` 用 `packages/media_kit_video` 的本地补丁副本覆盖（修 2.0.1 的渲染线程崩溃，见「Windows 稳定性」）。

### 集中检查

~~~sh
dart format --output=none --set-exit-if-changed lib test
dart analyze --fatal-infos lib test
flutter test --dart-define=DISABLE_REMOTE_IMAGES=true
flutter test --dart-define=DISABLE_REMOTE_IMAGES=true --dart-define=ALL_SOURCES=true
cd native
go test -race ./...
go test -race -ldflags="-X duanjuapp/native/core.buildAllSources=true" ./...
~~~

## 目录结构

| 目录 | 内容 |
| --- | --- |
| `lib` | 页面、播放器、本地用户、FFI、下载和媒体处理 |
| `native/core`、`native/bridge` | 独立站源核心、缓存、下载、目录迁移及 C ABI |
| `native/server` | 本地服务版（Go HTTP 服务 + 内嵌 web 前端，含直播代发），由 `scripts/build_web.py` 构建 |
| `android`、`windows`、`ios` | 平台工程与必要资源 |
| `macos` | 不入库：首次执行 `build_macos.py` 时由 `flutter create --platforms=macos` 生成，再按变体重写工程设置 |
| `assets/video_enhancement`、`packages/media_kit_libs_windows_video`、`packages/media_kit_video` | 增强 Shader 与许可、固定 Windows 媒体依赖插件、打过 Windows 补丁的 `media_kit_video`（见「Windows 稳定性」） |
| `scripts`、`.github/workflows` | 构建与发布 |
| `test` | 自动化测试 |

## 站源开发约定

站源是 Go 原生 provider（`native/core/provider_*.go`），不是运行期加载的 Python 源。新增站源需接入：`provider_huangguo.go`（常量 / 白名单 / `canonicalProviderSource` / `GetHuangguoChapters`）、`provider_media.go`（baseURL / host 反查 / 播放分派）、`app_categories.go`、`app_cover_metadata.go`、`app_runtime.go`（Config 字段 + 目录 / 搜索 / 详情分派 + 空页放行），并在 `lib/models.dart` 登记 `SourceSite`。

注意：Go RE2 正则**不支持 lookahead**。解析多线路播放列表时不能用 `(?=...)` 做分段，否则非捕获组会消耗下一段开头；应改用显式字符串截断（从容器标记之后查找下一段标记）。
