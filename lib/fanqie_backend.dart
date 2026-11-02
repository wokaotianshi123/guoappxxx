import 'dart:async';
import 'dart:convert';
import 'dart:ffi';
import 'dart:io';

import 'package:ffi/ffi.dart';
import 'package:flutter/services.dart' show rootBundle;
import 'package:path/path.dart' as path;
import 'package:path_provider/path_provider.dart';

import 'diary_service.dart';

/// 番茄内容引擎：本机的 Rust 核心（`fanqie_shim` crate）。
///
/// 引擎在本机 127.0.0.1:18921 提供 loopback REST 服务，负责番茄短剧上游的
/// 签名、设备注册与视频缓存；Go 核心里的「番茄」站源把请求转发给它。
///
/// 两种拉起方式，按平台选：
///
/// * **桌面端 / 本地服务版**：拉起随包的独立可执行 `fanqie_engine`，
///   走进程 + HTTP。不涉及动态库查找路径与代码签名，最省心。
/// * **Android / iOS**：进程内 FFI。Android 加载 `libfanqie_shim.so`；
///   iOS 用 `DynamicLibrary.process()` 找静态链进 Runner 的符号。
///
/// 任何一步失败都只写日志、不抛异常——番茄站源会给出「引擎未就绪」的
/// 明确提示，应用其余功能不受影响。若用户自行启动了一份监听 18921 的引擎，
/// 这里探测到端口已占用就直接复用，不会重复拉进程。
class FanqieBackend {
  FanqieBackend._();

  static const int port = 18921;
  static bool _started = false;
  static Process? _process;

  /// 引擎是否已确认在监听 127.0.0.1:port（端口能连上才算数）。
  static bool _ready = false;

  /// 起不来的具体原因，供界面提示与「设置 → 日记」排查。
  static String? _failure;

  /// 运行目录：失败时从这里捞引擎自己写的日志尾巴。
  static String? _runtimeDir;

  /// 桌面端可执行引擎的文件名。
  static String get _engineFileName =>
      Platform.isWindows ? 'fanqie_engine.exe' : 'fanqie_engine';

  /// 是否由本类负责拉起引擎。
  ///
  /// 所有平台都返回 true：某个平台缺产物时，等待就绪会超时并写一条可读日志，
  /// 界面上仍然是「引擎未就绪」而不是静默失败。
  static bool get supported => true;

  /// 启动引擎并等待就绪。超时不抛错。
  static Future<void> ensureStarted({
    Duration timeout = const Duration(seconds: 25),
  }) async {
    if (_started || !supported) return;
    _started = true;
    try {
      final runtimeDir = await _runtimeDirectory();
      _runtimeDir = runtimeDir;
      if (Platform.isAndroid || Platform.isIOS) {
        await _startInProcess(runtimeDir, timeout);
      } else {
        await _startAsProcess(runtimeDir, timeout);
      }
    } catch (error) {
      _fail('内容引擎不可用: $error');
    }
  }

  /// 给界面与错误文案用的引擎状态，一句话说清「为什么未就绪」。
  ///
  /// 安卓真机上「引擎未就绪」原来只有这一句，用户和开发者都无从下手；
  /// 现在至少能区分：动态库没打进包 / 符号对不上 / 初始化失败 / 还在起 / 超时。
  static String get diagnosis {
    if (_ready) return '引擎已就绪';
    final failure = _failure;
    if (failure != null) return failure;
    return _started ? '引擎仍在初始化' : '引擎未启动';
  }

  /// 等引擎就绪（或确定失败）。已就绪/已失败时立即返回。
  ///
  /// 首次进入番茄时引擎往往刚被拉起：安卓走进程内 FFI，要读配置、载设备池，
  /// 慢的时候好几秒。先等一会儿再发请求，比直接弹「未就绪」体面得多。
  static Future<bool> waitReady(Duration timeout) async {
    if (_ready) return true;
    if (!_started || _failure != null) return false;
    final deadline = DateTime.now().add(timeout);
    while (DateTime.now().isBefore(deadline)) {
      if (_ready) return true;
      if (_failure != null) return false;
      await Future<void>.delayed(const Duration(milliseconds: 200));
    }
    return _ready;
  }

  /// 现查一次引擎状态：端口能不能连、能不能真的应答 /health。
  ///
  /// [diagnosis] 只是「上次记录」：引擎中途挂掉时它会撒谎（报「已就绪」但请求
  /// 全失败）。出错时用这个现查，并把 /health 的结果带出来——端口在听但不应答，
  /// 和端口根本没起，是两种完全不同的病。
  static Future<String> describeNow() async {
    if (!_started) return '引擎未启动';
    if (!await _portOpen()) {
      return _failure ?? '引擎没有在 127.0.0.1:$port 监听（已退出或还没起来）';
    }
    final health = await _healthLine();
    // 不管 /health 是否 200 都带上日志尾巴：/health 只能证明引擎的 HTTP 层活着，
    // 某个 handler 里的 panic 它照样返回 ok。安卓上没有 logcat，这个文件是唯一现场。
    final tail = _logTail();
    final detail = tail == null ? health : '$health；引擎日志：$tail';
    final failure = _failure;
    if (failure != null) return '$failure；端口在听，$detail';
    return '端口在听，$detail';
  }

  /// 打一次引擎自己的健康检查，返回一句可直接写进日志/提示的话。
  static Future<String> _healthLine() async {
    final client = HttpClient()..connectionTimeout = const Duration(seconds: 2);
    try {
      final request =
          await client.getUrl(Uri.parse('http://127.0.0.1:$port/health'));
      final response = await request.close().timeout(const Duration(seconds: 3));
      final body =
          await response.transform(const Utf8Decoder(allowMalformed: true)).join();
      final trimmed = body.trim();
      final short = trimmed.length > 120 ? '${trimmed.substring(0, 120)}…' : trimmed;
      return '/health ${response.statusCode} $short';
    } catch (error) {
      return '/health 请求失败：$error';
    } finally {
      client.close(force: true);
    }
  }

  static void _markReady() {
    if (_ready) return;
    _ready = true;
    _failure = null;
    DiaryService.add('[fanqie] 内容引擎就绪 (127.0.0.1:$port)');
    // 端口在听不等于引擎真能干活，立刻打一次 /health 把结论留在日记里。
    unawaited(_healthLine().then((line) => DiaryService.add('[fanqie] 就绪探测 $line')));
  }

  /// 记录失败原因，并尽量带上引擎自己写的日志尾巴。
  ///
  /// 安卓上开发者拿不到 logcat（READ_LOGS 是系统权限），引擎的 panic / 初始化
  /// 失败只会落在运行目录的 `fanqie_engine.log` 里；把它最后一行并进错误信息，
  /// 用户截图或「设置 → 日记」就能直接看到现场。
  static String? _logTail() {
    final dir = _runtimeDir;
    if (dir == null) return null;
    try {
      final file = File(path.join(dir, 'fanqie_engine.log'));
      if (!file.existsSync()) return null;
      final lines = file
          .readAsStringSync()
          .split('\n')
          .map((line) => line.trim())
          .where((line) => line.isNotEmpty)
          .toList();
      if (lines.isEmpty) return null;
      // 引擎的一行日志可能很长（带堆栈），贴进提示前截断。
      final last = lines.last;
      return last.length > 160 ? '${last.substring(0, 160)}…' : last;
    } catch (_) {
      // 日志读不到不影响失败原因本身。
      return null;
    }
  }

  static void _fail(String reason) {
    final tail = _logTail();
    final text = tail == null ? reason : '$reason；引擎日志：$tail';
    _failure = text;
    DiaryService.add('[fanqie] $text');
  }

  /// 运行目录：引擎的 panic / 运行日志落在 `fanqie_engine.log`。
  static Future<String> _runtimeDirectory() async {
    Directory base;
    if (Platform.isAndroid) {
      base = await getExternalStorageDirectory() ??
          await getApplicationSupportDirectory();
    } else {
      base = await getApplicationSupportDirectory();
    }
    final runtimeDir = path.join(base.path, 'fanqie');
    await Directory(runtimeDir).create(recursive: true);
    for (final name in ['config.json', 'filter.json']) {
      final text = await rootBundle.loadString('assets/fanqie/$name');
      await File(path.join(runtimeDir, name)).writeAsString(text, flush: true);
    }
    return runtimeDir;
  }

  // ---------------------------------------------------------------------------
  // 桌面端 / 本地服务版：独立进程
  // ---------------------------------------------------------------------------

  /// 找出随包的引擎可执行文件。
  ///
  /// 打包时引擎就放在主程序同级目录（Windows 的 zip、macOS 的
  /// `Contents/MacOS`、Linux 的可执行目录都是这个位置），所以优先看
  /// `Platform.resolvedExecutable` 的父目录；其次才找应用数据目录，
  /// 便于用户自己丢一份引擎进去做调试。
  static Future<String?> _locateEngine() async {
    final candidates = <String>[
      path.join(path.dirname(Platform.resolvedExecutable), _engineFileName),
    ];
    if (Platform.isMacOS) {
      // 万一打包脚本把引擎放进了 Resources。
      candidates.add(path.join(path.dirname(Platform.resolvedExecutable),
          '..', 'Resources', _engineFileName));
    }
    final support = await getApplicationSupportDirectory();
    candidates.add(path.join(support.path, 'fanqie', _engineFileName));
    for (final candidate in candidates) {
      if (await File(candidate).exists()) return candidate;
    }
    return null;
  }

  static Future<void> _startAsProcess(String runtimeDir, Duration timeout) async {
    if (await _portOpen()) {
      DiaryService.add('[fanqie] 复用已在运行的引擎 (127.0.0.1:$port)');
      _markReady();
      return;
    }
    final executable = await _locateEngine();
    if (executable == null) {
      _fail('未找到 $_engineFileName，本次安装包未带番茄引擎');
      return;
    }
    final process = await Process.start(executable, [
      '--config', path.join(runtimeDir, 'config.json'),
      '--pool', path.join(runtimeDir, 'device_pool.json'),
      '--filter', path.join(runtimeDir, 'filter.json'),
      '--runtime', runtimeDir,
      '--port', '$port',
      // 宿主一退出就把 stdin 关掉，引擎读到 EOF 自己退出，不留孤儿进程占端口。
      '--exit-with-parent',
    ]);
    _process = process;
    // 只保留最后若干行日志，避免引擎长期运行时把内存吃满。
    process.stdout
        .transform(const SystemEncoding().decoder)
        .listen((line) => _logEngineOutput(line, 'out'))
        .onError((_) {});
    process.stderr
        .transform(const SystemEncoding().decoder)
        .listen((line) => _logEngineOutput(line, 'err'))
        .onError((_) {});
    unawaited(process.exitCode.then((code) {
      if (_process == process) _process = null;
      if (!_stopping) {
        DiaryService.add('[fanqie] 内容引擎退出，code=$code');
      }
    }));
    final ready = await _waitForPort(timeout);
    if (ready) {
      _markReady();
    } else {
      _fail('独立进程引擎启动超时（${timeout.inSeconds} 秒）');
    }
  }

  static bool _stopping = false;

  static void _logEngineOutput(String line, String kind) {
    final text = line.trim();
    if (text.isEmpty) return;
    // 引擎的普通日志量不小，只把错误与就绪行写进诊断日志。
    if (kind == 'err' || text.contains('ready')) {
      DiaryService.add('[fanqie:$kind] $text');
    }
  }

  /// 关闭引擎。桌面端应用退出前调用；Android/iOS 的进程内引擎随进程结束。
  static Future<void> shutdown() async {
    _stopping = true;
    final process = _process;
    _process = null;
    if (process == null) return;
    try {
      process.kill();
    } catch (_) {
      // 已经退出就不用管。
    }
  }

  static Future<bool> _portOpen() async {
    try {
      final socket = await Socket.connect('127.0.0.1', port,
          timeout: const Duration(milliseconds: 300));
      socket.destroy();
      return true;
    } catch (_) {
      return false;
    }
  }

  static Future<bool> _waitForPort(Duration timeout) async {
    final deadline = DateTime.now().add(timeout);
    while (DateTime.now().isBefore(deadline)) {
      if (await _portOpen()) return true;
      await Future<void>.delayed(const Duration(milliseconds: 250));
    }
    return false;
  }

  // ---------------------------------------------------------------------------
  // Android / iOS：进程内 FFI
  // ---------------------------------------------------------------------------

  static Future<void> _startInProcess(String runtimeDir, Duration timeout) async {    // 每一步单独兜错：安卓真机上「未就绪」可能是没打进 .so、符号对不上、
    // init 失败或者只是慢，笼统地报一句「不可用」等于什么都查不出来。
    DynamicLibrary library;
    DiaryService.add('[fanqie] 启动进程内引擎，宿主 ABI 推测 ${_processAbi()}');
    try {
      // Android 走随 APK 打包的 .so；iOS 的符号由静态库链进 Runner，
      // 因此用 process() 在本进程里查。
      library = Platform.isAndroid
          ? DynamicLibrary.open('libfanqie_shim.so')
          : DynamicLibrary.process();
    } catch (error) {
      _fail('无法加载引擎动态库 libfanqie_shim.so'
          '（宿主 ABI 推测 ${_processAbi()}；仓库里只随包放了 arm64-v8a 一份，'
          '若装的是 armeabi-v7a / x86_64 版 APK，请改用 arm64-v8a 那个包）：$error');
      return;
    }
    late final int Function(Pointer<Utf8>, Pointer<Utf8>, Pointer<Utf8>,
        Pointer<Utf8>, int) init;
    late final int Function() status;
    try {
      init = library.lookupFunction<
          Int32 Function(
              Pointer<Utf8>, Pointer<Utf8>, Pointer<Utf8>, Pointer<Utf8>, Uint16),
          int Function(Pointer<Utf8>, Pointer<Utf8>, Pointer<Utf8>,
              Pointer<Utf8>, int)>('fanqie_init');
      status = library
          .lookupFunction<Int32 Function(), int Function()>('fanqie_status');
    } catch (error) {
      _fail('引擎动态库缺少 fanqie_init / fanqie_status 符号：$error');
      return;
    }
    // 统一用 calloc 分配、calloc 释放，避免 malloc/calloc 混用。
    final configPath =
        path.join(runtimeDir, 'config.json').toNativeUtf8(allocator: calloc);
    final poolPath =
        path.join(runtimeDir, 'device_pool.json').toNativeUtf8(allocator: calloc);
    final filterPath =
        path.join(runtimeDir, 'filter.json').toNativeUtf8(allocator: calloc);
    final runtimePath = runtimeDir.toNativeUtf8(allocator: calloc);
    final result = init(configPath, poolPath, filterPath, runtimePath, port);
    calloc
      ..free(configPath)
      ..free(poolPath)
      ..free(filterPath)
      ..free(runtimePath);
    if (result != 0) {
      _fail('引擎启动调用失败（fanqie_init 返回 $result）');
      return;
    }
    final deadline = DateTime.now().add(timeout);
    while (DateTime.now().isBefore(deadline)) {
      // 端口能连上才算真就绪：fanqie_status 只在 init 返回后才置位，
      // 进程内启动慢的时候它会滞后于「服务已经在听」这个事实。
      if (await _portOpen()) {
        _markReady();
        return;
      }
      if (status() == -1) {
        _fail('引擎初始化失败');
        return;
      }
      await Future<void>.delayed(const Duration(milliseconds: 250));
    }
    _fail('引擎启动超时（${timeout.inSeconds} 秒）');
  }

  /// 从 Dart 的版本串里猜当前进程 ABI。
  ///
  /// 安卓上 `Platform.version` 会带 `android_arm64` / `android_arm` / `android_x64`
  /// 这样的尾巴。只是给提示用的：真正能不能加载由 dlopen 说了算，这里猜错也
  /// 不会改变任何逻辑。
  static String _processAbi() {
    final version = Platform.version.toLowerCase();
    if (version.contains('arm64')) return 'arm64-v8a';
    if (version.contains('x64') || version.contains('x86_64')) return 'x86_64';
    if (version.contains('arm')) return 'armeabi-v7a';
    return '未知';
  }
}
