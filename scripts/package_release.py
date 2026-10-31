import argparse
import os
import plistlib
import re
import shutil
import subprocess
import sys
import zipfile
from pathlib import Path

from app_build import BuildVariant, add_variant_argument

root = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument('--platform', choices=['android', 'windows', 'macos'], required=True)
parser.add_argument('--abi', action='append', choices=['arm64-v8a', 'armeabi-v7a', 'x86_64'])
parser.add_argument('--identity', default='',
                    help='macOS 签名身份；默认 ad-hoc 签名（-），有 Developer ID 时传证书名')
add_variant_argument(parser)
options = parser.parse_args()
variant = BuildVariant(options.all_sources)
match = re.search(r'^version:\s*([\w.+-]+)\s*$', (root / 'pubspec.yaml').read_text(encoding='utf-8'), re.MULTILINE)
if not match:
    raise SystemExit('pubspec.yaml 缺少合法版本号。')
version = match.group(1)
output = root / 'dist' / options.platform
output.mkdir(parents=True, exist_ok=True)
artifacts = []


def run(arguments, cwd=None):
    print('+ ' + ' '.join(str(item) for item in arguments), flush=True)
    subprocess.run([str(item) for item in arguments], cwd=cwd, check=True)


def macho_architectures(path):
    result = subprocess.run(['lipo', '-info', str(path)], capture_output=True, text=True)
    if result.returncode != 0:
        raise SystemExit(f'不是 Mach-O 文件：{path}')
    return result.stdout.strip()


def verify_macos_application(application):
    info = plistlib.loads((application / 'Contents' / 'Info.plist').read_bytes())
    executable = application / 'Contents' / 'MacOS' / info['CFBundleExecutable']
    if not executable.is_file() or not os.access(executable, os.X_OK):
        raise SystemExit('macOS 包缺少可执行文件。')
    # 只出一个 dmg 就得两片都有：Apple 芯片原生跑，Intel 机器也原生跑。
    architectures = macho_architectures(executable)
    if 'arm64' not in architectures or 'x86_64' not in architectures:
        raise SystemExit('macOS 包应同时包含 arm64 和 x86_64：' + architectures)
    frameworks = application / 'Contents' / 'Frameworks'
    for required in ['App.framework', 'FlutterMacOS.framework']:
        if not (frameworks / required).is_dir():
            raise SystemExit('macOS 包缺少框架：' + required)
    # Go 核心在 dmg 里是独立 dylib（不是 iOS 那样链进主程序），符号必须能查到，
    # 否则装上一启动就是「找不到 DuanjuRequest」。
    library = frameworks / 'libduanju_core.dylib'
    if not library.is_file():
        raise SystemExit('macOS 包缺少原生核心：libduanju_core.dylib')
    core_architectures = macho_architectures(library)
    if 'arm64' not in core_architectures or 'x86_64' not in core_architectures:
        raise SystemExit('原生核心不是通用二进制：' + core_architectures)
    symbols = subprocess.run(['nm', '-gU', str(library)], capture_output=True, text=True).stdout
    for symbol in ['_DuanjuRequest', '_DuanjuFree']:
        if symbol not in symbols:
            raise SystemExit('原生核心缺少 FFI 入口：' + symbol)
    # 播放依赖这两个框架，缺了能启动但放不了；不同 Flutter/插件版本下它们可能被
    # 静态链接进主程序而不出现在 Frameworks 里，所以只告警不拦发布。
    for optional in ['Mpv.framework', 'ffmpegkit.framework']:
        if not (frameworks / optional).is_dir():
            print(f'警告：Frameworks 里没有 {optional}，确认播放链路是静态链入的。', flush=True)


def sign_macos_application(application, identity):
    # 没有 Developer ID 时用 ad-hoc 签名（-）：未公证的 app 首次打开需要用户
    # 手动去掉隔离属性，但至少能通过 Gatekeeper 的签名完整性检查。
    flags = ['codesign', '--force', '--sign', identity or '-']
    if identity and identity != '-':
        flags += ['--options', 'runtime', '--timestamp']
    for framework in sorted((application / 'Contents' / 'Frameworks').glob('*')):
        run(flags + [framework])
    run(flags + [application])


def draw_dmg_background(path, display):
    try:
        from PIL import Image, ImageDraw, ImageFont
    except ImportError:
        return None
    width, height = 620, 380
    image = Image.new('RGB', (width, height), '#F5F9FC')
    draw = ImageDraw.Draw(image)
    draw.rectangle((0, 0, width, 96), fill='#E8F1FA')
    arrow = '#B9CBDD'
    draw.line((300, 178, 400, 178), fill=arrow, width=6)
    draw.line((384, 166, 402, 178), fill=arrow, width=6)
    draw.line((384, 190, 402, 178), fill=arrow, width=6)
    font_path = None
    for candidate in ('/System/Library/Fonts/PingFang.ttc',
                      '/System/Library/Fonts/Hiragino Sans GB.ttc',
                      '/System/Library/Fonts/STHeiti Medium.ttc'):
        if Path(candidate).exists():
            font_path = candidate
            break
    title = ImageFont.truetype(font_path, 26) if font_path else ImageFont.load_default()
    hint = ImageFont.truetype(font_path, 15) if font_path else ImageFont.load_default()
    draw.text((width / 2, 48), display, font=title, fill='#3B4552', anchor='mm')
    draw.text((width / 2, 292), f'把「{display}」拖到右侧 Applications 完成安装',
              font=hint, fill='#7A8796', anchor='mm')
    image.save(path)
    return path


def build_macos_dmg(display, staging, staged, target):
    background = draw_dmg_background(staging.parent / 'dmg-background.png', display)
    installed = subprocess.run([sys.executable, '-c', 'import dmgbuild'],
                               capture_output=True).returncode == 0
    if installed:
        settings = staging.parent / 'dmg-settings.py'
        layout = [
            f'volume_name = {display!r}',
            "format = 'UDZO'",
            f'files = [{staged.name!r}]',
            "symlinks = {'Applications': '/Applications'}",
            "default_view = 'icon-view'",
            'icon_size = 128',
            'text_size = 13',
            'window_rect = ((240, 140), (620, 380))',
            f'icon_locations = {{{staged.name!r}: (155, 175), "Applications": (465, 175)}}',
            'show_status_bar = False',
            'show_tab_view = False',
            'show_toolbar = False',
            'show_pathbar = False',
            'show_sidebar = False',
            'arrange_by = None',
        ]
        if background is not None:
            layout.append(f'background = {str(background)!r}')
        settings.write_text('\n'.join(layout) + '\n')
        run([sys.executable, '-m', 'dmgbuild', '-s', settings, display, target], cwd=staging)
    else:
        print('未安装 dmgbuild，退回普通 dmg（没有拖拽安装的窗口布局）；'
              '需要时执行 pip install dmgbuild。', flush=True)
        os.symlink('/Applications', staging / 'Applications')
        run(['hdiutil', 'create', '-volname', display, '-srcfolder', staging, '-ov',
             '-fs', 'HFS+', '-format', 'UDZO', '-imagekey', 'zlib-level=9', target])


def package_macos(application):
    display = variant.name
    staging = root / 'build' / 'macos-dmg'
    if staging.exists():
        shutil.rmtree(staging)
    staging.mkdir(parents=True)
    staged = staging / f'{display}.app'
    shutil.copytree(application, staged, symlinks=True)
    plist_path = staged / 'Contents' / 'Info.plist'
    info = plistlib.loads(plist_path.read_bytes())
    info['CFBundleDisplayName'] = display
    info['CFBundleName'] = display
    info['CFBundleIdentifier'] = f'com.duanju.{variant.slug}'
    plist_path.write_bytes(plistlib.dumps(info))
    sign_macos_application(staged, options.identity)
    verify_macos_application(staged)
    target = output / f'{variant.slug}-{version}-macos-universal.dmg'
    if target.exists():
        target.unlink()
    build_macos_dmg(display, staging, staged, target)
    # 挂起来验证一遍：用户拿到 dmg 只会看到这两个图标，缺了就等于安装不了。
    mount = root / 'build' / 'macos-dmg-mount'
    if mount.exists():
        shutil.rmtree(mount)
    mount.mkdir(parents=True)
    try:
        run(['hdiutil', 'attach', target, '-nobrowse', '-readonly', '-mountpoint', mount])
        if not (mount / f'{display}.app').is_dir():
            raise SystemExit('dmg 里缺少应用：' + display)
        if not (mount / 'Applications').is_symlink():
            raise SystemExit('dmg 里缺少 Applications 快捷方式。')
    finally:
        subprocess.run(['hdiutil', 'detach', str(mount)], capture_output=True)
    return target

if options.platform == 'android':
    for abi in options.abi or ['arm64-v8a', 'armeabi-v7a', 'x86_64']:
        source = root / 'build' / 'app' / 'outputs' / 'flutter-apk' / f'app-{abi}-release.apk'
        if not source.is_file():
            raise SystemExit('缺少 APK：' + str(source))
        with zipfile.ZipFile(source) as archive:
            names = set(archive.namelist())
            required = [f'lib/{abi}/{library}' for library in
                        ['libduanju_core.so', 'libflutter.so', 'libapp.so', 'libmpv.so', 'libffmpegkit.so']]
            missing = set(required) - names
            if missing:
                raise SystemExit('APK 缺少原生库：' + ', '.join(sorted(missing)))
        target = output / f'{variant.slug}-{version}-{abi}.apk'
        shutil.copy2(source, target)
        artifacts.append(target)
elif options.platform == 'windows':
    bundle = root / 'build' / 'windows' / 'x64' / 'runner' / 'Release'
    required = ['quanjushijie.exe', 'duanju_core.dll', 'flutter_windows.dll', 'libffmpegkit.dll',
                'libmpv-2.dll', 'msvcp140.dll', 'vcruntime140.dll',
                'data/icudtl.dat', 'data/app.so']
    missing = [name for name in required if not (bundle / name).is_file()]
    if missing:
        raise SystemExit('Windows 安装包缺少文件：' + ', '.join(missing))
    target = output / f'{variant.slug}-{version}-windows-x64.zip'
    with zipfile.ZipFile(target, 'w', zipfile.ZIP_DEFLATED) as archive:
        for source in sorted(bundle.rglob('*')):
            if source.is_file():
                relative = source.relative_to(bundle).as_posix()
                if relative == 'quanjushijie.exe':
                    relative = variant.slug + '.exe'
                archive.write(source, relative)
    artifacts.append(target)
elif options.platform == 'macos':
    release = root / 'build' / 'macos' / 'Build' / 'Products' / 'Release'
    application = release / f'{variant.slug}.app'
    if not application.is_dir():
        candidates = sorted(path for path in release.glob('*.app') if path.is_dir()) \
            if release.is_dir() else []
        if not candidates:
            raise SystemExit('缺少 macOS 应用：' + str(application))
        application = candidates[0]
    verify_macos_application(application)
    artifacts.append(package_macos(application))

for artifact in artifacts:
    print(artifact)
