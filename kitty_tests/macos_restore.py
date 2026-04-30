#!/usr/bin/env python
# License: GPL v3 Copyright: 2026

import os
import tempfile
from types import SimpleNamespace

from kitty.macos_restore import (
    RESTORE_NOTICE_PREFIX,
    extra_launch_data_callback,
    latest_session_path,
    should_restore_on_startup,
    should_save_on_last_window_close,
    should_save_on_quit,
    strip_restore_notice,
)

from . import BaseTest


class TestMacOSRestore(BaseTest):

    def test_restore_mode_helpers(self):
        never = SimpleNamespace(macos_restore_session='never', startup_session='')
        quit_ = SimpleNamespace(macos_restore_session='quit', startup_session='')
        cli = SimpleNamespace(session='', args=())
        self.assertFalse(should_save_on_quit(never))
        self.assertFalse(should_save_on_last_window_close(never))
        self.assertFalse(should_restore_on_startup(never, cli))
        self.assertTrue(should_save_on_quit(quit_))
        self.assertTrue(should_save_on_last_window_close(quit_))

    def test_should_restore_on_startup(self):
        opts = SimpleNamespace(macos_restore_session='quit', startup_session='')
        cli = SimpleNamespace(session='', args=())
        path = latest_session_path()
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, 'w', encoding='utf-8') as f:
            f.write('new_tab\nlaunch')
        self.assertTrue(should_restore_on_startup(opts, cli))
        self.assertFalse(should_restore_on_startup(opts, SimpleNamespace(session='x', args=())))
        self.assertFalse(should_restore_on_startup(opts, SimpleNamespace(session='', args=('bash',))))
        self.assertFalse(should_restore_on_startup(SimpleNamespace(macos_restore_session='quit', startup_session='x'), cli))

    def test_strip_restore_notice_and_callback(self):
        class FakeWindow:
            id = 17

            def as_text(self, **kw):
                return (
                    f'{RESTORE_NOTICE_PREFIX}2026-04-30 12:00:00\n'
                    'keep this line\n'
                    f'{RESTORE_NOTICE_PREFIX}2026-04-30 12:00:01\n'
                )

        self.ae(strip_restore_notice(FakeWindow().as_text()), 'keep this line\n')
        with tempfile.TemporaryDirectory() as tdir:
            callback = extra_launch_data_callback(tdir)
            ref = callback(FakeWindow())
            self.ae(ref, {'scrollback_ref': os.path.join('scrollback', '17.ansi')})
            path = os.path.join(tdir, ref['scrollback_ref'])
            with open(path, 'rb') as f:
                self.ae(f.read().decode('utf-8'), 'keep this line\n')
            # Callback is stable for repeated calls on the same window id.
            self.ae(callback(FakeWindow()), ref)
