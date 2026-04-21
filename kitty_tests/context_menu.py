#!/usr/bin/env python
# License: GPL v3 Copyright: 2026, Kovid Goyal <kovid at kovidgoyal.net>

from kitty import boss as boss_module
from kitty.boss import CONTEXT_MENU_SUBMENU, Boss, CONTEXT_MENU_ACTIONS, parse_context_menu_entries
from kitty.fast_data_types import GLFW_MOUSE_BUTTON_LEFT, GLFW_PRESS, SingleKey
from kitty.types import WindowGeometry, WindowSystemMouseEvent

from . import BaseTest


class TestContextMenu(BaseTest):

    def test_context_menu_dispatch(self):
        boss = Boss.__new__(Boss)

        class Window:
            os_window_id = 3
            tab_id = 5
            id = 7
            geometry = WindowGeometry(0, 0, 800, 600, 80, 24)

            class Screen:
                columns = 80
                lines = 24

            screen = Screen()

            def current_mouse_position(self):
                return {'cell_x': 78, 'cell_y': 22}

        class Mappings:
            pushed = None
            popped = []

            def _push_keyboard_mode(self, mode):
                self.pushed = mode

            def pop_keyboard_mode_if_is(self, name):
                self.popped.append(name)
                return True

        target = Window()
        mappings = Mappings()
        boss.window_for_dispatch = target
        boss.context_menu_target = None
        boss.context_menu_bounds = None
        boss.window_id_map = {target.id: target}
        boss.mappings = mappings
        boss.mouse_handler = None
        calls = []

        def combine(action, window_for_dispatch=None, dispatch_type='KeyPress', raise_error=False):
            calls.append(('combine', action, window_for_dispatch, dispatch_type))
            return True

        def set_context_menu_bar(os_window_id, tab_id, window_id, text, x, y, width, height):
            calls.append(('bar', os_window_id, tab_id, window_id, text, x, y, width, height))

        def redirect_mouse_handling(enabled):
            calls.append(('redirect', enabled))

        def cell_size_for_window(os_window_id):
            return 10, 20

        orig_set_context_menu_bar = boss_module.set_context_menu_bar
        orig_redirect_mouse_handling = boss_module.redirect_mouse_handling
        orig_cell_size_for_window = boss_module.cell_size_for_window
        boss.combine = combine
        try:
            boss_module.set_context_menu_bar = set_context_menu_bar
            boss_module.redirect_mouse_handling = redirect_mouse_handling
            boss_module.cell_size_for_window = cell_size_for_window
            boss.show_context_menu()
            text = '\n'.join(label for _, label, _ in CONTEXT_MENU_ACTIONS)
            self.ae(calls[:2], [
                ('bar', target.os_window_id, target.tab_id, target.id, text, 59, 17, 19, 6),
                ('redirect', True),
            ])
            self.assertIsNotNone(mappings.pushed)
            self.ae(mappings.pushed.keymap[SingleKey(key=ord('p'))][0].definition, 'context_menu_action p')
            boss.context_menu_mouse_handler(WindowSystemMouseEvent(False, target.id, GLFW_PRESS, 0, GLFW_MOUSE_BUTTON_LEFT, 0, 590, 360))
            self.ae(calls[-3:], [
                ('bar', target.os_window_id, target.tab_id, target.id, None, 0, 0, 0, 0),
                ('redirect', False),
                ('combine', 'paste_from_clipboard', target, 'MouseEvent'),
            ])
        finally:
            boss_module.set_context_menu_bar = orig_set_context_menu_bar
            boss_module.redirect_mouse_handling = orig_redirect_mouse_handling
            boss_module.cell_size_for_window = orig_cell_size_for_window

    def test_context_menu_custom_entries(self):
        custom = parse_context_menu_entries(('Run custom kitten', 'kitten', 'custom_kitten', '--flag'))
        self.ae(custom[-1], ('r', 'Run custom kitten', 'kitten custom_kitten --flag'))
        submenu = parse_context_menu_entries(('Actions::Launch something special', 'launch', '--hold'))
        self.ae(submenu[-1][:2], ('a', 'Actions >'))
        self.assertTrue(submenu[-1][2].startswith(CONTEXT_MENU_SUBMENU))
