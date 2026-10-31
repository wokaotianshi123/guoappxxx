"""构建短剧视界 / 全剧视界 macOS 应用与 dmg。

仓库里不带 `macos/` 工程目录（它和 `ios/` 一样是构建期产物），首次构建时用
`flutter create --platforms=macos` 生成，再按变体改写 PRODUCT_NAME / bundle id。

Go 核心编成通用 dylib（arm64 + x86_64，见 build_native.py），由本脚本塞进
`Contents/Frameworks` 并补 ad-hoc 签名；Dart 侧用 DynamicLibrary.open() 按路径
加载它（见 lib/core_bridge.dart）。这一点和 iOS 不同：iOS 是把 c-archive 静态库
链进主程序、用 DynamicLibrary.process() 取符号。
"""

import argparse
import os
import platform
import plistlib
import re
import shutil
import subprocess
import sys
from pathlib import Path

from build_mirrors import china_mirror_environment, mirrored_pub_lockfile
from ffmpeg_frameworks import ensure_pods, package_path
from app_build import BuildVariant, add_variant_argument

root = Path(__file__).resolve().parents[1]

# 12.0 与 Flutter 官方 macOS 模板 / Podfile 一致；ffmpeg_kit_flutter_new 的
# macOS 产物要求 10.15+，media_kit 要求 10.9+，都在这条线以下。
DEPLOYMENT_TARGET = '12.0'
CORE_LIBRARY = 'libduanju_core.dylib'


def run(arguments, **kwargs):
    print('+ ' + ' '.join(str(item) for item in arguments), flush=True)
    subprocess.run([str(item) for item in arguments],
                   cwd=kwargs.pop('cwd', root), check=True, **kwargs)


def ensure_platform(flutter, env):
    if (root / 'macos' / 'Runner.xcodeproj').exists():
        return
    print('未找到 macOS 平台目录，先生成。', flush=True)
    run([flutter, 'create', '--platforms=macos', '--org', 'com.duanju', '.'], env=env)


def apply_settings(config, settings):
    """就地覆盖 xcconfig 里的键：有则改值、无则追加到末尾。

    必须是「就地」而不是「删掉再追加」：后者每次构建都会轮换键顺序，文件天天
    变脏；而放在 `#include` 之前的同名键会被 include 覆盖，这种情况只保留末尾
    那一条（xcconfig 后赋值者生效）。
    """
    if not config.is_file():
        return
    lines = config.read_text(encoding='utf-8').splitlines()
    for key, value in settings.items():
        pattern = re.compile(r'^\s*' + re.escape(key) + r'\s*=')
        line = f'{key} = {value}'
        include_at = max((index for index, text in enumerate(lines)
                          if text.lstrip().startswith('#include')), default=-1)
        kept = []
        replaced = False
        for index, text in enumerate(lines):
            if not pattern.match(text):
                kept.append(text)
                continue
            # 一旦被后面的 #include 覆盖，这条旧值就是死代码，直接丢掉。
            if not replaced and index > include_at:
                kept.append(line)
                replaced = True
        if not replaced:
            kept.append(line)
        lines = kept
    config.write_text('\n'.join(lines).rstrip('\n') + '\n', encoding='utf-8')


def configure_project(variant):
    configs = root / 'macos' / 'Runner' / 'Configs'
    apply_settings(configs / 'AppInfo.xcconfig', {
        'PRODUCT_NAME': variant.slug,
        'PRODUCT_BUNDLE_IDENTIFIER': 'com.duanju.' + variant.slug,
        'MACOSX_DEPLOYMENT_TARGET': DEPLOYMENT_TARGET,
    })
    # CI 没有签名证书，必须关掉 Xcode 签名；`flutter build macos --no-codesign`
    # 这个开关在 Flutter 里时有时无，不能依赖命令行，直接写构建设置最稳。
    apply_settings(configs / 'Release.xcconfig', {
        'CODE_SIGNING_ALLOWED': 'NO',
        'CODE_SIGNING_REQUIRED': 'NO',
        'CODE_SIGN_IDENTITY': '-',
        'CODE_SIGN_STYLE': 'Manual',
        'ENABLE_HARDENED_RUNTIME': 'NO',
    })
    patch_deployment_target()
    configure_entitlements()
    configure_info_plist()


def configure_info_plist():
    """补齐 macOS 的 Info.plist 项，和 iOS 工程里的那几条对齐。

    ATS：不少站源的接口和媒体仍是明文 http，iOS 侧已 `NSAllowsArbitraryLoads`，
    macOS 默认拦截，会表现成「目录出得来、一放就失败」。
    本地网络：macOS 15 起发现/注册 Bonjour 需要用途说明，缺了局域网发现与
    投播会静默失败。
    """
    path = root / 'macos' / 'Runner' / 'Info.plist'
    if not path.is_file():
        return
    data = plistlib.loads(path.read_bytes())
    wanted = {
        'NSAppTransportSecurity': {
            'NSAllowsArbitraryLoads': True,
            'NSAllowsLocalNetworking': True,
        },
        'NSLocalNetworkUsageDescription': '用于发现局域网设备、同步追剧进度和推送播放，以及读取设备内播放服务。',
        'NSBonjourServices': ['_zgj-link._tcp'],
    }
    missing = {key: value for key, value in wanted.items() if data.get(key) != value}
    if not missing:
        return
    data.update(missing)
    path.write_bytes(plistlib.dumps(data))
    print('[macos] Info.plist 补入 ' + '、'.join(missing), flush=True)


def patch_deployment_target():
    """`flutter create` 生成的工程可能还是 10.14，ffmpeg 的 pod 要求 10.15+。"""
    project = root / 'macos' / 'Runner.xcodeproj' / 'project.pbxproj'
    if not project.is_file():
        return
    text = project.read_text(encoding='utf-8')
    patched = re.sub(r'MACOSX_DEPLOYMENT_TARGET = [\d.]+;',
                     f'MACOSX_DEPLOYMENT_TARGET = {DEPLOYMENT_TARGET};', text)
    if patched != text:
        project.write_text(patched, encoding='utf-8')
        print(f'[macos] project.pbxproj 部署目标改为 {DEPLOYMENT_TARGET}', flush=True)


def configure_entitlements():
    """关掉 App Sandbox。

    应用要直连各家站源、把下载的视频写到用户指定目录。Flutter 默认模板是
    「沙箱开启 + 只给 network.server」，release 包连外网都出不去，表现就是
    「装上打不开任何剧」。Windows 版本来就没有沙箱，这里对齐桌面端行为。
    """
    files = {
        'DebugProfile.entitlements': {
            'com.apple.security.app-sandbox': False,
            'com.apple.security.cs.allow-jit': True,
            'com.apple.security.network.server': True,
            'com.apple.security.files.user-selected.read-write': True,
        },
        'Release.entitlements': {
            'com.apple.security.app-sandbox': False,
            'com.apple.security.files.user-selected.read-write': True,
        },
    }
    for name, values in files.items():
        target = root / 'macos' / 'Runner' / name
        if not target.parent.is_dir():
            continue
        current = plistlib.loads(target.read_bytes()) if target.is_file() else {}
        if current == values:
            continue
        target.write_bytes(plistlib.dumps(values))
        print(f'[macos] 重写 {name}（关闭沙箱）', flush=True)


def ensure_podfile(flutter):
    """保证 macos/Podfile 存在且部署目标不低于 DEPLOYMENT_TARGET。

    Podfile 不随 `flutter create` 生成，是 Flutter 首次构建时从 SDK 模板复制
    过来的；这里提前复制，好在 pod install 之前改掉 platform 行。
    """
    podfile = root / 'macos' / 'Podfile'
    if not podfile.is_file():
        template = (Path(flutter).resolve().parent.parent / 'packages' / 'flutter_tools'
                    / 'templates' / 'cocoapods' / 'Podfile-macos')
        if not template.is_file():
            raise SystemExit('Flutter SDK 里找不到 macOS Podfile 模板：' + str(template))
        shutil.copy2(template, podfile)
        print('[macos] 已从 SDK 模板生成 macos/Podfile', flush=True)
    text = podfile.read_text(encoding='utf-8')
    patched = re.sub(r"(?m)^platform :osx, '[^']*'",
                     f"platform :osx, '{DEPLOYMENT_TARGET}'", text)
    if patched != text:
        podfile.write_text(patched, encoding='utf-8')


def repair_media_kit(env):
    """media_kit 的 macOS 包靠 `make` 生成 mpv 框架符号链接。

    pub 缓存里的 create_framework_symlinks.sh 若是 CRLF 行尾，sh 会直接报
    `: not found`；符号链接没建出来时链接期才报缺 mpv。这里按需修一次。
    """
    package = package_path('media_kit_libs_macos_video')
    if package is None:
        return
    directory = package / 'macos'
    script = directory / 'create_framework_symlinks.sh'
    linked = directory / 'Frameworks' / '.symlinks' / 'mpv' / 'macos'
    if not script.is_file() or linked.exists():
        return
    script.write_bytes(script.read_bytes().replace(b'\r\n', b'\n'))
    (directory / 'Frameworks' / '.symlinks' / 'mpv').mkdir(parents=True, exist_ok=True)
    run(['sh', 'create_framework_symlinks.sh', 'Frameworks/Mpv.xcframework',
         'Frameworks/.symlinks/mpv'], env=env, cwd=directory)


def build_arguments(flutter):
    help_text = subprocess.run([flutter, 'build', 'macos', '--help'],
                               capture_output=True, text=True, check=False).stdout
    return ['--no-codesign'] if '--no-codesign' in help_text else []


def locate_application():
    release = root / 'build' / 'macos' / 'Build' / 'Products' / 'Release'
    applications = sorted(path for path in release.glob('*.app')
                          if path.is_dir()) if release.is_dir() else []
    if not applications:
        raise SystemExit('未生成 macOS 应用包：' + str(release))
    return applications[0]


def rename_application(application, variant):
    if application.name == variant.slug + '.app':
        return application
    target = application.with_name(variant.slug + '.app')
    if target.exists():
        shutil.rmtree(target)
    application.rename(target)
    macos = target / 'Contents' / 'MacOS'
    current = sorted(path for path in macos.iterdir() if path.is_file())
    if len(current) == 1 and current[0].name != variant.slug:
        current[0].rename(macos / variant.slug)
    info = target / 'Contents' / 'Info.plist'
    if info.is_file():
        data = plistlib.loads(info.read_bytes())
        data['CFBundleName'] = variant.slug
        data['CFBundleDisplayName'] = variant.name
        data['CFBundleExecutable'] = variant.slug
        info.write_bytes(plistlib.dumps(data))
    return target


def embed_library(application):
    source = root / 'native' / 'build' / 'darwin' / CORE_LIBRARY
    if not source.is_file():
        raise SystemExit('缺少 macOS 原生库：' + str(source))
    info = subprocess.run(['lipo', '-info', str(source)],
                          capture_output=True, text=True, check=False)
    if info.returncode != 0:
        raise SystemExit('原生核心不是合法的 Mach-O：' + str(source))
    architectures = info.stdout.strip()
    for required in ('arm64', 'x86_64'):
        if required not in architectures:
            raise SystemExit(f'原生核心缺少 {required} 切片（{architectures}），dmg 会只支持一半机器。')
    frameworks = application / 'Contents' / 'Frameworks'
    frameworks.mkdir(parents=True, exist_ok=True)
    library = frameworks / CORE_LIBRARY
    shutil.copy2(source, library)
    run(['install_name_tool', '-id', '@rpath/' + CORE_LIBRARY, str(library)])
    for executable in sorted(path for path in (application / 'Contents' / 'MacOS').iterdir()
                             if path.is_file()):
        listing = subprocess.run(['otool', '-l', str(executable)],
                                 capture_output=True, text=True, check=False).stdout
        if '@executable_path/Frameworks' not in listing:
            subprocess.run(['install_name_tool', '-add_rpath', '@executable_path/Frameworks',
                            str(executable)], check=False)
    symbols = subprocess.run(['nm', '-gU', str(library)],
                             capture_output=True, text=True, check=True).stdout
    for symbol in ('_DuanjuRequest', '_DuanjuFree'):
        if symbol not in symbols:
            raise SystemExit('macOS 原生库缺少 FFI 入口：' + symbol)
    codesign = shutil.which('codesign')
    if codesign:
        # 关闭 Xcode 签名后产物是未签名的，Apple 芯片要求所有代码都有签名，
        # 这里按由内到外的顺序补 ad-hoc 签名，最后再签应用包本体。
        nested = sorted(path for path in frameworks.iterdir()
                        if path.suffix in ('.framework', '.dylib') or path.is_file())
        for item in nested:
            subprocess.run([codesign, '--force', '--sign', '-', '--timestamp=none',
                            str(item)], check=False)
        for item in sorted(path for path in (application / 'Contents' / 'MacOS').iterdir()
                           if path.is_file()):
            subprocess.run([codesign, '--force', '--sign', '-', '--timestamp=none',
                            str(item)], check=False)
        subprocess.run([codesign, '--force', '--sign', '-', '--timestamp=none',
                        str(application)], check=False)


def main():
    parser = argparse.ArgumentParser(description='构建短剧视界 / 全剧视界 macOS 应用与 dmg')
    parser.add_argument('--cn-mirrors', action='store_true', help='使用 Flutter 中国镜像')
    add_variant_argument(parser)
    options = parser.parse_args()
    variant = BuildVariant(options.all_sources)
    if platform.system() != 'Darwin':
        raise SystemExit('macOS 构建需要 macOS 与 Xcode Command Line Tools。')
    environment = os.environ.copy()
    environment.setdefault('GOPROXY', 'https://goproxy.cn,direct')
    environment.setdefault('GOSUMDB', 'off')
    flutter = shutil.which('flutter')
    if not flutter:
        raise SystemExit('请先将 Flutter SDK 的 bin 目录加入 PATH。')
    pod = shutil.which('pod')
    if not pod:
        raise SystemExit('请先安装 CocoaPods（brew install cocoapods）。')
    with china_mirror_environment(environment, options.cn_mirrors, gradle=False) as env:
        with mirrored_pub_lockfile(root, env):
            ensure_platform(flutter, env)
            configure_project(variant)
            ensure_podfile(flutter)
            run([sys.executable, root / 'scripts' / 'build_native.py', '--platform', 'darwin',
                 *variant.arguments], env=env)
            run([flutter, 'pub', 'get', '--enforce-lockfile'], env=env)
            # ffmpeg_kit_flutter_new 的 macOS 框架是 pod install 时按需下载的，
            # 缓存命中时会被跳过；出包前先自己补上，并清掉毒性 Pods 工程。
            ensure_pods('macos', root, env)
            run([pod, 'install'], env=env, cwd=root / 'macos')
            repair_media_kit(env)
            build_env = dict(env, CODE_SIGNING_ALLOWED='NO', CODE_SIGNING_REQUIRED='NO',
                             CODE_SIGN_IDENTITY='-')
            run([flutter, 'build', 'macos', '--release', '--no-pub',
                 *build_arguments(flutter), *variant.flutter_arguments], env=build_env)
            application = rename_application(locate_application(), variant)
            embed_library(application)
            run([sys.executable, root / 'scripts' / 'package_release.py', '--platform', 'macos',
                 *variant.arguments], env=env)
    print('完成：' + str(root / 'dist' / 'macos'), flush=True)


if __name__ == '__main__':
    main()
