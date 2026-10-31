import 'dart:async';
import 'dart:convert';
import 'dart:ffi';
import 'dart:io';
import 'dart:isolate';

import 'package:ffi/ffi.dart';
import 'package:path/path.dart' as path;
import 'package:path_provider/path_provider.dart';

import 'models.dart';
import 'danmaku_models.dart';
import 'background_downloads.dart';
import 'local_store.dart';
import 'app_build.dart';
import 'source_status.dart';
import 'ranking_models.dart';
import 'cover_decoder.dart';
import 'catalog_updates.dart';
import 'download_collections.dart';
import 'resource_settings.dart';
import 'diary_service.dart';

typedef _NativeRequest = Pointer<Utf8> Function(Pointer<Utf8>);
typedef _DartRequest = Pointer<Utf8> Function(Pointer<Utf8>);
typedef _NativeFree = Void Function(Pointer<Utf8>);
typedef _DartFree = void Function(Pointer<Utf8>);

/// macOS 上 Go 核心是随包分发的 `Contents/Frameworks/libduanju_core.dylib`
/// （由 scripts/build_macos.py 塞进去并补签名）。它不像 iOS 那样链进主程序，
/// 所以只能按路径 dlopen，不能用 DynamicLibrary.process()。
String _macosCoreLibraryPath() => path.join(
  path.dirname(Platform.resolvedExecutable),
  '..',
  'Frameworks',
  'libduanju_core.dylib',
);

String _nativeRequest(String body) {
  final DynamicLibrary library;
  if (Platform.isAndroid) {
    library = DynamicLibrary.open('libduanju_core.so');
  } else if (Platform.isWindows) {
    library = DynamicLibrary.open(
      path.join(path.dirname(Platform.resolvedExecutable), 'duanju_core.dll'),
    );
  } else if (Platform.isMacOS) {
    library = DynamicLibrary.open(_macosCoreLibraryPath());
  } else if (Platform.isIOS) {
    library = DynamicLibrary.process();
  } else {
    throw UnsupportedError('当前支持 Android、iOS、Windows 和 macOS');
  }
  final request = library.lookupFunction<_NativeRequest, _DartRequest>(
    'DuanjuRequest',
  );
  final free = library.lookupFunction<_NativeFree, _DartFree>('DuanjuFree');
  final input = body.toNativeUtf8();
  Pointer<Utf8> output = nullptr;
  try {
    output = request(input);
    if (output == nullptr) {
      throw StateError('本地核心没有返回结果');
    }
    return output.toDartString();
  } finally {
    malloc.free(input);
    if (output != nullptr) {
      free(output);
    }
  }
}

typedef _NativeStderrLog = Int32 Function(Pointer<Utf8>);
typedef _DartStderrLog = int Function(Pointer<Utf8>);

/// 把原生核心（c-shared / c-archive）的标准错误重定向到 [logPath]。
///
/// Go 运行时发生 fatal error（典型：并发 map 读写、OOM）时，会把完整堆栈
/// 直接打到 stderr 然后 exit(2)；桌面端没有 console，这些输出会彻底丢失，
/// 表现为「闪退却查不到任何日志」。重定向后即便 fatal 也能把堆栈留在文件里。
/// 该符号在旧版原生核心里不存在，查找失败时静默跳过（旧包退回原行为）。
void installStderrLog(String logPath) {
  try {
    final DynamicLibrary library;
    if (Platform.isAndroid) {
      library = DynamicLibrary.open('libduanju_core.so');
    } else if (Platform.isWindows) {
      library = DynamicLibrary.open(
        path.join(path.dirname(Platform.resolvedExecutable), 'duanju_core.dll'),
      );
    } else if (Platform.isMacOS) {
      library = DynamicLibrary.open(_macosCoreLibraryPath());
    } else if (Platform.isIOS) {
      library = DynamicLibrary.process();
    } else {
      return;
    }
    final install = library.lookupFunction<_NativeStderrLog, _DartStderrLog>(
      'DuanjuInstallStderrLog',
    );
    final ptr = logPath.toNativeUtf8();
    try {
      install(ptr);
    } finally {
      malloc.free(ptr);
    }
  } catch (_) {
    // 符号不存在（旧原生核心）或路径不可写：忽略，退回原行为。
  }
}

class AppFailure implements Exception {
  AppFailure(this.message, {this.code = ''});
  final String message;
  final String code;
  @override
  String toString() => message;
}

abstract class AppRepository {
  Future<Map<String, dynamic>> lan(
    String command,
    Map<String, dynamic> payload,
  ) async => throw AppFailure('当前环境不支持设备互联');
  Future<PlaybackPlan?> prepareHandoff(
    Drama drama,
    Episode episode, {
    int quality = 0,
    bool adBlock = true,
  }) => preload(drama, episode, quality: quality, adBlock: adBlock);
  Future<void> cancelHandoff() async {}
  Future<ResourceSettings> resourceSettings() async => const ResourceSettings();
  Future<ResourceSettings> saveResourceSettings(
    ResourceSettings settings,
  ) async => throw AppFailure('当前环境不支持资源设置');
  final catalogUpdates = CatalogUpdates();
  Future<void> cancelPreload() async {}
  Future<PlaybackPlan?> preload(
    Drama drama,
    Episode episode, {
    int quality = 0,
    bool online = false,
    bool adBlock = true,
    int route = 0,
  }) async => null;
  Future<void> cancelDanmaku() async {}
  Future<DanmakuPage> danmaku(
    PlaybackPlan plan, {
    required int startMs,
    required int durationMs,
  }) async => throw AppFailure('当前环境不支持弹幕');
  Future<void> cancelCatalog() async {}
  Future<void> cancelSuggestions() async {}
  Future<void> cancelRecommendations() async {}
  Future<Drama?> supplementMetadata(Drama drama) async => null;
  Future<Drama> followCheck(Drama drama) async => (await detail(drama)).drama;
  Future<CatalogPage> recommendations(
    String genre, {
    bool more = false,
    bool force = false,
  }) async => throw AppFailure('当前环境不支持红果推荐');
  Future<List<RankingBoard>> rankingBoards() async => const [];
  Future<RankingPage> rankings(
    String board, {
    int page = 1,
    bool force = false,
  }) async => throw AppFailure('当前环境不支持榜单');
  Future<List<CatalogCategory>> categories(
    String source, {
    bool force = false,
  }) async => const [CatalogCategory.all];
  bool get supportsSourceManagement => false;
  Future<SourceStatus> sourceStatus(String source) async =>
      SourceStatus.fromJson({'source': source});
  Future<SourceStatus> startSourceJob(
    String source,
    String operation, {
    Drama? drama,
  }) async => throw AppFailure('当前环境不支持站源管理');
  Future<SourceStatus> cancelSourceJob(String source) async =>
      throw AppFailure('当前环境不支持站源管理');
  Future<List<String>> suggestions(String query) async => const [];
  Future<Map<String, dynamic>> storage() async => {};
  Future<String> downloadDirectory() async =>
      (await storage())['directory'] as String? ?? '';
  Future<void> moveDownloads(String directory) async =>
      throw AppFailure('当前环境不支持迁移');
  Future<int> workLease(String id, String command) async => 0;
  bool get supportsDownloads => false;
  Future<List<DownloadJob>> downloads() async => [];
  Future<int> enqueueDownloads(
    DramaDetail detail,
    List<Episode> episodes, {
    int quality = 0,
  }) async => throw AppFailure('当前环境不支持下载');
  Future<void> controlDownloads(String command, {String id = ''}) async {}
  Future<DownloadBatchResult> controlDownloadBatch(
    String command,
    List<String> ids, {
    Map<String, String> expectedVersions = const {},
  }) async {
    if (expectedVersions.isNotEmpty) throw AppFailure('当前环境不支持校验后清理原分集');
    final completed = <String>[], failures = <String, String>{};
    for (final id in ids.toSet()) {
      try {
        await controlDownloads(command, id: id);
        completed.add(id);
      } catch (error) {
        failures[id] = error.toString();
      }
    }
    return DownloadBatchResult(completed: completed, failures: failures);
  }

  Future<int> updateDownloadCollection(
    DramaDetail detail,
    List<Episode> episodes, {
    int quality = 0,
  }) => enqueueDownloads(detail, episodes, quality: quality);
  Future<PlaybackPlan?> localPlayback(Drama drama, Episode episode) async =>
      null;
  Future<PlaybackPlan> resolveOnline(
    Drama drama,
    Episode episode, {
    int quality = 0,
    bool adBlock = true,
    int route = 0,
  }) => resolve(
    drama,
    episode,
    quality: quality,
    adBlock: adBlock,
    route: route,
  );
  Future<void> initialize();
  Future<CatalogPage> catalog(
    String source, {
    int page = 1,
    String query = '',
    String category = '',
    bool force = false,
  });
  Future<CatalogPage> cached(String source, {String category = ''});
  /// 跨源聚合搜索：并发搜索 [sources] 并归一去重。无跨源能力的仓库默认不可用。
  Future<CatalogPage> searchAcross(List<String> sources, String query) =>
      throw AppFailure('当前环境不支持全部源搜索');
  Future<String> cover(Drama drama, {bool force = false});
  bool coverCached(Drama drama) => false;
  Future<DramaDetail> detail(Drama drama);
  /// [route] 是上一次选中的线路序号（0 起）：切集、换清晰度时沿用它，
  /// 只有这一集没有这条线路时核心才会回落到第一条线路。
  Future<PlaybackPlan> resolve(
    Drama drama,
    Episode episode, {
    int quality = 0,
    bool adBlock = true,
    int route = 0,
  });
  Future<PlaybackPlan> fallback(PlaybackPlan current);
  Future<PlaybackPlan> selectRoute(PlaybackPlan current, int route) =>
      throw AppFailure('当前环境不支持切换播放线路');
  Future<void> cancelPlayback();
  Future<void> release(String session);

  /// 投屏转发用：把当前播放会话在局域网口再挂一份，返回电视能访问的内网基址。
  /// 直连原始线路的投屏不走这里（也就不需要手机一直开着）。
  Future<String> castBase({String peer = ''}) async =>
      throw AppFailure('当前环境不支持投屏');

  /// 关掉投屏转发端口。没开过也能安全调用。
  Future<void> releaseCastBase() async {}

  Future<List<CustomSourceSite>> customSources() async => const [];
  Future<CustomSourceSite> addCustomSource({
    required String name,
    required String base,
    String rule = '',
    String kind = '',
  }) async => throw AppFailure('当前环境不支持自定义源');
  Future<CustomSourceSite> updateCustomSource(
    String id, {
    required String name,
    required String base,
    String rule = '',
    String kind = '',
  }) async => throw AppFailure('当前环境不支持自定义源');
  Future<void> removeCustomSource(String id) async =>
      throw AppFailure('当前环境不支持自定义源');

  /// 导出全部自定义源为 JSON 文本（含 XBPQ 规则原文）。
  Future<String> exportCustomSources() async =>
      throw AppFailure('当前环境不支持自定义源');

  /// 导入自定义源文本，返回 {added, updated, skipped, failed} 计数与原因。
  Future<Map<String, dynamic>> importCustomSources(String content) async =>
      throw AppFailure('当前环境不支持自定义源');

  /// 恢复默认：删除全部自定义源，仅保留内置源。返回删除数量。
  Future<int> resetCustomSources() async =>
      throw AppFailure('当前环境不支持自定义源');
}

class NativeRepository extends AppRepository {
  static final _coverDecoder = CoverDecoder();
  static final _coverPaths = <String, String>{};
  NativeRepository({this.background = false});
  final bool background;
  LocalStore? access;
  final _readOwner = DateTime.now().microsecondsSinceEpoch.toString();
  int _readSequence = 0;
  final _activeReads = <String, int>{};

  @override
  Future<Map<String, dynamic>> lan(
    String command,
    Map<String, dynamic> payload,
  ) => _call({'action': 'lan', 'command': command, 'lan': payload});

  @override
  Future<PlaybackPlan?> prepareHandoff(
    Drama drama,
    Episode episode, {
    int quality = 0,
    bool adBlock = true,
  }) async {
    final data = await _read('handoff', {
      'action': 'prepareHandoff',
      'drama': drama.toJson(),
      'chapter': episode.raw,
      'index': episode.number,
      'quality': quality,
      'adBlock': adBlock,
      'force': access != null && !access!.canDownload,
    });
    return PlaybackPlan.fromJson(data);
  }

  @override
  Future<void> cancelHandoff() => _cancelReads('handoff');

  void _adminPermission() {
    if (access != null && (access!.locked || !access!.profile.admin)) {
      throw AppFailure('仅管理员可修改本机资源设置');
    }
  }

  @override
  Future<ResourceSettings> resourceSettings() async {
    _adminPermission();
    return ResourceSettings.fromJson(
      await _call({'action': 'resourceSettings'}),
    );
  }

  @override
  Future<ResourceSettings> saveResourceSettings(
    ResourceSettings settings,
  ) async {
    _adminPermission();
    return ResourceSettings.fromJson(
      await _call({
        'action': 'saveResourceSettings',
        'settings': settings.toJson(),
      }),
    );
  }

  Future<Map<String, dynamic>> _read(
    String scope,
    Map<String, dynamic> input,
  ) async {
    final sequence = ++_readSequence;
    _activeReads[scope] = sequence;
    try {
      return await _call({
        ...input,
        'session': '$_readOwner:$scope',
        'sequence': sequence,
      });
    } finally {
      if (_activeReads[scope] == sequence) _activeReads.remove(scope);
    }
  }

  Future<void> _cancelReads(String prefix) async {
    final reads = _activeReads.entries
        .where((entry) => entry.key.startsWith(prefix))
        .toList();
    await Future.wait(
      reads.map((entry) async {
        try {
          await _call({
            'action': 'cancelRead',
            'session': '$_readOwner:${entry.key}',
            'sequence': entry.value,
          });
        } catch (_) {}
      }),
    );
  }

  @override
  Future<void> cancelDanmaku() => _cancelReads('danmaku');

  @override
  Future<void> cancelPreload() => _cancelReads('preload');

  @override
  Future<PlaybackPlan?> preload(
    Drama drama,
    Episode episode, {
    int quality = 0,
    bool online = false,
    bool adBlock = true,
    int route = 0,
  }) async => PlaybackPlan.fromJson(
    await _read('preload', {
      'action': 'preload',
      'drama': drama.toJson(),
      'chapter': episode.raw,
      'index': episode.number,
      'quality': quality,
      'adBlock': adBlock,
      'route': route,
      'force': online || access?.canDownload == false,
    }),
  );

  @override
  Future<DanmakuPage> danmaku(
    PlaybackPlan plan, {
    required int startMs,
    required int durationMs,
  }) async {
    _authorize('hongguo');
    if (plan.local || plan.session.isEmpty || plan.danmakuId.isEmpty) {
      throw AppFailure('本集暂不支持弹幕');
    }
    return DanmakuPage.fromJson(
      await _read('danmaku', {
        'action': 'danmaku',
        'playbackSession': plan.session,
        'startMs': startMs,
        'durationMs': durationMs,
      }),
      episodeId: plan.danmakuId,
      startMs: startMs,
      durationMs: durationMs,
    );
  }

  @override
  Future<void> cancelCatalog() => _cancelReads('catalog-');
  @override
  Future<void> cancelSuggestions() => _cancelReads('suggestions');
  @override
  Future<void> cancelRecommendations() => _cancelReads('recommendations-');

  @override
  Future<CatalogPage> recommendations(
    String genre, {
    bool more = false,
    bool force = false,
  }) async {
    _authorize('hongguo');
    return CatalogPage.fromJson(
      await _read('recommendations-$genre', {
        'action': 'recommendations',
        'category': genre,
        'command': more ? 'more' : '',
        'force': force,
      }),
    );
  }

  @override
  Future<Drama?> supplementMetadata(Drama drama) async {
    if (!(drama.source == 'hongguo' && drama.onlineDate.isEmpty ||
        drama.source == 'huangdou' &&
            (drama.heat.isEmpty || drama.vipStatus == null))) {
      return null;
    }
    final result = await _read('metadata', {
      'action': 'metadata',
      'drama': drama.toJson(),
    });
    return Drama.fromJson(Map<String, dynamic>.from(result['drama'] as Map));
  }

  @override
  Future<Drama> followCheck(Drama drama) async {
    _authorize(drama.source);
    final result = await _call({
      'action': 'followCheck',
      'drama': drama.toJson(),
    });
    return Drama.fromJson(Map<String, dynamic>.from(result['drama'] as Map));
  }

  @override
  bool get supportsSourceManagement => true;

  @override
  Future<List<RankingBoard>> rankingBoards() async {
    final result = await _call({'action': 'rankingBoards'});
    return [
          for (final row in result['items'] as List? ?? [])
            RankingBoard.fromJson(Map<String, dynamic>.from(row as Map)),
        ]
        .where(
          (board) =>
              SourceSite.isAvailable(board.source) &&
              (access?.allowsSource(board.source) ?? true),
        )
        .toList();
  }

  @override
  Future<RankingPage> rankings(
    String board, {
    int page = 1,
    bool force = false,
  }) async => RankingPage.fromJson(
    await _call({
      'action': 'rankings',
      'board': board,
      'page': page,
      'force': force,
    }),
  );

  @override
  Future<SourceStatus> sourceStatus(String source) async =>
      SourceStatus.fromJson(
        await _call({'action': 'sourceStatus', 'source': source}),
      );

  @override
  Future<SourceStatus> startSourceJob(
    String source,
    String operation, {
    Drama? drama,
  }) async {
    _authorize(source);
    if (drama != null && drama.source != source) throw AppFailure('站源与剧集不匹配');
    final epoch = access?.profileEpoch;
    await BackgroundDownloads.ensureStarted();
    if (epoch != access?.profileEpoch) throw AppFailure('用户已切换，请重新操作');
    return SourceStatus.fromJson(
      await _call({
        'action': 'sourceJob',
        'source': source,
        'command': operation,
        if (drama != null) 'drama': drama.toJson(),
      }),
    );
  }

  @override
  Future<SourceStatus> cancelSourceJob(String source) async =>
      SourceStatus.fromJson(
        await _call({'action': 'cancelSourceJob', 'source': source}),
      );

  void _authorize(String source, {bool download = false}) {
    if (!SourceSite.isAvailable(source)) {
      throw AppFailure('当前版本不包含此站源');
    }
    if (access == null) return;
    if (access!.locked ||
        !access!.allowsSource(source) ||
        download && !access!.canDownload) {
      throw AppFailure('当前用户没有此操作权限');
    }
  }

  void _downloadPermission() {
    if (access != null && (access!.locked || !access!.canDownload)) {
      throw AppFailure('当前用户仅支持在线观看');
    }
  }

  @override
  Future<List<String>> suggestions(String query) async {
    _authorize('hongguo');
    final result = await _read('suggestions', {
      'action': 'suggestions',
      'query': query,
    });
    return (result['items'] as List? ?? []).whereType<String>().toList();
  }

  @override
  Future<String> downloadDirectory() async {
    _downloadPermission();
    return (await _call({'action': 'downloadDirectory'}))['directory']
            as String? ??
        '';
  }

  @override
  Future<Map<String, dynamic>> storage() async {
    _downloadPermission();
    return _call({'action': 'storage'});
  }

  @override
  Future<void> moveDownloads(String directory) async {
    _downloadPermission();
    if (access != null && !access!.profile.admin) {
      throw AppFailure('仅管理员可更改下载目录');
    }
    await BackgroundDownloads.ensureStarted();
    await workLease('storage', 'start');
    try {
      await _call({'action': 'moveDownloads', 'directory': directory});
    } finally {
      await workLease('storage', 'end');
    }
  }

  @override
  Future<int> workLease(String id, String command) async => intValue(
    (await _call({
      'action': 'workLease',
      'jobId': id,
      'command': command,
    }))['count'],
  );
  int _playbackSequence = DateTime.now().microsecondsSinceEpoch;

  Future<Map<String, dynamic>> _call(Map<String, dynamic> input) async {
    try {
      final action = input['action'] as String;
      final unrestricted =
          {
            'initialize',
            'release',
            'cancelPlayback',
            'cancelRead',
            'updateSystemProxy',
          }.contains(action) ||
          action == 'workLease' && input['command'] == 'end' ||
          action == 'lan' &&
              {
                'stop',
                'respond',
                'cancel',
                'disconnect',
              }.contains(input['command']);
      final epoch = access?.profileEpoch;
      if (!unrestricted && access?.locked == true) throw AppFailure('请先解锁当前用户');
      if (action == 'rankings') {
        _authorize(RankingBoard.sourceForID(input['board'] as String));
      }
      if (action == 'recommendations' ||
          action == 'cachedRecommendations' ||
          action == 'suggestions' ||
          action == 'danmaku') {
        _authorize('hongguo');
      }
      if ({
        'catalog',
        'cached',
        'categories',
        'sourceStatus',
        'sourceJob',
        'cancelSourceJob',
      }.contains(action)) {
        _authorize(input['source'] as String);
      }
      if ({
        'cover',
        'prepareCover',
        'detail',
        'metadata',
        'resolve',
        'preload',
        'prepareHandoff',
        'enqueueDownloads',
        'localPlayback',
      }.contains(action)) {
        _authorize(
          (input['drama'] as Map)['source'] as String,
          download: action == 'enqueueDownloads' || action == 'localPlayback',
        );
      }
      if (!unrestricted &&
          {
            'downloads',
            'controlDownloads',
            'controlDownloadBatch',
            'storage',
            'downloadDirectory',
            'moveDownloads',
            'workLease',
          }.contains(action)) {
        _downloadPermission();
      }
      if (action == 'resolve' && access != null && !access!.canDownload) {
        input['force'] = true;
      }
      final body = jsonEncode(input);
      final response =
          await Isolate.run(
            () => jsonDecode(_nativeRequest(body)) as Map<String, dynamic>,
          ).timeout(
            Duration(
              seconds: action == 'moveDownloads'
                  ? 620
                  : action == 'danmaku'
                  ? 15
                  : action == 'preload'
                  ? 20
                  : 70,
            ),
          );
      if (response['ok'] != true) {
        throw AppFailure(
          response['error'] as String? ?? '读取失败，请重试',
          code: response['code'] as String? ?? '',
        );
      }
      final data = response['data'];
      if (!unrestricted && epoch != access?.profileEpoch) {
        if (data is Map && data['session'] is String) {
          await release(data['session'] as String);
        }
        if (action == 'workLease' && input['command'] == 'start') {
          await workLease(input['jobId'] as String, 'end');
        }
        throw AppFailure('用户已切换，请重新操作');
      }
      return data is Map ? Map<String, dynamic>.from(data) : {};
    } on AppFailure {
      rethrow;
    } on TimeoutException {
      if (input['session'] is String &&
          input['sequence'] is int &&
          {
            'catalog',
            'categories',
            'suggestions',
            'recommendations',
            'metadata',
            'danmaku',
            'preload',
            'prepareHandoff',
          }.contains(input['action'])) {
        unawaited(
          _call({
            'action': 'cancelRead',
            'session': input['session'],
            'sequence': input['sequence'],
          }).catchError((Object _) => <String, dynamic>{}),
        );
      }
      throw AppFailure('站源响应超时，请重试');
    } catch (_) {
      throw AppFailure('本地核心加载失败，请使用完整安装包重新安装');
    }
  }

  @override
  Future<void> initialize() async {
    final directory = await getApplicationSupportDirectory();
    // 尽早把原生核心的标准错误重定向到日志文件：Go 运行时发生 fatal
    // （并发 map 读写、OOM 等）时堆栈会写进 <logs>/native_stderr.log，
    // 否则桌面端「闪退却查不到任何日志」。符号不存在时静默跳过。
    installStderrLog(path.join(directory.path, 'logs', 'native_stderr.log'));
    if (!background) {
      DiaryService.nativeLogPath = path.join(directory.path, 'logs', 'app.log');
    }
    final build = await _call({
      'action': 'initialize',
      'directory': directory.path,
    });
    if (build['allSources'] != allSourcesEnabled) {
      throw AppFailure('应用与原生核心的站源版本不一致，请使用完整安装包重新安装');
    }
    if (!background) await BackgroundDownloads.prepare();
    if (!background) {
      SystemProxyMonitor.start((value) async {
        await _call({'action': 'updateSystemProxy', 'systemProxy': value});
      });
    }
  }

  @override
  Future<List<CatalogCategory>> categories(
    String source, {
    bool force = false,
  }) async {
    final result = await _read('categories-$source', {
      'action': 'categories',
      'source': source,
      'force': force,
    });
    return [
      for (final row in result['items'] as List? ?? const [])
        CatalogCategory.fromJson(Map<String, dynamic>.from(row as Map)),
    ];
  }

  @override
  Future<CatalogPage> catalog(
    String source, {
    int page = 1,
    String query = '',
    String category = '',
    bool force = false,
  }) async => CatalogPage.fromJson(
    await _read('catalog-$source', {
      'action': 'catalog',
      'source': source,
      'page': page,
      'query': query,
      'category': category,
      'force': force,
    }),
  );
  @override
  Future<CatalogPage> cached(String source, {String category = ''}) async =>
      CatalogPage.fromJson(
        await _call({
          'action': 'cached',
          'source': source,
          'category': category,
        }),
      );
  @override
  Future<CatalogPage> searchAcross(List<String> sources, String query) async =>
      CatalogPage.fromJson(
        await _call({
          'action': 'searchAcross',
          'sources': sources,
          'query': query,
        }),
      );
  @override
  bool coverCached(Drama drama) =>
      _coverPaths.containsKey('${drama.id}\u0000${drama.cover}');

  @override
  Future<String> cover(Drama drama, {bool force = false}) async {
    final key = '${drama.id}\u0000${drama.cover}';
    final known = force ? null : _coverPaths[key];
    if (known != null) {
      _authorize(drama.source);
      if (await File(known).exists()) return known;
      _coverPaths.remove(key);
    }
    final epoch = access?.profileEpoch;
    final result = await _call({
      'action': 'cover',
      'drama': drama.toJson(),
      'force': force,
    });
    final file = result['path'] as String? ?? '';
    if (file.isEmpty) throw AppFailure('海报暂不可用');
    if (result['heic'] != true) return _rememberCover(key, file);
    final converted = await _coverDecoder.convert(
      file,
      () => _call({'action': 'prepareCover', 'drama': drama.toJson()}),
      force: force,
    );
    _authorize(drama.source);
    if (epoch != access?.profileEpoch) throw AppFailure('用户已切换，请重新操作');
    return _rememberCover(key, converted);
  }

  String _rememberCover(String key, String path) {
    _coverPaths.remove(key);
    _coverPaths[key] = path;
    if (_coverPaths.length > 2000) _coverPaths.remove(_coverPaths.keys.first);
    return path;
  }

  @override
  Future<DramaDetail> detail(Drama drama) async => DramaDetail.fromJson(
    await _call({'action': 'detail', 'drama': drama.toJson()}),
  );
  @override
  Future<PlaybackPlan> resolve(
    Drama drama,
    Episode episode, {
    int quality = 0,
    bool adBlock = true,
    int route = 0,
  }) async => PlaybackPlan.fromJson(
    await _call({
      'action': 'resolve',
      'drama': drama.toJson(),
      'chapter': episode.raw,
      'index': episode.number,
      'quality': quality,
      'adBlock': adBlock,
      'route': route,
      'sequence': ++_playbackSequence,
    }),
  );
  @override
  Future<PlaybackPlan> fallback(PlaybackPlan current) async =>
      PlaybackPlan.fromJson(
        await _call({
          'action': 'fallback',
          'session': current.session,
          'sequence': ++_playbackSequence,
        }),
      );
  @override
  Future<PlaybackPlan> selectRoute(PlaybackPlan current, int route) async =>
      PlaybackPlan.fromJson(
        await _call({
          'action': 'selectRoute',
          'session': current.session,
          'route': route,
          'sequence': ++_playbackSequence,
        }),
      );
  @override
  Future<void> cancelPlayback() async {
    await _call({'action': 'cancelPlayback', 'sequence': ++_playbackSequence});
  }

  @override
  bool get supportsDownloads => access?.canDownload ?? true;

  @override
  Future<List<DownloadJob>> downloads() async {
    final result = await _call({'action': 'downloads'});
    return (result['jobs'] as List? ?? [])
        .whereType<Map>()
        .map((value) => DownloadJob.fromJson(Map<String, dynamic>.from(value)))
        .where(
          (job) =>
              SourceSite.isAvailable(job.drama.source) &&
              (access == null || access!.allowsSource(job.drama.source)),
        )
        .toList();
  }

  @override
  Future<int> enqueueDownloads(
    DramaDetail detail,
    List<Episode> episodes, {
    int quality = 0,
  }) async {
    _authorize(detail.drama.source, download: true);
    final epoch = access?.profileEpoch;
    await BackgroundDownloads.ensureStarted();
    if (epoch != access?.profileEpoch) throw AppFailure('用户已切换，请重新操作');
    final result = await _call({
      'action': 'enqueueDownloads',
      'drama': detail.drama.toJson(),
      'quality': quality,
      'entries': episodes
          .map((episode) => {'chapter': episode.raw, 'index': episode.number})
          .toList(),
    });
    return intValue(result['added']);
  }

  @override
  Future<int> updateDownloadCollection(
    DramaDetail detail,
    List<Episode> episodes, {
    int quality = 0,
  }) async {
    _authorize(detail.drama.source, download: true);
    final epoch = access?.profileEpoch;
    await BackgroundDownloads.ensureStarted();
    if (epoch != access?.profileEpoch) throw AppFailure('用户已切换，请重新操作');
    final result = await _call({
      'action': 'enqueueDownloads',
      'force': true,
      'drama': detail.drama.toJson(),
      'quality': quality,
      'entries': episodes
          .map((episode) => {'chapter': episode.raw, 'index': episode.number})
          .toList(),
    });
    return intValue(result['added']);
  }

  @override
  Future<DownloadBatchResult> controlDownloadBatch(
    String command,
    List<String> ids, {
    Map<String, String> expectedVersions = const {},
  }) async {
    _downloadPermission();
    final epoch = access?.profileEpoch;
    if (ids.isEmpty || ids.length > 500) throw AppFailure('每批请选择 1 至 500 个任务');
    final visible = (await downloads()).map((job) => job.id).toSet();
    if (ids.any((id) => !visible.contains(id))) {
      throw AppFailure('部分任务已删除或当前用户无权操作，请刷新');
    }
    if (command == 'resume') await BackgroundDownloads.ensureStarted();
    if (epoch != access?.profileEpoch) throw AppFailure('用户已切换，请重新操作');
    return DownloadBatchResult.fromJson(
      await _call({
        'action': 'controlDownloadBatch',
        'command': command,
        'jobIds': ids,
        if (expectedVersions.isNotEmpty) 'expectedVersions': expectedVersions,
      }),
    );
  }

  @override
  Future<void> controlDownloads(String command, {String id = ''}) async {
    _downloadPermission();
    if (command == 'resume' || command == 'resumeAll') {
      await BackgroundDownloads.ensureStarted();
    }
    if (access != null && !access!.profile.admin) {
      final visible = await downloads();
      if (command == 'pauseAll' || command == 'resumeAll') {
        for (final job in visible.where(
          (job) => command == 'pauseAll' ? job.active : job.resumable,
        )) {
          await _call({
            'action': 'controlDownloads',
            'command': command == 'pauseAll' ? 'pause' : 'resume',
            'jobId': job.id,
          });
        }
        return;
      }
      if (!visible.any((job) => job.id == id)) {
        throw AppFailure('当前用户没有此下载任务权限');
      }
    }
    await _call({
      'action': 'controlDownloads',
      'command': command,
      'jobId': id,
    });
  }

  @override
  Future<PlaybackPlan?> localPlayback(Drama drama, Episode episode) async {
    final result = await _call({
      'action': 'localPlayback',
      'drama': drama.toJson(),
      'index': episode.number,
    });
    if ((result['url'] as String? ?? '').isEmpty) return null;
    return PlaybackPlan.fromJson(result);
  }

  @override
  Future<PlaybackPlan> resolveOnline(
    Drama drama,
    Episode episode, {
    int quality = 0,
    bool adBlock = true,
    int route = 0,
  }) async => PlaybackPlan.fromJson(
    await _call({
      'action': 'resolve',
      'drama': drama.toJson(),
      'chapter': episode.raw,
      'index': episode.number,
      'quality': quality,
      'adBlock': adBlock,
      'route': route,
      'force': true,
      'sequence': ++_playbackSequence,
    }),
  );

  @override
  Future<void> release(String session) async {
    if (session.isEmpty) {
      return;
    }
    try {
      await _call({'action': 'release', 'session': session});
    } catch (_) {}
  }

  @override
  Future<String> castBase({String peer = ''}) async {
    final result = await _call({'action': 'castBase', 'peer': peer});
    return result['base']?.toString() ?? '';
  }

  @override
  Future<void> releaseCastBase() async {
    try {
      await _call({'action': 'closeCast'});
    } catch (_) {}
  }

  @override
  Future<List<CustomSourceSite>> customSources() async {
    _adminPermission();
    final result = await _call({'action': 'customSources'});
    return [
      for (final row in result['items'] as List? ?? const [])
        CustomSourceSite.fromJson(Map<String, dynamic>.from(row as Map)),
    ];
  }

  @override
  Future<CustomSourceSite> addCustomSource({
    required String name,
    required String base,
    String rule = '',
    String kind = '',
  }) async {
    _adminPermission();
    final result = await _call({
      'action': 'addCustomSource',
      'name': name,
      'base': base,
      'rule': rule,
      'kind': kind,
    });
    return CustomSourceSite.fromJson(Map<String, dynamic>.from(result as Map));
  }

  @override
  Future<CustomSourceSite> updateCustomSource(
    String id, {
    required String name,
    required String base,
    String rule = '',
    String kind = '',
  }) async {
    _adminPermission();
    final result = await _call({
      'action': 'updateCustomSource',
      'source': id,
      'name': name,
      'base': base,
      'rule': rule,
      'kind': kind,
    });
    return CustomSourceSite.fromJson(Map<String, dynamic>.from(result as Map));
  }

  @override
  Future<void> removeCustomSource(String id) async {
    _adminPermission();
    await _call({'action': 'removeCustomSource', 'source': id});
  }

  @override
  Future<String> exportCustomSources() async {
    _adminPermission();
    final result = await _call({'action': 'exportCustomSources'});
    return (result as Map)['content'] as String? ?? '';
  }

  @override
  Future<Map<String, dynamic>> importCustomSources(String content) async {
    _adminPermission();
    final result = await _call({
      'action': 'importCustomSources',
      'content': content,
    });
    return Map<String, dynamic>.from(result as Map);
  }

  @override
  Future<int> resetCustomSources() async {
    _adminPermission();
    final result = await _call({'action': 'resetCustomSources'});
    return (result as Map)['removed'] as int? ?? 0;
  }
}
