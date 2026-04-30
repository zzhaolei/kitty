#!/usr/bin/env python
# License: GPL v3 Copyright: 2026

import json
import os

from kitty.options.types import defaults
from kitty.session import parse_session

from . import BaseTest


class TestSession(BaseTest):

    def test_parse_scrollback_ref_relative_to_session_path(self):
        payload = json.dumps({'id': 1, 'scrollback_ref': 'scrollback/1.ansi'}, separators=(',', ':'))
        path = os.path.join(os.getcwd(), 'sample.kitty-session')
        s = next(parse_session(f'launch kitty-unserialize-data={payload}', defaults, session_arg=path, session_path=path))
        self.ae(s.tabs[0].windows[0].scrollback_ref, os.path.join(os.path.dirname(path), 'scrollback', '1.ansi'))

    def test_parse_scrollback_ref_absolute_path_unchanged(self):
        target = os.path.join(os.getcwd(), 'scrollback', '2.ansi')
        payload = json.dumps({'id': 2, 'scrollback_ref': target}, separators=(',', ':'))
        path = os.path.join(os.getcwd(), 'sample.kitty-session')
        s = next(parse_session(f'launch kitty-unserialize-data={payload}', defaults, session_arg=path, session_path=path))
        self.ae(s.tabs[0].windows[0].scrollback_ref, target)
