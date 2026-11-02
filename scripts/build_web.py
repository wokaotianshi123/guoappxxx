import argparse
import hashlib
import os
import platform
import re
import shutil
import subprocess
import urllib.request
import zipfile
from pathlib import Path

root = Path(__file__).resolve().parents[1]
native = root / 'native'
parser = argparse.ArgumentParser(description='构建短剧视界 / 全剧视界本地服务版')
parser.add_argument('--edition', action='append', choices=['duanjushijie', 'quanjushijie'],
                    help='只构建指定版本；默认两个版本都构建')
options = parser.parse_args()

version = re.search(r'^version:\s*(\S+)', (root / 'pubspec.yaml').read_text(), re.MULTILINE).group(1)
editions = options.edition or ['duanjushijie', 'quanjushijie']


def is_all_sources(slug):
    return slug == 'quanjushijie'


def edition_name(slug):
    return '全剧视界' if is_all_sources(slug) else '短剧视界'

environment = os.environ.copy()
environment.setdefault('GOPROXY', 'https://goproxy.cn,direct')
environment.setdefault('GOSUMDB', 'off')
environment['CGO_ENABLED'] = '0'
go = shutil.which('go')
if not go:
    raise SystemExit('请先安装 Go 1.25.0 或更新版本。')
bootstrap = environment.copy()
bootstrap['GOSUMDB'] = os.environ.get('GOSUMDB', 'sum.golang.org')
toolchain = subprocess.check_output([go, 'env', 'GOROOT'], cwd=native, env=bootstrap, text=True).strip()
go = str(Path(toolchain) / 'bin' / ('go.exe' if platform.system() == 'Windows' else 'go'))

targets = [
    ('windows', 'amd64'),
    ('linux', 'amd64'),
    ('darwin', 'amd64'),
    ('darwin', 'arm64'),
]


def binary_name(slug, goos, goarch):
    if goos == 'windows':
        return slug + '-windows.exe'
    if goos == 'linux':
        return slug + '-linux'
    return slug + '-macos-arm' if goarch == 'arm64' else slug + '-macos-intel'


def build_binary(slug, all_sources, goos, goarch):
    name = binary_name(slug, goos, goarch)
    destination = root / 'build' / 'web' / name
    destination.parent.mkdir(parents=True, exist_ok=True)
    flags = '-s -w -X duanjuapp/native/core.buildAllSources=' + str(all_sources).lower() + \
        ' -X main.editionSlug=' + slug + ' -X main.appVersion=' + version
    build_environment = dict(environment, GOOS=goos, GOARCH=goarch)
    print('Building ' + name, flush=True)
    subprocess.run([go, 'build', '-trimpath', '-ldflags=' + flags, '-o', str(destination), './server'],
                   cwd=native, env=build_environment, check=True)
    return destination, name


def start_script(slug):
    return '\n'.join([
        '#!/bin/sh',
        'cd "$(dirname "$0")"',
        'case "$(uname -s)" in',
        '  Darwin)',
        '    if [ "$(uname -m)" = "arm64" ]; then',
        '      BIN="./' + slug + '-macos-arm"',
        '    else',
        '      BIN="./' + slug + '-macos-intel"',
        '    fi',
        '    ;;',
        '  *) BIN="./' + slug + '-linux" ;;',
        'esac',
        'chmod +x "$BIN"',
        # 番茄引擎是同目录的另一个可执行文件，由服务进程拉起。部分解压器会丢掉
        # zip 里的可执行位，这里顺手补上，否则番茄源会一直提示「引擎未就绪」。
        'chmod +x ./fanqie_engine-* 2>/dev/null',
        'if ! command -v ffmpeg >/dev/null 2>&1 && [ ! -x "./ffmpeg" ]; then',
        '  echo ""',
        '  echo "没有检测到 ffmpeg。"',
        '  echo "红果等站源是加密的 H.265，浏览器无法直接解码，需要 ffmpeg 转码；其余站源不受影响。"',
        '  echo "安装方法：macOS 执行 brew install ffmpeg；Linux 执行 apt install ffmpeg / yum install ffmpeg。"',
        '  echo "下载静态包（解压出 ffmpeg 放到本目录亦可，可加 https://cfkua.wokaotianshi.eu.org/ 前缀加速）："',
        '  echo "  macOS(Apple 芯片) https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-macos-arm64-gpl.zip"',
        '  echo "  macOS(Intel)     https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-macos-64-gpl.zip"',
        '  echo "  Linux(x86_64)    https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-linux64-gpl.zip"',
        '  echo ""',
        'fi',
        'exec "$BIN" -open',
        '',
    ])


# Windows 静态版 ffmpeg（约 90 MB）。GitHub 直连在国内常常超时，
# 先走加速前缀（cfkua 为首选，实测可用），失败依次回退到其它镜像。
FFMPEG_MIRRORS = [
    'https://cfkua.wokaotianshi.eu.org/https://github.com/GyanD/codexffmpeg/releases/download/7.1/ffmpeg-7.1-essentials_build.zip',
    'https://ghfast.top/https://github.com/GyanD/codexffmpeg/releases/download/7.1/ffmpeg-7.1-essentials_build.zip',
    'https://gh-proxy.com/https://github.com/GyanD/codexffmpeg/releases/download/7.1/ffmpeg-7.1-essentials_build.zip',
    'https://github.com/GyanD/codexffmpeg/releases/download/7.1/ffmpeg-7.1-essentials_build.zip',
]


def windows_script(slug):
    mirror_lines = [line for url in FFMPEG_MIRRORS for line in ('  "' + url + '"', '')]
    return '\r\n'.join([
        '@echo off',
        'setlocal EnableDelayedExpansion',
        'cd /d "%~dp0"',
        'title ' + slug + ' local service',
        'set "BIN=' + slug + '-windows.exe"',
        'set "FF=%~dp0ffmpeg.exe"',
        'set "ZIP=%~dp0ffmpeg-download.zip"',
        'set "FFTMP=%~dp0ffmpeg-temp"',
        'set "NEEDFF=1"',
        'if exist "!FF!" set "NEEDFF=0"',
        'if "!NEEDFF!"=="1" (',
        '  for /f "delims=" %%A in (\'where ffmpeg 2^>nul\') do set "NEEDFF=0"',
        ')',
        'if "!NEEDFF!"=="0" goto :RUN',
        'echo.',
        'echo ================================================================',
        'echo  没有检测到 ffmpeg',
        'echo ----------------------------------------------------------------',
        'echo  红果等站源的视频是加密的 H.265，浏览器既不能解密也不能解码，',
        'echo  必须由 ffmpeg 在服务端解密转码后才能播放；其余站源不受影响。',
        'echo  现在可以自动下载 ffmpeg（约 90 MB）并放到本目录：',
        'echo      %~dp0ffmpeg.exe',
        'echo ================================================================',
        'echo.',
        'set "ANSWER=Y"',
        'where choice >nul 2>nul',
        'if errorlevel 1 (',
        '  set /p "ANSWER=是否现在下载 ffmpeg？直接回车下载，输入 N 跳过："',
        ') else (',
        '  choice /c YN /n /t 30 /d Y /m "是否现在下载 ffmpeg？（Y=下载，N=跳过）"',
        '  if not errorlevel 2 set "ANSWER=Y"',
        '  if errorlevel 2 set "ANSWER=N"',
        ')',
        'if /i "!ANSWER!"=="N" goto :SKIP',
        'call :DOWNLOAD',
        'goto :RUN',
        '',
        ':SKIP',
        'echo.',
        'echo 已跳过下载。加密站源暂时无法播放，其余站源不受影响。',
        'echo 之后可手动把 ffmpeg.exe 放到本目录，或执行 winget install ffmpeg。',
        'goto :RUN',
        '',
        ':DOWNLOAD',
        'set "OK=0"',
        'set "USECURL=0"',
        'where curl >nul 2>nul',
        'if not errorlevel 1 set "USECURL=1"',
        'echo.',
        'echo 正在下载 ffmpeg，视网络情况需要几分钟，请稍候...',
        'for %%U in (',
    ] + mirror_lines + [
        ') do (',
        '  if "!OK!"=="0" (',
        '    echo 下载地址：%%~U',
        '    if exist "!ZIP!" del /q "!ZIP!"',
        '    if "!USECURL!"=="1" (',
        '      curl.exe -L --retry 2 --retry-delay 2 --connect-timeout 20 --max-time 1800 -o "!ZIP!" "%%~U"',
        '    ) else (',
        '      powershell -NoProfile -ExecutionPolicy Bypass -Command "[Net.ServicePointManager]::SecurityProtocol=[Net.SecurityProtocolType]::Tls12; Invoke-WebRequest -Uri \'%%~U\' -OutFile \'!ZIP!\' -UseBasicParsing"',
        '    )',
        '    if exist "!ZIP!" (',
        '      for %%F in ("!ZIP!") do if %%~zF GTR 1000000 set "OK=1"',
        '    )',
        '  )',
        ')',
        'if "!OK!"=="0" (',
        '  echo 下载失败。可稍后手动下载 ffmpeg.exe 放到本目录，或执行 winget install ffmpeg。',
        '  echo 现在照常启动服务，未加密的站源仍可播放。',
        '  goto :EOF',
        ')',
        'echo 下载完成，正在解压...',
        'if exist "!FFTMP!" rd /s /q "!FFTMP!"',
        'md "!FFTMP!" 2>nul',
        'tar.exe -xf "!ZIP!" -C "!FFTMP!" >nul 2>&1',
        'if errorlevel 1 (',
        '  powershell -NoProfile -ExecutionPolicy Bypass -Command "Expand-Archive -LiteralPath \'!ZIP!\' -DestinationPath \'!FFTMP!\' -Force"',
        ')',
        'for /f "delims=" %%F in (\'dir /s /b "!FFTMP!\\ffmpeg.exe" 2^>nul\') do copy /y "%%F" "!FF!" >nul',
        'del /q "!ZIP!" 2>nul',
        'rd /s /q "!FFTMP!" 2>nul',
        'if exist "!FF!" (',
        '  for %%F in ("!FF!") do if %%~zF LSS 1000000 del /q "!FF!"',
        ')',
        'if exist "!FF!" (',
        '  echo ffmpeg 已放到本目录，加密站源现在可以正常播放。',
        ') else (',
        '  echo 解压后没有找到 ffmpeg.exe，请手动下载放到本目录。',
        ')',
        'goto :EOF',
        '',
        ':RUN',
        'if exist "!BIN!" goto :START',
        'echo.',
        'echo 没有找到 !BIN!，请确认压缩包已完整解压后再运行 start.bat。',
        'pause',
        'goto :END',
        '',
        ':START',
        'echo.',
        'echo 正在启动本地服务，浏览器会自动打开 http://127.0.0.1:8000/web/',
        'echo 同一局域网的其它设备可用启动时打印的局域网地址访问。',
        'echo 关闭本窗口或按 Ctrl+C 即可停止服务（换端口：start.bat 后加 -port 8001）。',
        'echo.',
        '"!BIN!" -open %*',
        'echo.',
        'echo 服务已退出。',
        'pause',
        ':END',
        'endlocal',
        '',
    ])


def readme(name, slug):
    return '\n'.join([
        name + ' 本地服务版 ' + version,
        '',
        '使用方法：',
        '  Windows：双击 start.bat',
        '  macOS / Linux：终端执行 sh start.sh',
        '',
        '服务启动后浏览器打开：',
        '  http://127.0.0.1:8000/web/',
        '  同一局域网的其他设备打开：http://<本机局域网 IP>:8000/web/',
        '  启动时会打印所有可用地址；换端口运行：start.sh 后加 -port 8001。',
        '',
        '收藏夹：',
        '  网页顶部右侧「收藏夹」里有播放记录与收藏，可单项删除、批量删除或清空。',
        '  播放页的「收藏」按钮可把当前剧加入收藏，再点一次取消。',
        '  播放记录与收藏保存在数据目录的 library.json，换浏览器、换设备都还在。',
        '',
        '说明：',
        '  数据保存在本目录下的 ' + slug + '-data，直接删除即可清空缓存。',
        '  关闭终端窗口或按 Ctrl+C 即停止服务。',
        '',
        '关于 ffmpeg：',
        '  红果等站源的视频是加密的 H.265，浏览器既不能解密也不能解码，',
        '  必须由服务端调用 ffmpeg 解密并转码成 H.264 才能播放。',
        '  Windows 版已在本目录内置 ffmpeg.exe，开箱即播。',
        '  macOS / Linux 需要本机安装 ffmpeg（装好后重启服务即可）：',
        '    macOS：brew install ffmpeg',
        '    Linux：apt install ffmpeg / yum install ffmpeg',
        '  未检测到 ffmpeg 时其余站源仍可正常播放；启动信息里会写明是否检测到 ffmpeg。',
        '',
    ])


def fetch_ffmpeg():
    """下载 GyanD 7.1 静态包并只取出 ffmpeg.exe，缓存到 build/web/ffmpeg/。

    依次尝试 FFMPEG_MIRRORS；同一版第二次打包直接复用缓存。取不到时退出并
    给出明确原因（ffmpeg.exe 要随 duanjushijie.zip / quanjushijie.zip 一起
    发布，缺了它 Windows 版加密站源就没法播放）。
    """
    cache = root / 'build' / 'web' / 'ffmpeg'
    cache.mkdir(parents=True, exist_ok=True)
    exe = cache / 'ffmpeg.exe'
    if exe.exists() and exe.stat().st_size > 1_000_000:
        print('ffmpeg ready (cached) ' + str(exe), flush=True)
        return exe
    for url in FFMPEG_MIRRORS:
        print('Downloading ffmpeg ' + url, flush=True)
        blob = cache / 'ffmpeg-download.zip'
        try:
            blob.unlink(missing_ok=True)
            urllib.request.urlretrieve(url, blob)
            if blob.stat().st_size < 1_000_000:
                print('ffmpeg download too small, trying next mirror', flush=True)
                continue
            with zipfile.ZipFile(blob) as src:
                for name in src.namelist():
                    if name.lower().endswith('ffmpeg.exe'):
                        with src.open(name) as handle, open(exe, 'wb') as out:
                            shutil.copyfileobj(handle, out)
                        break
            blob.unlink(missing_ok=True)
            if exe.exists() and exe.stat().st_size > 1_000_000:
                print('ffmpeg ready ' + str(exe), flush=True)
                return exe
        except Exception as exc:
            print('ffmpeg mirror failed ' + url + ' (' + str(exc) + ')', flush=True)
    raise SystemExit('所有 ffmpeg 下载源都失败，无法把 ffmpeg.exe 打进发布包。')


def stage_fanqie_engine(staging):
    """把各平台的番茄引擎复制进暂存目录。

    产物由 `scripts/build_fanqie_engine.py` 在各自原生 runner 上编好后，
    CI 会下载到 build/fanqie/<平台>/。缺哪个平台就跳过哪个——番茄源在那台
    机器上仍然显示，只是播放时会提示引擎未就绪，其它站源不受影响。
    """
    engines = root / 'build' / 'fanqie'
    plan = [
        ('windows', 'fanqie_engine.exe', 'fanqie_engine-windows.exe'),
        ('linux', 'fanqie_engine', 'fanqie_engine-linux'),
        ('macos', 'fanqie_engine', 'fanqie_engine-macos-arm'),
        ('macos', 'fanqie_engine', 'fanqie_engine-macos-intel'),
    ]
    for platform_name, source_name, target_name in plan:
        source = engines / platform_name / source_name
        if not source.is_file():
            print('未找到 ' + platform_name + ' 版番茄引擎，本次包内不含该平台引擎。')
            continue
        target = staging / target_name
        shutil.copy2(source, target)
        target.chmod(0o755)


def package_edition(slug):
    output = root / 'dist' / 'web'
    output.mkdir(parents=True, exist_ok=True)
    staging = root / 'build' / 'web' / slug
    if staging.exists():
        shutil.rmtree(staging)
    staging.mkdir(parents=True)
    for goos, goarch in targets:
        source, name = build_binary(slug, is_all_sources(slug), goos, goarch)
        target = staging / name
        shutil.copy2(source, target)
        target.chmod(0o755)
        # 暂存目录里已经有一份，删掉平铺的那份，四个平台加起来能省近百 MB 磁盘。
        source.unlink(missing_ok=True)
    # 番茄内容引擎随包分发：本地服务版会拉起它，番茄站源才能真正出内容。
    # 四个平台的二进制平铺在一个目录里，所以引擎带平台后缀避免同名冲突。
    stage_fanqie_engine(staging)
    # 直接用字节写入，避免 Windows 上把 LF 二次转换成 CR CR LF。
    # start.bat 用 GBK：cmd.exe 按系统 ANSI 代码页读批处理，中文 Windows 上才不会乱码。
    (staging / 'start.sh').write_bytes(start_script(slug).encode('utf-8'))
    (staging / 'start.sh').chmod(0o755)
    (staging / 'start.bat').write_bytes(windows_script(slug).encode('gbk'))
    (staging / 'README.txt').write_text(readme(edition_name(slug), slug), encoding='utf-8')
    # Windows 版随包携带 ffmpeg.exe（GyanD 7.1 静态包解压出的单文件），
    # 加密站源开箱即播；macOS / Linux 二进制不依赖它（用各自系统的 ffmpeg）。
    ffmpeg_exe = fetch_ffmpeg()
    shutil.copy2(ffmpeg_exe, staging / 'ffmpeg.exe')
    archive = output / (slug + '.zip')
    archive.unlink(missing_ok=True)
    with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as bundle:
        for item in sorted(staging.rglob('*')):
            if item.is_file():
                bundle.write(item, slug + '/' + item.relative_to(staging).as_posix())
    print(archive)
    return archive


archives = [package_edition(slug) for slug in editions]
output = root / 'dist' / 'web'
bundle_path = output / 'guoappweb.zip'
bundle_path.unlink(missing_ok=True)
with zipfile.ZipFile(bundle_path, 'w', zipfile.ZIP_DEFLATED) as bundle:
    checksums = []
    for archive in archives:
        bundle.write(archive, archive.name)
        digest = hashlib.sha256()
        with archive.open('rb') as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b''):
                digest.update(chunk)
        checksums.append(f'{digest.hexdigest()}  {archive.name}')
    bundle.writestr('SHA256SUMS.txt', '\n'.join(checksums) + '\n')
print(bundle_path)
