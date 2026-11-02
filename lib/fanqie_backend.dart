import 'dart:async';
import 'dart:ffi';
import 'dart:io';

import 'package:ffi/ffi.dart';
import 'package:flutter/services.dart' show rootBundle;
import 'package:path/path.dart' as path;
import 'package:path_provider/path_provider.dart';

import 'diary_service.dart';

/// 番茄内容引擎：本机内嵌的 Rust 核心（libfanqie_shim）。
///
/// 引擎在本机 127.0.0.1:18921 提供 loopback REST 服务，负责番茄短剧上游的
/// 签名、设备注册与视频缓存；Go 核心里的「番茄」站源把请求转发给它。
/// 这里只负责：写出配置文件、通过 FFI 启动引擎、等待就绪。
///
/// 平台说明：目前只有 Android 产物内打包了 `libfanqie_shim.so`，因此仅 Android
/// 会拉起引擎；macOS / iOS / Windows / 本地服务器版没有该产物，站源仍在列表里
/// 显示，播放时会得到「番茄内容引擎未就绪」的明确提示。若在桌面端自行启动
/// 一份监听 127.0.0.1:18921 的番茄引擎，站源可直接使用，不需要改代码。
class FanqieBackend {
  FanqieBackend._();

  static const int port = 18921;
  static bool _started = false;

  /// 只有内置了引擎产物的平台才需要（且能够）自行拉起引擎。
  static bool get supported => Platform.isAndroid;

  /// 启动引擎并等待就绪。超时不抛错——站源请求会拿到明确的失败信息，
  /// 但应用其余功能不受影响。
  static Future<void> ensureStarted({
    Duration timeout = const Duration(seconds: 25),
  }) async {
    if (_started || !supported) return;
    _started = true;
    try {
      // 运行目录放外部存储的应用专属目录：引擎的 panic/运行日志落在
      // fanqie_engine.log，adb 可以直接 pull 出来定位问题。
      final base = await getExternalStorageDirectory() ??
          await getApplicationSupportDirectory();
      final runtimeDir = path.join(base.path, 'fanqie');
      await Directory(runtimeDir).create(recursive: true);
      for (final name in ['config.json', 'filter.json']) {
        final text = await rootBundle.loadString('assets/fanqie/$name');
        await File(path.join(runtimeDir, name)).writeAsString(text, flush: true);
      }
      final library = DynamicLibrary.open('libfanqie_shim.so');
      final init = library.lookupFunction<
          Int32 Function(
              Pointer<Utf8>, Pointer<Utf8>, Pointer<Utf8>, Pointer<Utf8>, Uint16),
          int Function(Pointer<Utf8>, Pointer<Utf8>, Pointer<Utf8>,
              Pointer<Utf8>, int)>('fanqie_init');
      final status = library
          .lookupFunction<Int32 Function(), int Function()>('fanqie_status');
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
    } catch (error) {
      DiaryService.add('[fanqie] 内容引擎不可用: $error');
    }
  }
}
