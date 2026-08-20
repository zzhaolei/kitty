package search

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kovidgoyal/kitty/tools/tui/loop"
	"github.com/kovidgoyal/kitty/tools/wcswidth"
)

func testHandler(t *testing.T, lineCount int) *Handler {
	t.Helper()
	lp, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	lines := make([]DisplayLine, lineCount)
	for i := range lines {
		lines[i].raw = fmt.Sprintf("line %d", i)
		lines[i].visible = lines[i].raw
		lines[i].width = wcswidth.Stringwidth(lines[i].visible)
		lines[i].logicalLine = i
	}
	return &Handler{
		lp:           lp,
		lines:        lines,
		screenSize:   loop.ScreenSize{WidthCells: 80, HeightCells: 11},
		scrollStart:  5,
		currentMatch: -1,
	}
}

func keyEvent(spec string) *loop.KeyEvent {
	shortcut := loop.ParseShortcut(spec)
	return &loop.KeyEvent{Type: loop.PRESS, Mods: shortcut.Mods, Key: shortcut.KeyName}
}

func TestCommandAndSearchModes(t *testing.T) {
	h := testHandler(t, 30)
	h.query = "old query"
	h.reSearch()

	if err := h.onText("ignored", true, false); err != nil {
		t.Fatal(err)
	}
	if h.query != "old query" {
		t.Fatalf("text input in command mode changed the query to %q", h.query)
	}

	j := keyEvent("j")
	if err := h.onKeyEvent(j); err != nil {
		t.Fatal(err)
	}
	if !j.Handled || h.scrollStart != 6 {
		t.Fatalf("j should scroll down in command mode, handled=%v scrollStart=%d", j.Handled, h.scrollStart)
	}

	k := keyEvent("k")
	if err := h.onKeyEvent(k); err != nil {
		t.Fatal(err)
	}
	if !k.Handled || h.scrollStart != 5 {
		t.Fatalf("k should scroll up in command mode, handled=%v scrollStart=%d", k.Handled, h.scrollStart)
	}

	h.lp.Quit(1)
	slash := keyEvent("/")
	if err := h.onKeyEvent(slash); err != nil {
		t.Fatal(err)
	}
	if !slash.Handled || h.mode != searchMode || h.query != "" {
		t.Fatalf("/ should start a fresh search, handled=%v mode=%v query=%q", slash.Handled, h.mode, h.query)
	}

	searchJ := keyEvent("j")
	if err := h.onKeyEvent(searchJ); err != nil {
		t.Fatal(err)
	}
	if searchJ.Handled {
		t.Fatal("j should be text input, not scrolling, in search mode")
	}
	if err := h.onText("j", true, false); err != nil {
		t.Fatal(err)
	}
	if h.query != "j" || h.scrollStart != 5 {
		t.Fatalf("j should extend the query without scrolling in search mode, query=%q scrollStart=%d", h.query, h.scrollStart)
	}

	escape := keyEvent("escape")
	if err := h.onKeyEvent(escape); err != nil {
		t.Fatal(err)
	}
	if h.mode != commandMode || h.lp.ExitCode() != 1 {
		t.Fatalf("escape in search mode should only return to command mode, mode=%v exitCode=%d", h.mode, h.lp.ExitCode())
	}

	if err := h.onKeyEvent(keyEvent("escape")); err != nil {
		t.Fatal(err)
	}
	if h.lp.ExitCode() != 0 {
		t.Fatalf("escape in command mode should quit with status 0, got %d", h.lp.ExitCode())
	}
}

func TestEnterLeavesSearchMode(t *testing.T) {
	h := testHandler(t, 30)
	h.mode = searchMode
	h.query = "line 17"
	h.reSearch()

	enter := keyEvent("enter")
	if err := h.onKeyEvent(enter); err != nil {
		t.Fatal(err)
	}
	if !enter.Handled || h.mode != commandMode || h.query != "line 17" || h.scrollStart != 17 {
		t.Fatalf("enter should accept the query and return to command mode, handled=%v mode=%v query=%q scrollStart=%d", enter.Handled, h.mode, h.query, h.scrollStart)
	}
}

func TestEmptySearchClearsMatches(t *testing.T) {
	h := testHandler(t, 0)
	h.lines = parseDisplayLine("hit hit\nmiss")
	h.query = "hit"
	h.reSearch()
	if len(h.matches) != 2 || h.currentMatch < 0 {
		t.Fatalf("expected two matches, count=%d current=%d", len(h.matches), h.currentMatch)
	}

	h.query = ""
	h.reSearch()
	if len(h.matches) != 0 || h.currentMatch != -1 {
		t.Fatalf("empty search retained match state: count=%d current=%d", len(h.matches), h.currentMatch)
	}
	for i, line := range h.lines {
		if len(line.matches) != 0 {
			t.Fatalf("line %d retained %d matches", i, len(line.matches))
		}
	}
}

func TestRenderedAnsiLineResetsFormatting(t *testing.T) {
	h := testHandler(t, 0)
	h.lines = parseDisplayLine("\x1b[31mred red")
	h.query = "red"
	h.reSearch()

	rendered, _ := h.renderDisplayLine(h.lines[0], 0)
	if !strings.HasSuffix(rendered, "\x1b[m\x1b[K") {
		t.Fatalf("rendered ANSI line does not reset formatting before clearing its remainder: %q", rendered)
	}
	if visible := wcswidth.StripEscapeCodes(rendered); visible != "red red" {
		t.Fatalf("rendering multiple matches changed the visible line to %q", visible)
	}
	if !strings.HasPrefix(rendered, "\x1b[31mred red") {
		t.Fatalf("rendering injected highlighting into the original ANSI stream: %q", rendered)
	}
	if len(h.matches) != 2 || h.lines[0].matches[0].startCol != 0 || h.lines[0].matches[0].endCol != 3 {
		t.Fatalf("unexpected visible match geometry: count=%d matches=%+v", len(h.matches), h.lines[0].matches)
	}
}

func TestSearchDoesNotAllocatePerMatch(t *testing.T) {
	allocationsFor := func(input string) (float64, *Handler) {
		h := testHandler(t, 0)
		h.lines = parseDisplayLine(input)
		h.query = "hit"
		h.reSearch()
		return testing.AllocsPerRun(10, h.reSearch), h
	}
	oneMatchAllocations, _ := allocationsFor("hit")
	manyMatchAllocations, h := allocationsFor(strings.Repeat("hit hit hit\n", 200))
	matchStorage := &h.matches[0]
	spanStorage := &h.lines[0].matches[0]

	if manyMatchAllocations > oneMatchAllocations+1 {
		t.Fatalf("allocations grew with match count: one=%.1f many=%.1f", oneMatchAllocations, manyMatchAllocations)
	}
	h.reSearch()
	if matchStorage != &h.matches[0] || spanStorage != &h.lines[0].matches[0] {
		t.Fatal("repeated search replaced reusable match storage")
	}
}

func TestCurrentMatchIsPannedIntoView(t *testing.T) {
	h := testHandler(t, 0)
	h.screenSize.WidthCells = 5
	h.lines = parseDisplayLine("\x1b[31m0123456789match")
	h.query = "match"
	h.reSearch()

	offset := h.horizontalOffset
	if offset != 10 {
		t.Fatalf("selected match should move the view to column 10, got %d", offset)
	}
	rendered, actualOffset := h.renderDisplayLine(h.lines[0], offset)
	if actualOffset != offset {
		t.Fatalf("rendered offset is %d, expected %d", actualOffset, offset)
	}
	visibleTail := wcswidth.StripEscapeCodes(rendered[strings.LastIndexByte(rendered, '\r')+1:])
	if visibleTail != "match" {
		t.Fatalf("selected match is not visible after panning: %q", visibleTail)
	}
	if !strings.HasPrefix(rendered, "\x1b[31m") || !strings.HasSuffix(rendered, "\x1b[m") || strings.HasSuffix(rendered, "\x1b[K") {
		t.Fatalf("panning did not preserve and close ANSI state: %q", rendered)
	}

	h.lines = parseDisplayLine("\tmatch")
	h.horizontalOffset = 0
	h.reSearch()
	offset = h.horizontalOffset
	rendered, _ = h.renderDisplayLine(h.lines[0], offset)
	visibleTail = wcswidth.StripEscapeCodes(rendered[strings.LastIndexByte(rendered, '\r')+1:])
	if offset != 8 || visibleTail != "match" {
		t.Fatalf("tab-expanded match was not panned into view: offset=%d tail=%q", offset, visibleTail)
	}
}

func TestSearchIgnoresAnsiEscapeContents(t *testing.T) {
	h := testHandler(t, 0)
	h.lines = parseDisplayLine("he\x1b[31mllo")
	h.query = "3"
	h.reSearch()
	if len(h.matches) != 0 {
		t.Fatalf("matched an invisible SGR parameter: %d", len(h.matches))
	}

	h.query = "hello"
	h.reSearch()
	if len(h.matches) != 1 {
		t.Fatalf("a visible match split by ANSI was not found: %d", len(h.matches))
	}
	match := h.lines[0].matches[0]
	if match.startCol != 0 || match.endCol != 5 || match.match != 0 {
		t.Fatalf("unexpected visible match offsets: %+v", match)
	}

	h.lines = parseDisplayLine("\tmatch")
	h.query = "match"
	h.reSearch()
	match = h.lines[0].matches[0]
	if match.startCol != 8 || match.endCol != 13 {
		t.Fatalf("tab expansion produced incorrect columns: %+v", match)
	}
}

func TestParseDisplayLinesUsesPhysicalRows(t *testing.T) {
	lines := parseDisplayLine("\x1b[31mfirst\x1b[m\r\x1b[32msecond\x1b[m\r\nthird\r\n")
	if len(lines) != 3 {
		t.Fatalf("expected three physical rows, got %d", len(lines))
	}
	for i, expected := range []string{"first", "second", "third"} {
		if lines[i].visible != expected {
			t.Fatalf("row %d: got %q, expected %q", i, lines[i].visible, expected)
		}
		if strings.ContainsRune(lines[i].raw, '\r') {
			t.Fatalf("row %d retained a wrap marker: %q", i, lines[i].raw)
		}
	}
	if lines[0].logicalLine != lines[1].logicalLine || lines[2].logicalLine == lines[1].logicalLine {
		t.Fatalf("soft and hard wrap relationships were lost: %d, %d, %d", lines[0].logicalLine, lines[1].logicalLine, lines[2].logicalLine)
	}
	if lines := parseDisplayLine(""); len(lines) != 0 {
		t.Fatalf("empty input produced %d rows", len(lines))
	}
}

func TestSearchCanSpanSoftWrappedRows(t *testing.T) {
	h := testHandler(t, 0)
	h.lines = parseDisplayLine("hel\rlo\r\nother\r\n")
	h.query = "hello"
	h.reSearch()
	if len(h.matches) != 1 {
		t.Fatalf("expected one cross-row match, got %d", len(h.matches))
	}
	if h.currentMatch != 0 || len(h.lines[0].matches) != 1 || len(h.lines[1].matches) != 1 ||
		h.lines[0].matches[0] != (MatchSpan{startCol: 0, endCol: 3, match: 0}) ||
		h.lines[1].matches[0] != (MatchSpan{startCol: 0, endCol: 2, match: 0}) {
		t.Fatalf("unexpected cross-row match: current=%d spans=%+v, %+v", h.currentMatch, h.lines[0].matches, h.lines[1].matches)
	}
}

func TestHyperlinksAreSelfContainedPerRow(t *testing.T) {
	open := "\x1b]8;id=1;https://example.com\x1b\\"
	lines := parseDisplayLine(open + "one\r\ntwo" + closeHyperlink + "\r\n")
	if len(lines) != 2 {
		t.Fatalf("expected two rows, got %d", len(lines))
	}
	if lines[0].hyperlinkPrefix != "" || lines[1].hyperlinkPrefix != open {
		t.Fatalf("hyperlink state was not propagated: prefixes=%q, %q", lines[0].hyperlinkPrefix, lines[1].hyperlinkPrefix)
	}

	h := testHandler(t, 0)
	first, _ := h.renderDisplayLine(lines[0], 0)
	second, _ := h.renderDisplayLine(lines[1], 0)
	if !strings.HasSuffix(first, closeHyperlink+"\x1b[m\x1b[K") {
		t.Fatalf("first row did not close the hyperlink: %q", first)
	}
	if !strings.HasPrefix(second, open) || !strings.HasSuffix(second, closeHyperlink+"\x1b[m\x1b[K") {
		t.Fatalf("second row is not self-contained: %q", second)
	}
}

func TestScreenLayoutPutsSearchAboveToolbar(t *testing.T) {
	layout := calculateScreenLayout(11)
	if layout.viewsHeight != 6 {
		t.Fatalf("unexpected content viewport height: %d", layout.viewsHeight)
	}
	if layout.panelTopY != 7 || layout.searchBarY != 8 || layout.panelMiddleY != 9 || layout.toolbarY != 10 || layout.panelBottomY != 11 {
		t.Fatalf("unexpected bottom panel layout: %+v", layout)
	}
	if !(layout.searchBarY < layout.toolbarY) {
		t.Fatalf("search bar should be above toolbar: search=%d toolbar=%d", layout.searchBarY, layout.toolbarY)
	}

	h := testHandler(t, 30)
	newSize := loop.ScreenSize{WidthCells: 100, HeightCells: 20}
	if err := h.onResize(h.screenSize, newSize); err != nil {
		t.Fatal(err)
	}
	if h.screenSize != newSize || h.viewportHeight() != 15 {
		t.Fatalf("resize did not update the viewport: size=%+v height=%d", h.screenSize, h.viewportHeight())
	}
	if style := h.panelBorderStyle(); style != "fg=yellow dim" {
		t.Fatalf("unexpected command-mode border style: %q", style)
	}
	if hint := wcswidth.StripEscapeCodes(h.toolbarHint("[/]", "Search")); hint != "[/]Search " {
		t.Fatalf("unexpected toolbar hint: %q", hint)
	}
	h.mode = searchMode
	if style := h.panelBorderStyle(); style != "fg=bright-yellow" {
		t.Fatalf("unexpected search-mode border style: %q", style)
	}
	h.mode = helpMode
	if style := h.panelBorderStyle(); style != "fg=yellow dim" {
		t.Fatalf("unexpected help-mode border style: %q", style)
	}

	for height := 1; height <= 6; height++ {
		layout := calculateScreenLayout(height)
		if layout.searchBarY < 1 || layout.searchBarY > height {
			t.Fatalf("height %d has an invisible search row: %+v", height, layout)
		}
		if layout.viewsHeight > height {
			t.Fatalf("height %d has an overflowing viewport: %+v", height, layout)
		}
		if height >= 2 && layout.viewsHeight < 1 {
			t.Fatalf("height %d should retain a content row: %+v", height, layout)
		}
	}
}

func TestLongSearchTextKeepsTailVisible(t *testing.T) {
	tail, hidden := searchTextTail("0123456789", 5)
	if !hidden || tail != "6789" {
		t.Fatalf("unexpected truncated tail: hidden=%v tail=%q", hidden, tail)
	}
	tail, hidden = searchTextTail("猫咪", 3)
	if !hidden || tail != "咪" {
		t.Fatalf("wide graphemes were truncated incorrectly: hidden=%v tail=%q", hidden, tail)
	}
	if tail, hidden = searchTextTail("short", 10); hidden || tail != "short" {
		t.Fatalf("short text was truncated: hidden=%v tail=%q", hidden, tail)
	}
}

func TestBracketedPasteIsSanitizedAndCommittedOnce(t *testing.T) {
	h := testHandler(t, 3)
	h.mode = searchMode
	if err := h.onText("\x1b[31mline 1\nignored", false, true); err != nil {
		t.Fatal(err)
	}
	if h.query != "" {
		t.Fatalf("paste was committed before its end: %q", h.query)
	}
	if err := h.onText("", false, false); err != nil {
		t.Fatal(err)
	}
	if h.query != "line 1" || strings.ContainsRune(h.query, '\x1b') {
		t.Fatalf("paste was not sanitized: %q", h.query)
	}
	if len(h.matches) != 1 {
		t.Fatalf("sanitized paste did not trigger search: %d matches", len(h.matches))
	}
	if text := sanitizeSearchText("a\tb"); text != "a       b" {
		t.Fatalf("tab in search text was not expanded consistently: %q", text)
	}
}

func TestInitialSelectionMovesToAnchoredMatch(t *testing.T) {
	h := testHandler(t, 30)
	h.query = "line 27"
	h.scrollStart = len(h.lines)
	h.prepareInitialView()
	if h.currentMatch < 0 || h.matches[h.currentMatch] != 27 || h.scrollStart != 24 {
		t.Fatalf("initial selection was not revealed: current=%d scrollStart=%d", h.currentMatch, h.scrollStart)
	}
}

func TestLessScrollingKeys(t *testing.T) {
	tests := []struct {
		keys  []string
		delta int
	}{
		{[]string{"j", "down", "enter", "e", "ctrl+e", "ctrl+n"}, 1},
		{[]string{"k", "up", "y", "ctrl+y", "ctrl+k", "ctrl+p"}, -1},
		{[]string{"page_down", "space", "f", "ctrl+f", "ctrl+v", "z"}, 6},
		{[]string{"page_up", "b", "ctrl+b", "alt+v", "w"}, -6},
		{[]string{"d", "ctrl+d"}, 3},
		{[]string{"u", "ctrl+u"}, -3},
	}
	for _, tc := range tests {
		for _, key := range tc.keys {
			t.Run(key, func(t *testing.T) {
				h := testHandler(t, 60)
				h.scrollStart = 20
				ev := keyEvent(key)
				if err := h.onKeyEvent(ev); err != nil {
					t.Fatal(err)
				}
				if !ev.Handled || h.scrollStart != 20+tc.delta {
					t.Fatalf("%s: handled=%v scrollStart=%d, expected %d", key, ev.Handled, h.scrollStart, 20+tc.delta)
				}
			})
		}
	}
}

func TestLessJumpAndQuitKeys(t *testing.T) {
	for _, key := range []string{"g", "<", "home", "ctrl+home"} {
		t.Run("start_"+key, func(t *testing.T) {
			h := testHandler(t, 60)
			h.scrollStart = 20
			if err := h.onKeyEvent(keyEvent(key)); err != nil {
				t.Fatal(err)
			}
			if h.scrollStart != 0 {
				t.Fatalf("%s should jump to the start, got %d", key, h.scrollStart)
			}
		})
	}
	for _, key := range []string{"G", ">", "end", "ctrl+end"} {
		t.Run("end_"+key, func(t *testing.T) {
			h := testHandler(t, 60)
			h.scrollStart = 20
			if err := h.onKeyEvent(keyEvent(key)); err != nil {
				t.Fatal(err)
			}
			if h.scrollStart != 54 {
				t.Fatalf("%s should jump to the end, got %d", key, h.scrollStart)
			}
		})
	}
	t.Run("shifted_G", func(t *testing.T) {
		h := testHandler(t, 60)
		h.scrollStart = 20
		ev := &loop.KeyEvent{Type: loop.PRESS, Mods: loop.SHIFT, Key: "G", ShiftedKey: "g", Text: "G"}
		if err := h.onKeyEvent(ev); err != nil {
			t.Fatal(err)
		}
		if h.scrollStart != 54 {
			t.Fatalf("shift+g should jump to the end, got %d", h.scrollStart)
		}
	})

	h := testHandler(t, 10)
	h.lp.Quit(1)
	if err := h.onKeyEvent(keyEvent("q")); err != nil {
		t.Fatal(err)
	}
	if h.lp.ExitCode() != 0 {
		t.Fatalf("q should quit with status 0, got %d", h.lp.ExitCode())
	}

	for _, key := range []string{"r", "ctrl+r", "ctrl+l"} {
		ev := keyEvent(key)
		if err := h.onKeyEvent(ev); err != nil {
			t.Fatal(err)
		}
		if !ev.Handled {
			t.Fatalf("%s should redraw the screen", key)
		}
	}
}

func TestLessSearchNavigationKeys(t *testing.T) {
	h := testHandler(t, 0)
	h.lines = parseDisplayLine("match\nother\nmatch\nother\nmatch")
	h.query = "match"
	h.reSearch()
	if h.currentMatch != 0 || h.matches[h.currentMatch] != 0 {
		t.Fatalf("unexpected initial match: index=%d line=%d", h.currentMatch, h.matches[h.currentMatch])
	}

	n := keyEvent("n")
	if err := h.onKeyEvent(n); err != nil {
		t.Fatal(err)
	}
	if !n.Handled || h.currentMatch != 1 || h.matches[h.currentMatch] != 2 {
		t.Fatalf("n should select the next match: handled=%v index=%d line=%d", n.Handled, h.currentMatch, h.matches[h.currentMatch])
	}

	upperN := &loop.KeyEvent{Type: loop.PRESS, Mods: loop.SHIFT, Key: "N", ShiftedKey: "n", Text: "N"}
	if err := h.onKeyEvent(upperN); err != nil {
		t.Fatal(err)
	}
	if !upperN.Handled || h.currentMatch != 0 || h.matches[h.currentMatch] != 0 {
		t.Fatalf("N should reverse match direction: handled=%v index=%d line=%d", upperN.Handled, h.currentMatch, h.matches[h.currentMatch])
	}
}

func TestBackwardSearchAndDirectionAwareRepeat(t *testing.T) {
	h := testHandler(t, 0)
	h.lines = parseDisplayLine("match\nother\nmatch\nother\nmatch")
	h.scrollStart = 0

	question := keyEvent("?")
	if err := h.onKeyEvent(question); err != nil {
		t.Fatal(err)
	}
	if !question.Handled || h.mode != searchMode || h.searchDirection != searchBackward {
		t.Fatalf("? did not enter backward search: handled=%v mode=%v direction=%v", question.Handled, h.mode, h.searchDirection)
	}
	if err := h.onText("match", true, false); err != nil {
		t.Fatal(err)
	}
	if h.currentMatch != 2 || h.matches[h.currentMatch] != 4 {
		t.Fatalf("backward search did not start at the last visible match: current=%d matches=%v", h.currentMatch, h.matches)
	}
	if err := h.onKeyEvent(keyEvent("escape")); err != nil {
		t.Fatal(err)
	}
	if err := h.onKeyEvent(keyEvent("n")); err != nil {
		t.Fatal(err)
	}
	if h.currentMatch != 1 || h.matches[h.currentMatch] != 2 {
		t.Fatalf("n did not continue backward: current=%d matches=%v", h.currentMatch, h.matches)
	}
	upperN := &loop.KeyEvent{Type: loop.PRESS, Mods: loop.SHIFT, Key: "N", ShiftedKey: "n", Text: "N"}
	if err := h.onKeyEvent(upperN); err != nil {
		t.Fatal(err)
	}
	if h.currentMatch != 2 || h.matches[h.currentMatch] != 4 {
		t.Fatalf("N did not reverse the backward search: current=%d matches=%v", h.currentMatch, h.matches)
	}
}

func TestPOSIXRegexpSearch(t *testing.T) {
	h := testHandler(t, 0)
	h.lines = parseDisplayLine("WARN 12\nINFO\nWARN 345")
	h.query = `^WARN [0-9]+$`
	h.reSearch()
	if h.searchError != "" || len(h.matches) != 2 || h.matches[0] != 0 || h.matches[1] != 2 {
		t.Fatalf("unexpected regexp result: error=%q matches=%v", h.searchError, h.matches)
	}

	h.query = "["
	h.reSearch()
	if h.searchError == "" || len(h.matches) != 0 || h.currentMatch != -1 {
		t.Fatalf("invalid regexp was not reported cleanly: error=%q matches=%v current=%d", h.searchError, h.matches, h.currentMatch)
	}

	h.query = "^"
	h.reSearch()
	if h.searchError != "" || len(h.matches) != 3 {
		t.Fatalf("zero-width regexp should find each logical line: error=%q matches=%v", h.searchError, h.matches)
	}
}

func TestLessNumericPrefixes(t *testing.T) {
	press := func(t *testing.T, h *Handler, keys ...string) {
		t.Helper()
		for _, key := range keys {
			ev := keyEvent(key)
			if err := h.onKeyEvent(ev); err != nil {
				t.Fatal(err)
			}
			if !ev.Handled {
				t.Fatalf("%s was not handled", key)
			}
		}
	}

	h := testHandler(t, 100)
	h.scrollStart = 10
	press(t, h, "3", "j")
	if h.scrollStart != 13 || h.pendingCount != 0 {
		t.Fatalf("3j moved to %d with pending count %d", h.scrollStart, h.pendingCount)
	}
	press(t, h, "4", "f")
	if h.scrollStart != 17 {
		t.Fatalf("4f should move four lines, got %d", h.scrollStart)
	}
	press(t, h, "2", "z", "z")
	if h.scrollStart != 21 || h.pageRows != 2 {
		t.Fatalf("2z should set a two-line page: scroll=%d pageRows=%d", h.scrollStart, h.pageRows)
	}
	press(t, h, "4", "d", "u")
	if h.scrollStart != 21 || h.halfPageRows != 4 {
		t.Fatalf("4d should set a four-line half page: scroll=%d halfRows=%d", h.scrollStart, h.halfPageRows)
	}
	press(t, h, "2", "g")
	if h.scrollStart != 1 {
		t.Fatalf("2g should jump to line two, got %d", h.scrollStart)
	}
	press(t, h, "3", "G")
	if h.scrollStart != 2 {
		t.Fatalf("3G should jump to line three, got %d", h.scrollStart)
	}
	press(t, h, "5", "0", "%")
	if h.scrollStart != 49 {
		t.Fatalf("50%% should jump halfway through the file, got %d", h.scrollStart)
	}

	h.lines = parseDisplayLine("match\nother\nmatch\nother\nmatch")
	h.query = "match"
	h.searchAnchor = 0
	h.searchDirection = searchForward
	h.reSearch()
	press(t, h, "2", "n")
	if h.currentMatch != 2 {
		t.Fatalf("2n selected match %d, expected 2", h.currentMatch)
	}

	h.mode = commandMode
	h.query = ""
	press(t, h, "2", "/")
	if err := h.onText("match", true, false); err != nil {
		t.Fatal(err)
	}
	if h.searchCount != 2 || h.currentMatch != 1 {
		t.Fatalf("2/pattern selected match %d with search count %d", h.currentMatch, h.searchCount)
	}
}

func TestLessHorizontalKeys(t *testing.T) {
	h := testHandler(t, 0)
	h.screenSize.WidthCells = 6
	h.lines = parseDisplayLine("0123456789abcdef")

	for _, key := range []string{"right", "alt+)"} {
		h.horizontalOffset = 0
		ev := keyEvent(key)
		if err := h.onKeyEvent(ev); err != nil {
			t.Fatal(err)
		}
		if !ev.Handled || h.horizontalOffset != 3 {
			t.Fatalf("%s should move right half a screen: handled=%v offset=%d", key, ev.Handled, h.horizontalOffset)
		}
	}
	if err := h.onKeyEvent(keyEvent("2")); err != nil {
		t.Fatal(err)
	}
	if err := h.onKeyEvent(keyEvent("right")); err != nil {
		t.Fatal(err)
	}
	if h.horizontalOffset != 5 {
		t.Fatalf("2 Right should move two columns from three, got %d", h.horizontalOffset)
	}
	if err := h.onKeyEvent(keyEvent("ctrl+right")); err != nil {
		t.Fatal(err)
	}
	if h.horizontalOffset != 10 {
		t.Fatalf("Ctrl-Right should jump to the last column, got %d", h.horizontalOffset)
	}
	if err := h.onKeyEvent(keyEvent("ctrl+left")); err != nil {
		t.Fatal(err)
	}
	if h.horizontalOffset != 0 {
		t.Fatalf("Ctrl-Left should jump to the first column, got %d", h.horizontalOffset)
	}
}

func TestHelpAndUppercaseQuit(t *testing.T) {
	h := testHandler(t, 30)
	hKey := keyEvent("h")
	if err := h.onKeyEvent(hKey); err != nil {
		t.Fatal(err)
	}
	if !hKey.Handled || h.mode != helpMode {
		t.Fatalf("h did not open help: handled=%v mode=%v", hKey.Handled, h.mode)
	}
	if err := h.onKeyEvent(keyEvent("j")); err != nil {
		t.Fatal(err)
	}
	if h.helpScrollStart != 1 {
		t.Fatalf("j did not scroll help, start=%d", h.helpScrollStart)
	}
	if err := h.onKeyEvent(keyEvent("q")); err != nil {
		t.Fatal(err)
	}
	if h.mode != commandMode {
		t.Fatalf("q should close help, mode=%v", h.mode)
	}

	h.lp.Quit(1)
	upperQ := keyEvent("shift+q")
	if err := h.onKeyEvent(upperQ); err != nil {
		t.Fatal(err)
	}
	if !upperQ.Handled || h.lp.ExitCode() != 0 {
		t.Fatalf("Q should quit: handled=%v exit=%d", upperQ.Handled, h.lp.ExitCode())
	}
}

func TestNavigateWithNoMatchesDoesNotPanic(t *testing.T) {
	h := testHandler(t, 3)
	h.query = "not present"
	h.reSearch()
	if err := h.onKeyEvent(keyEvent("n")); err != nil {
		t.Fatal(err)
	}
}
