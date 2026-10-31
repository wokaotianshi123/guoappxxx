"""准备 ffmpeg_kit_flutter_new 的 iOS / macOS 预编译原生框架。

这个插件的 podspec 用 prepare_command 在 `pod install` 时下载官方 release 里的
预编译框架（iOS 约 100 MB，macOS 略小），装不上就编不过，报的是：

    'ffmpegkit/FFmpegKitConfig.h' file not found

坑在于 `pod install` 只在「确实需要安装」的时候才执行 prepare_command。当
`ios/Pods` 或 `macos/Pods` 被 CI 缓存还原、`Podfile.lock` 与已装清单一致时，
CocoaPods 认为插件早就装好了，直接跳过下载；而框架实际落在 pub 缓存（另一个
缓存），两边一旦不同步就成了「pod install 秒过、编译期才炸」。所以出包前自己
先把框架准备好，不依赖 `pod install` 的副作用。

另外，如果这份 Pods 是在框架还缺失的时候生成的，里面的 FRAMEWORK_SEARCH_PATHS
必然不含 ffmpegkit，光把框架补上也没用，必须让 pod install 重新生成工程；
`pods_need_regeneration()` 负责认出这种「毒性」工程。
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
from pathlib import Path
from urllib.parse import unquote, urlparse
from urllib.request import url2pathname

PACKAGE = 'ffmpeg_kit_flutter_new_min_gpl'

# 与插件 podspec / setup 脚本里的清单保持一致，少一个都会在链接期报缺符号。
FRAMEWORKS = (
    'ffmpegkit',
    'libavcodec',
    'libavdevice',
    'libavfilter',
    'libavformat',
    'libavutil',
    'libswresample',
    'libswscale',
)

# GitHub release 直连在国内经常超时；脚本支持用环境变量换成镜像地址，
# 这里按顺序试，第一个成功为止。空串表示用脚本自己的默认地址。
MIRRORS = ('', 'https://cfkua.wokaotianshi.eu.org/', 'https://ghfast.top/', 'https://gh-proxy.com/')

PLATFORMS = {
    'ios': {
        'directory': 'ios',
        'script': 'setup_ios.sh',
        'env': 'FFMPEG_KIT_IOS_URL',
        'suffix': '.xcframework',
    },
    'macos': {
        'directory': 'macos',
        'script': 'setup_macos.sh',
        'env': 'FFMPEG_KIT_MACOS_URL',
        'suffix': '.framework',
    },
}


def project_root() -> Path:
    return Path(__file__).resolve().parents[1]


def package_path(name: str, root: Path | None = None) -> Path | None:
    """从 .dart_tool/package_config.json 里取依赖包的真实目录。"""
    config = (root or project_root()) / '.dart_tool' / 'package_config.json'
    if not config.is_file():
        return None
    for package in json.loads(config.read_text())['packages']:
        if package['name'] == name:
            location = Path(url2pathname(unquote(urlparse(package['rootUri']).path)))
            return location if location.is_absolute() else (config.parent / location).resolve()
    return None


def frameworks_present(package: Path, platform: str) -> bool:
    spec = PLATFORMS[platform]
    base = package / spec['directory'] / 'Frameworks'
    return base.is_dir() and all((base / f'{name}{spec["suffix"]}').is_dir() for name in FRAMEWORKS)


def _default_url(script: Path) -> str:
    """从 setup 脚本里读默认下载地址，免得把版本号抄第二遍。"""
    for line in script.read_text(errors='ignore').splitlines():
        if line.startswith('DEFAULT_URL='):
            return line.split('=', 1)[1].strip().strip('"')
    return ''


def prepare(platform: str, root: Path | None = None, env: dict | None = None) -> bool:
    """确保框架已就位。返回 True 表示本次真的下载了。"""
    spec = PLATFORMS[platform]
    package = package_path(PACKAGE, root)
    if package is None:
        raise SystemExit(f'找不到依赖 {PACKAGE}，请先执行 flutter pub get。')
    if frameworks_present(package, platform):
        print(f'[ffmpeg] {platform} 原生框架已就位，跳过下载。', flush=True)
        return False
    script = package / 'scripts' / spec['script']
    if not script.is_file():
        raise SystemExit(f'{PACKAGE} 缺少 scripts/{spec["script"]}，无法准备 {platform} 原生框架。')
    workdir = package / spec['directory']
    url = _default_url(script)
    failures = []
    for prefix in MIRRORS:
        child = dict(env or os.environ)
        if prefix and url:
            child[spec['env']] = prefix + url
        else:
            child.pop(spec['env'], None)
        source = prefix or 'GitHub 直连'
        print(f'[ffmpeg] 下载 {platform} 原生框架（{source}）…', flush=True)
        result = subprocess.run(['bash', str(script)], cwd=workdir, env=child)
        if frameworks_present(package, platform):
            print(f'[ffmpeg] {platform} 原生框架准备完成。', flush=True)
            return True
        failures.append(f'{source}（退出码 {result.returncode}）')
    raise SystemExit(
        f'{platform} 原生框架下载失败：{"; ".join(failures)}。'
        f'可用 {spec["env"]} 指定可达的镜像后重试，否则构建会在编译期报 '
        "'ffmpegkit/FFmpegKitConfig.h' file not found。"
    )


def pods_need_regeneration(platform: str, root: Path | None = None) -> bool:
    """判断现有的 Pods 工程是不是在框架缺失时生成的（那样搜索路径永远补不上）。"""
    pods = (root or project_root()) / platform / 'Pods'
    if not pods.is_dir():
        return False
    support = pods / 'Target Support Files'
    configs = list(support.glob('ffmpeg_kit_flutter_new*/*.xcconfig'))
    if not configs:
        # 工程里根本没有这个 pod 的配置，说明是别的 pod 变动，交给 pod install 自己处理。
        return False
    for config in configs:
        try:
            if 'ffmpegkit' in config.read_text(errors='ignore'):
                return False
        except OSError:
            return False
    return True


def ensure_pods(platform: str, root: Path | None = None, env: dict | None = None) -> None:
    """出包前的统一入口：补框架，必要时丢掉旧的 Pods 让 pod install 重新生成。"""
    root = root or project_root()
    downloaded = prepare(platform, root, env)
    if downloaded or pods_need_regeneration(platform, root):
        pods = root / platform / 'Pods'
        if pods.is_dir():
            print(f'[ffmpeg] {platform}/Pods 生成时框架缺失，丢弃后重新 pod install。', flush=True)
            shutil.rmtree(pods)


def _main(argv):
    targets = [item for item in argv if item in PLATFORMS] or list(PLATFORMS)
    download = '--download' in argv
    for target in targets:
        package = package_path(PACKAGE)
        if package is None:
            print(f'{target}: 还没装 {PACKAGE}，先跑 flutter pub get')
            continue
        location = package / PLATFORMS[target]['directory'] / 'Frameworks'
        present = frameworks_present(package, target)
        print(f'{target}: 框架{"" if present else "不"}在 {location}')
        if download and not present:
            prepare(target)


if __name__ == '__main__':
    import sys

    _main(sys.argv[1:])
