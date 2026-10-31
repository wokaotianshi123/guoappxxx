"""Flutter 中国镜像与 pubspec.lock 适配。

只在构建脚本里用：把 PUB_HOSTED_URL / FLUTTER_STORAGE_BASE_URL 指向国内镜像，
并在 `flutter pub get --enforce-lockfile` 期间临时把 lockfile 里的托管源地址改成
镜像，否则 pub 会认为「lockfile 记的是 pub.dev、当前托管源却是镜像」而直接失败。
退出上下文时无论成功还是异常都会把 lockfile 原样写回，不污染工作区。
"""

from __future__ import annotations

import contextlib
import re
from pathlib import Path

PUB_MIRROR = 'https://pub.flutter-io.cn'
STORAGE_MIRROR = 'https://storage.flutter-io.cn'

# 锁文件里所有 `url:` 行都属于 hosted 依赖的描述段。
_LOCK_URL = re.compile(r'(?m)^(?P<indent>[ \t]*)url:[ \t]*"(?P<url>https?://[^"]+)"[ \t]*$')


@contextlib.contextmanager
def china_mirror_environment(environment, enabled, gradle=False):
    """enabled 为真时给出一份指向国内镜像的环境变量副本。

    外部已经设了 PUB_HOSTED_URL 时予以尊重（CI 里可能配了别的镜像）。
    `gradle` 只是保留给 Android 构建的形参：Gradle 解析的是 maven 仓库，
    改 Pub 镜像管不着，真要换源得改 Gradle 脚本，这里不做。
    """
    if not enabled:
        yield environment
        return
    child = dict(environment)
    child.setdefault('PUB_HOSTED_URL', PUB_MIRROR)
    child.setdefault('FLUTTER_STORAGE_BASE_URL', STORAGE_MIRROR)
    print(f"[mirror] PUB_HOSTED_URL={child['PUB_HOSTED_URL']}", flush=True)
    yield child


@contextlib.contextmanager
def mirrored_pub_lockfile(root, environment):
    """把 lockfile 的托管源临时改成当前 PUB_HOSTED_URL，退出时原样还原。"""
    lock = Path(root) / 'pubspec.lock'
    host = (environment.get('PUB_HOSTED_URL') or '').rstrip('/')
    if not host or not lock.is_file():
        yield lock
        return
    original = lock.read_bytes()
    text = original.decode('utf-8')
    rewritten = _LOCK_URL.sub(
        lambda match: f'{match.group("indent")}url: "{host}"', text
    )
    if rewritten == text:
        yield lock
        return
    print(f'[mirror] pubspec.lock 临时指向 {host}（构建结束后还原）', flush=True)
    # 用字节写入：write_text 在 Windows 上会把 \n 翻成 \r\n，还原时就不是原文件了。
    lock.write_bytes(rewritten.encode('utf-8'))
    try:
        yield lock
    finally:
        lock.write_bytes(original)
