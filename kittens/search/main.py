#!/usr/bin/env python


import sys

from kitty.conf.types import Definition

definition = Definition(
    '!kittens.search',
)

agr = definition.add_group
egr = definition.end_group
map = definition.add_map

# shortcuts {{{
agr('shortcuts', 'Keyboard shortcuts')

map(
    'Scroll up',
    'selection_up ctrl+k selection_up',
)
map(
    'Scroll down',
    'selection_down ctrl+j selection_down',
)

egr()  # }}}

OPTIONS = r"""
--selection
Initial text to search for, usually populated from the current selection.
""".format

usage = ''
short_description = 'Search the scrollback'
help_text = 'Search the scrollback with POSIX regular expressions and less-style navigation'

if __name__ == '__main__':
    raise SystemExit('This kitten must be used only from a kitty.conf mapping')
elif __name__ == '__doc__':
    cd = sys.cli_docs  # type: ignore
    cd['usage'] = usage
    cd['options'] = OPTIONS
    cd['help_text'] = help_text
    cd['short_desc'] = short_description
elif __name__ == '__conf__':
    sys.options_definition = definition  # type: ignore
