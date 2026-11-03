"""按固定提交号拉取上游 Rust 核心，编译番茄内容引擎的各平台产物。

引擎有两种形态，同一份 crate 编出来：

* ``fanqie_engine``（可执行）：Windows / macOS / Linux 桌面端与本地服务版用。
  宿主进程拉起它，它自己在 127.0.0.1:18921 起 loopback HTTP 服务。
* ``libfanqie_shim``（cdylib / staticlib）：Android 走 ``.so``，iOS 走静态库，
  宿主进程内直接 FFI 调用 ``fanqie_init``。

上游是 GPL-3.0 的 https://github.com/ch6vip/fqapp ，本仓库不内联它的源码，
而是按提交号下载后把 ``native/fanqie_shim`` 摆进它的 ``native/`` 目录下编译，
这样 ``Cargo.toml`` 里的 ``../fqapp/rust`` 相对路径与上游布局一致。

用法（在仓库根目录）：:

    python scripts/build_fanqie_engine.py --out build/fanqie/windows
    python scripts/build_fanqie_engine.py --out build/fanqie/macos --macos-universal
    python scripts/build_fanqie_engine.py --out build/fanqie/ios --target aarch64-apple-ios
"""

import argparse
import os
import shutil
import subprocess
import sys
import time
import urllib.request
from pathlib import Path


def ensure_printable_stdio():
    """让 stdout/stderr 至少能写出中文。

    CI 的 Windows runner 上 Python 按系统 ANSI 代码页取 stdout 编码（cp1252），
    脚本里任何一句中文 print 都会直接 ``UnicodeEncodeError`` 把构建打断——本地
    中文 Windows（cp936）反而看不出问题。这里只在当前编码表达不了中文时才切
    UTF-8，本机 GBK 控制台维持原样，不会被改出一屏乱码。
    """
    for stream in (sys.stdout, sys.stderr):
        if stream is None:
            continue
        encoding = getattr(stream, 'encoding', None) or 'utf-8'
        try:
            '番茄'.encode(encoding)
        except (UnicodeEncodeError, LookupError):
            try:
                stream.reconfigure(encoding='utf-8', errors='replace')
            except (AttributeError, ValueError):
                pass


ensure_printable_stdio()

# 固定提交号：上游 master 随时会变，出包必须可复现。
UPSTREAM_REPO = 'ch6vip/fqapp'
UPSTREAM_COMMIT = 'aabde536c8621092f13b1a95f52e631b189f9243'

# Android：ABI → (Rust target, NDK clang 包装脚本名模板)。
# CI 用 --split-per-abi 出三个 APK，每个 ABI 的包里都得有自己的
# libfanqie_shim.so，否则那个包上的番茄源一定「引擎未就绪」。
ANDROID_TARGETS = {
    'arm64-v8a': ('aarch64-linux-android', 'aarch64-linux-android{api}-clang'),
    'armeabi-v7a': ('armv7-linux-androideabi', 'armv7a-linux-androideabi{api}-clang'),
    'x86_64': ('x86_64-linux-android', 'x86_64-linux-android{api}-clang'),
}

root = Path(__file__).resolve().parents[1]
shim_source = root / 'native' / 'fanqie_shim'

parser = argparse.ArgumentParser(description='构建番茄内容引擎（各平台）')
parser.add_argument('--out', required=True, help='产物输出目录')
parser.add_argument('--target', action='append', default=[],
                    help='Rust target triple，可重复；默认按本机推断')
parser.add_argument('--macos-universal', action='store_true',
                    help='macOS 同时编 arm64 与 x86_64 再 lipo 成通用二进制')
parser.add_argument('--android', action='store_true',
                    help='按 Android ABI 编 libfanqie_shim.so，输出到 <out>/<abi>/')
parser.add_argument('--android-abi', action='append', default=[], choices=sorted(ANDROID_TARGETS),
                    help='只编这些 Android ABI，默认三种全编')
parser.add_argument('--android-skip-existing', action='store_true',
                    help='目标位置已有 .so 时跳过（CI 用它保护仓库里已验证的产物）')
parser.add_argument('--android-api', type=int, default=24,
                    help='Android API level（与 app 的 minSdk 一致，默认 24）')
parser.add_argument('--no-fetch', action='store_true',
                    help='跳过源码下载（本地已缓存时用）')
options = parser.parse_args()

out = Path(options.out)
if not out.is_absolute():
    out = root / out


def run(command, **kwargs):
    print('+', ' '.join(str(item) for item in command), flush=True)
    return subprocess.run([str(item) for item in command], check=True, **kwargs)


def cargo_binary():
    cargo = shutil.which('cargo')
    if cargo:
        return cargo
    fallback = Path.home() / '.cargo' / 'bin' / ('cargo.exe' if sys.platform == 'win32' else 'cargo')
    if fallback.is_file():
        return str(fallback)
    raise SystemExit(
        '找不到 cargo。请先安装 Rust 工具链（https://rustup.rs），'
        'CI 上 windows-latest / ubuntu-latest / macos-latest 默认已装。'
    )


def download(url, destination):
    """下载上游源码包。

    这条链路在部分网络上会中途断流（http.client.IncompleteRead），所以流式读写
    并整包重试几次——比读进内存再一次写盘更容易从断点恢复。
    """
    destination.parent.mkdir(parents=True, exist_ok=True)
    print('下载', url, flush=True)
    last_error = None
    for attempt in range(1, 6):
        request = urllib.request.Request(url, headers={'User-Agent': 'duanju-build'})
        try:
            with urllib.request.urlopen(request, timeout=180) as response:
                with open(destination, 'wb') as handle:
                    while True:
                        chunk = response.read(65536)
                        if not chunk:
                            break
                        handle.write(chunk)
            if destination.stat().st_size > 0:
                return
        except Exception as error:  # noqa: BLE001 - 网络抖动一律重试
            last_error = error
            print('  第 %d 次下载中断：%s' % (attempt, error), flush=True)
            destination.unlink(missing_ok=True)
            time.sleep(2)
    raise SystemExit('上游源码下载失败：%s' % last_error)


def patch_upstream_tls(upstream):
    """让上游的「流式下载客户端」复用 webpki-roots，而不是平台验证器。

    上游 ``endpoints/video.rs`` 里另起了一个裸 ``reqwest::Client::builder()``。
    reqwest 0.13 默认的证书验证走 ``rustls-platform-verifier``，而安卓是进程内
    核心（``fanqie_init``），从来没初始化过它——首次构建这个客户端就会 panic，
    现场表现是「引擎端口在听、/health 200，但取视频一律 context deadline
    exceeded，重试 EOF」，引擎日志只剩一句
    ``Expect rustls-platform-verifier to be initialized``。

    上游主链路 ``upstream/http.rs`` 已经自己用 webpki-roots 建好 TLS 配置并通过
    ``tls_backend_preconfigured`` 注入，这里让流式客户端也照做即可。补丁对桌面端
    同样有效（webpki 根证书各平台通用），并且是幂等的。
    """
    http_rs = upstream / 'rust' / 'src' / 'upstream' / 'http.rs'
    video_rs = upstream / 'rust' / 'src' / 'endpoints' / 'video.rs'
    for path in (http_rs, video_rs):
        if not path.is_file():
            raise SystemExit('上游源码结构异常，缺少：' + str(path))

    text = http_rs.read_text(encoding='utf-8')
    if 'pub(crate) fn build_tls_config' not in text:
        if 'fn build_tls_config()' not in text:
            raise SystemExit('上游 http.rs 里找不到 build_tls_config，补丁需人工核对')
        text = text.replace('fn build_tls_config()', 'pub(crate) fn build_tls_config()', 1)
        http_rs.write_text(text, encoding='utf-8')

    text = video_rs.read_text(encoding='utf-8')
    if 'tls_backend_preconfigured' in text:
        return  # 已打过补丁
    old = (
        '            reqwest::Client::builder()\n'
        '                .timeout(VIDEO_DOWNLOAD_TIMEOUT)\n'
        '                .build()\n'
        '                .ok()'
    )
    new = (
        '            // 安卓进程内核心没有初始化 rustls-platform-verifier，\n'
        '            // 裸 client 会在 build 时 panic；复用主链路的 webpki 配置。\n'
        '            let tls = crate::upstream::http::build_tls_config().ok()?;\n'
        '            reqwest::Client::builder()\n'
        '                .tls_backend_preconfigured(tls)\n'
        '                .timeout(VIDEO_DOWNLOAD_TIMEOUT)\n'
        '                .build()\n'
        '                .ok()'
    )
    if old not in text:
        raise SystemExit('上游 video.rs 的 streaming_client 结构与补丁不匹配，需人工核对')
    video_rs.write_text(text.replace(old, new, 1), encoding='utf-8')
    print('已为上游打上 webpki TLS 补丁（流式下载客户端）', flush=True)


def ensure_upstream():
    """把上游源码放到 build/fanqie_engine/native/fqapp，并同步 shim crate。"""
    work = root / 'build' / 'fanqie_engine'
    upstream = work / 'native' / 'fqapp'
    stamp = work / '.upstream-commit'
    if options.no_fetch and upstream.is_dir():
        cached = True
    else:
        cached = stamp.is_file() and stamp.read_text(encoding='utf-8').strip() == UPSTREAM_COMMIT
    if not cached or not (upstream / 'rust' / 'Cargo.toml').is_file():
        if upstream.exists():
            shutil.rmtree(upstream, ignore_errors=True)
        archive = work / f'{UPSTREAM_COMMIT}.tar.gz'
        if not archive.is_file():
            download(f'https://codeload.github.com/{UPSTREAM_REPO}/tar.gz/{UPSTREAM_COMMIT}', archive)
        staging = work / 'extract'
        if staging.exists():
            shutil.rmtree(staging)
        staging.mkdir(parents=True)
        # tarfile 在 3.12+ 支持 filter 参数；不支持时退回默认行为。
        import tarfile
        with tarfile.open(archive) as handle:
            try:
                handle.extractall(staging, filter='data')
            except TypeError:
                handle.extractall(staging)
        extracted = [item for item in staging.iterdir() if item.is_dir()]
        if len(extracted) != 1:
            raise SystemExit('上游压缩包结构异常：' + str(staging))
        upstream.parent.mkdir(parents=True, exist_ok=True)
        shutil.move(str(extracted[0]), str(upstream))
        shutil.rmtree(staging, ignore_errors=True)
        stamp.parent.mkdir(parents=True, exist_ok=True)
        stamp.write_text(UPSTREAM_COMMIT, encoding='utf-8')
    # shim crate 每次覆盖，保证用的是本仓库当前版本。
    target_crate = upstream.parent / 'fanqie_shim'
    if target_crate.exists():
        shutil.rmtree(target_crate)
    shutil.copytree(shim_source, target_crate)
    patch_upstream_tls(upstream)
    return target_crate


def adhoc_sign(path):
    """macOS 上给产物做 ad-hoc 签名。

    Apple 芯片的内核只肯执行带签名的 Mach-O：Go 链接器会自己补一个 ad-hoc
    签名，Rust 不会，所以这里必须补，否则引擎在 M 系列机器上会被直接
    SIGKILL（用户看到的是「Killed: 9」）。签名留在 Mach-O 里，之后打 zip
    或 dmg 都不会丢。
    """
    if sys.platform != 'darwin':
        return
    run(['codesign', '--force', '--sign', '-', path])


def default_targets():
    if sys.platform == 'win32':
        return ['x86_64-pc-windows-msvc']
    if sys.platform == 'darwin':
        if options.macos_universal:
            return ['aarch64-apple-darwin', 'x86_64-apple-darwin']
        return ['aarch64-apple-darwin']
    return ['x86_64-unknown-linux-gnu']


def artifact_names(triple):
    """返回 (可执行文件名, 动态库文件名, 静态库文件名)，按 target 推断后缀。"""
    stem = triple.split('-')
    if 'windows' in triple:
        return 'fanqie_engine.exe', 'fanqie_shim.dll', 'fanqie_shim.lib'
    if 'apple-ios' in triple:
        # iOS 不允许加载 App 包外的动态库，走静态库链进 Runner。
        return 'fanqie_engine', None, 'libfanqie_shim.a'
    if 'darwin' in triple:
        return 'fanqie_engine', 'libfanqie_shim.dylib', 'libfanqie_shim.a'
    del stem
    return 'fanqie_engine', 'libfanqie_shim.so', 'libfanqie_shim.a'


def android_ndk():
    """定位 Android NDK：先环境变量，再看 $ANDROID_HOME/ndk/<版本>（取最高版本）。"""
    for name in ('ANDROID_NDK_HOME', 'ANDROID_NDK_ROOT', 'ANDROID_NDK'):
        value = os.environ.get(name)
        if value and Path(value).is_dir():
            return Path(value)
    for name in ('ANDROID_HOME', 'ANDROID_SDK_ROOT'):
        base = os.environ.get(name)
        if not base:
            continue
        ndk_root = Path(base) / 'ndk'
        if ndk_root.is_dir():
            versions = sorted(item for item in ndk_root.iterdir() if item.is_dir())
            if versions:
                return versions[-1]
    return None


def android_toolchains(abis):
    """返回 [(abi, rust target, clang 路径)]，顺手确认 NDK 里真有这些编译器。"""
    ndk = android_ndk()
    if ndk is None:
        raise SystemExit(
            '找不到 Android NDK：请设置 ANDROID_NDK_HOME，'
            '或让 $ANDROID_HOME/ndk/<版本> 存在（CI 用 sdkmanager 安装 ndk）。'
        )
    host = {'darwin': 'darwin-x86_64', 'linux': 'linux-x86_64',
            'win32': 'windows-x86_64'}.get(sys.platform)
    if host is None:
        raise SystemExit('不支持的宿主平台：' + sys.platform)
    binaries = ndk / 'toolchains' / 'llvm' / 'prebuilt' / host / 'bin'
    if not binaries.is_dir():
        raise SystemExit('NDK 工具链目录不存在：' + str(binaries))
    suffix = '.cmd' if sys.platform == 'win32' else ''
    toolchains = []
    for abi in abis:
        triple, template = ANDROID_TARGETS[abi]
        clang = binaries / (template.format(api=options.android_api) + suffix)
        if not clang.is_file():
            raise SystemExit('NDK 里找不到编译器：' + str(clang))
        toolchains.append((abi, triple, clang))
    return toolchains


def build_android(toolchains):
    """按 ABI 各编一份 libfanqie_shim.so，输出到 <out>/<abi>/。

    APK 是按 ABI 拆分的，只有对应目录里有这份 .so，那个包上的番茄源才能起来；
    默认 arm64 那份可以 `--android-skip-existing` 跳过，避免覆盖仓库里已经
    在真机上跑过的产物。
    """
    crate = ensure_upstream()
    cargo = cargo_binary()
    environment = os.environ.copy()
    environment.setdefault('CARGO_TERM_COLOR', 'always')
    for abi, triple, clang in toolchains:
        destination = out / abi / 'libfanqie_shim.so'
        if options.android_skip_existing and destination.is_file():
            print('已存在，保留原文件', destination, flush=True)
            continue
        target_env = environment.copy()
        key = triple.upper().replace('-', '_')
        target_env['CARGO_TARGET_%s_LINKER' % key] = str(clang)
        # Android 15 起有 16KB 页的设备：段对齐按 16KB 来，否则装上去可能起不来。
        target_env['CARGO_TARGET_%s_RUSTFLAGS' % key] = \
            '-C link-arg=-Wl,-z,max-page-size=16384'
        # cc / cc-rs 找编译器用的变量名有两种拼法，都给它。
        ar = clang.parent / ('llvm-ar.exe' if sys.platform == 'win32' else 'llvm-ar')
        for spelling in (triple, triple.replace('-', '_')):
            target_env['CC_' + spelling] = str(clang)
            if ar.is_file():
                target_env['AR_' + spelling] = str(ar)
        run([cargo, 'build', '--release', '--lib', '--target', triple,
             '--manifest-path', crate / 'Cargo.toml'], env=target_env)
        built = crate / 'target' / triple / 'release' / 'libfanqie_shim.so'
        if not built.is_file():
            raise SystemExit('构建产物缺失：' + str(built))
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(built, destination)
        print('输出', destination, flush=True)


def main():
    if not shim_source.is_dir():
        raise SystemExit('缺少 native/fanqie_shim 源码目录。')
    if options.android:
        # 先验 NDK 再拉源码：缺工具链时不该先白下几十兆。
        build_android(android_toolchains(options.android_abi or list(ANDROID_TARGETS)))
        return
    crate = ensure_upstream()
    cargo = cargo_binary()
    targets = options.target or default_targets()
    out.mkdir(parents=True, exist_ok=True)

    environment = os.environ.copy()
    environment.setdefault('CARGO_TERM_COLOR', 'always')
    built = []
    for triple in targets:
        # iOS 只要静态库：那里既不能 fork 子进程，也没必要编可执行引擎。
        extra = ['--lib'] if 'apple-ios' in triple else []
        run([cargo, 'build', '--release', '--target', triple, *extra,
             '--manifest-path', crate / 'Cargo.toml'], env=environment)
        directory = crate / 'target' / triple / 'release'
        if not directory.is_dir():
            raise SystemExit('构建产物缺失：' + str(directory))
        built.append((triple, directory))

    if len(built) > 1 and 'apple-darwin' in built[0][0]:
        # macOS 出通用二进制：两个架构 lipo 成一个，Intel / Apple 芯片都能跑。
        engine_name, library_name, _ = artifact_names(built[0][0])
        for name in (engine_name, library_name):
            if not name:
                continue
            sources = [directory / name for _, directory in built]
            missing = [str(item) for item in sources if not item.is_file()]
            if missing:
                raise SystemExit('lipo 前缺少产物：' + ', '.join(missing))
            run(['lipo', '-create', *sources, '-output', out / name])
            os.chmod(out / name, 0o755)
            adhoc_sign(out / name)
        print('通用二进制已输出到', out)
        return

    for triple, directory in built:
        engine_name, library_name, static_name = artifact_names(triple)
        for name in (engine_name, library_name, static_name):
            if not name:
                continue
            source = directory / name
            if not source.is_file():
                # 某些 target 不产出 cdylib（如 iOS），缺失属正常，跳过即可。
                print('跳过缺失产物', name, flush=True)
                continue
            destination = out / name
            shutil.copy2(source, destination)
            if name == engine_name:
                os.chmod(destination, 0o755)
                adhoc_sign(destination)
            print('输出', destination, flush=True)


if __name__ == '__main__':
    main()
