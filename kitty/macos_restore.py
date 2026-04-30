#!/usr/bin/env python
# License: GPL v3 Copyright: 2026

import json
import os
import shutil
import tempfile
from collections.abc import Callable, Mapping
from contextlib import suppress
from time import time
from typing import TYPE_CHECKING, Any

from .constants import cache_dir, is_macos
from .fast_data_types import current_focused_os_window_id, last_focused_os_window_id
from .session import default_save_as_session_opts
from .utils import log_error

if TYPE_CHECKING:
    from .boss import Boss
    from .window import Window


SNAPSHOT_VERSION = 1
SESSION_FILE_NAME = 'session.kitty-session'
MANIFEST_FILE_NAME = 'manifest.json'
SCROLLBACK_DIR_NAME = 'scrollback'
RESTORE_NOTICE_PREFIX = 'Restored scrollback '


def restore_root() -> str:
    return os.path.join(cache_dir(), 'restore')


def latest_snapshot_dir() -> str:
    return os.path.join(restore_root(), 'latest')


def latest_session_path() -> str:
    return os.path.join(latest_snapshot_dir(), SESSION_FILE_NAME)


def latest_manifest_path() -> str:
    return os.path.join(latest_snapshot_dir(), MANIFEST_FILE_NAME)


def restore_mode(opts: Any) -> str:
    return getattr(opts, 'macos_restore_session', 'quit')


def should_restore_mode(opts: Any) -> bool:
    return restore_mode(opts) != 'never'


def should_save_on_quit(opts: Any) -> bool:
    return restore_mode(opts) == 'quit'


def should_save_on_last_window_close(opts: Any) -> bool:
    return restore_mode(opts) == 'quit'


def should_restore_on_startup(opts: Any, cli_opts: Any) -> bool:
    if not is_macos:
        return False
    if not should_restore_mode(opts):
        return False
    if getattr(cli_opts, 'session', ''):
        return False
    if getattr(cli_opts, 'args', None):
        return False
    if getattr(opts, 'startup_session', ''):
        return False
    return os.path.exists(latest_session_path())


def session_path_for_startup(opts: Any, cli_opts: Any) -> str | None:
    return latest_session_path() if should_restore_on_startup(opts, cli_opts) else None


def strip_restore_notice(text: str) -> str:
    lines = text.splitlines(keepends=True)
    return ''.join(line for line in lines if not line.rstrip('\r\n').startswith(RESTORE_NOTICE_PREFIX))


def extra_launch_data_callback(snapshot_dir: str) -> Callable[[Any], Mapping[str, Any] | None]:
    scrollback_dir = os.path.join(snapshot_dir, SCROLLBACK_DIR_NAME)
    os.makedirs(scrollback_dir, exist_ok=True)
    written: set[int] = set()

    def callback(window: 'Window') -> Mapping[str, Any] | None:
        if window.id in written:
            return {'scrollback_ref': os.path.join(SCROLLBACK_DIR_NAME, f'{window.id}.ansi')}
        written.add(window.id)
        text = strip_restore_notice(window.as_text(as_ansi=True, add_history=True, add_wrap_markers=True))
        if not text:
            return None
        path = os.path.join(scrollback_dir, f'{window.id}.ansi')
        with open(path, 'wb') as f:
            f.write(text.encode('utf-8'))
        return {'scrollback_ref': os.path.join(SCROLLBACK_DIR_NAME, f'{window.id}.ansi')}

    return callback


def serialize_boss_to_snapshot(boss: 'Boss', snapshot_dir: str) -> None:
    session_path = os.path.join(snapshot_dir, SESSION_FILE_NAME)
    ser_opts = default_save_as_session_opts()
    matched_windows = frozenset(boss.match_windows(ser_opts.match)) if ser_opts.match else None
    order = {current_focused_os_window_id(): 2, last_focused_os_window_id(): 1}
    callback = extra_launch_data_callback(snapshot_dir)
    lines: list[str] = []
    for i, os_window_id in enumerate(sorted(boss.os_window_map, key=lambda wid: order.get(wid, 0))):
        tm = boss.os_window_map[os_window_id]
        lines.extend(tm.serialize_state_as_session(
            session_path, matched_windows, ser_opts, is_first=i == 0, extra_launch_data=callback))
    with open(session_path, 'w', encoding='utf-8') as f:
        f.write('\n'.join(lines))
    with open(os.path.join(snapshot_dir, MANIFEST_FILE_NAME), 'w', encoding='utf-8') as f:
        json.dump({
            'version': SNAPSHOT_VERSION,
            'created_at': time(),
            'session': SESSION_FILE_NAME,
        }, f)


def save_snapshot(boss: 'Boss') -> None:
    if not is_macos:
        return
    if not should_save_on_quit(boss.opts):
        return
    if not boss.os_window_map:
        with suppress(FileNotFoundError):
            shutil.rmtree(latest_snapshot_dir())
        return
    root = restore_root()
    os.makedirs(root, exist_ok=True)
    tmpdir = tempfile.mkdtemp(prefix='snapshot-', dir=root)
    try:
        serialize_boss_to_snapshot(boss, tmpdir)
        latest = latest_snapshot_dir()
        olddir = ''
        if os.path.lexists(latest):
            olddir = tempfile.mkdtemp(prefix='old-snapshot-', dir=root)
            os.rmdir(olddir)
            os.replace(latest, olddir)
        os.replace(tmpdir, latest)
        tmpdir = ''
        if olddir:
            shutil.rmtree(olddir, ignore_errors=True)
    except Exception as err:
        log_error(f'Failed to save macOS restore snapshot: {err}')
        raise
    finally:
        if tmpdir:
            shutil.rmtree(tmpdir, ignore_errors=True)
