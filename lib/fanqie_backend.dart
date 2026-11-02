import 'dart:async';
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
      if (Platform.isAndroid || Platform.isIOS) {
        await _startInProcess(runtimeDir, timeout);
      } else {
        await _startAsProcess(runtimeDir, timeout);
      }
    } catch (error) {
      DiaryService.add('[fanqie] 内容引擎不可用: $error');
    }
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
      return;
    }
    final executable = await _locateEngine();
    if (executable == null) {
      DiaryService.add('[fanqie] 未找到 $_engineFileName；'
          '番茄源仍会显示，播放时会提示引擎未就绪');
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
      DiaryService.add('[fanqie] 内容引擎就绪 (127.0.0.1:$port)');
    } else {
      DiaryService.add('[fanqie] 内容引擎启动超时，后台继续等待');
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

  static Future<void> _startInProcess(String runtimeDir, Duration timeout) async {
    // Android 走随 APK 打包的 .so；iOS 的符号由静态库链进 Runner，
    // 因此用 process() 在本进程里查。
    final library = Platform.isAndroid
        ? DynamicLibrary.open('libfanqie_shim.so')
        : DynamicLibrary.process();
    final init = library.lookupFunction<
        Int32 Function(
            Pointer<Utf8>, Pointer<Utf8>, Pointer<Utf8>, Pointer<Utf8>, Uint16),
        int Function(Pointer<Utf8>, Pointer<Utf8>, Pointer<Utf8>,
            Pointer<Utf8>, int)>('fanqie_init');
    final status =
        library.lookupFunction<Int32 Function(), int Function()>('fanqie_status');
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
      DiaryService.add('[fanqie] 引擎启动调用失败: $result');
      return;
    }
    final deadline = DateTime.now().add(timeout);
    while (DateTime.now().isBefore(deadline)) {
      final state = status();
      if (state == 1) {
        DiaryService.add('[fanqie] 内容引擎就绪 (127.0.0.1:$port)');
        return;
      }
      if (state == -1) {
        DiaryService.add('[fanqie] 内容引擎启动失败');
        return;
      }
      await Future<void>.delayed(const Duration(milliseconds: 250));
    }
    DiaryService.add('[fanqie] 内容引擎等待就绪超时，后台继续启动');
  }
}
