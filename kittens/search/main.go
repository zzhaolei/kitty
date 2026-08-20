package search

import (
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/kovidgoyal/kitty"
	"github.com/kovidgoyal/kitty/tools/cli"
	"github.com/kovidgoyal/kitty/tools/config"
	"github.com/kovidgoyal/kitty/tools/tty"
	"github.com/kovidgoyal/kitty/tools/tui/loop"
	"github.com/kovidgoyal/kitty/tools/utils"
	"github.com/kovidgoyal/kitty/tools/wcswidth"
)

type DisplayLine struct {
	raw             string
	visible         string
	width           int
	hyperlinkPrefix string
	logicalLine     int
	matches         []MatchSpan
}

type MatchSpan struct {
	startCol int
	endCol   int
	match    int
}

type inputMode uint8

const (
	commandMode inputMode = iota
	searchMode
	helpMode
)

type searchDirection uint8

const (
	searchForward searchDirection = iota
	searchBackward
)

type Handler struct {
	lp                *loop.Loop
	lines             []DisplayLine
	query             string
	matches           []int
	currentMatch      int
	mode              inputMode
	searchDirection   searchDirection
	searchCount       int
	searchError       string
	screenSize        loop.ScreenSize
	scrollStart       int
	searchAnchor      int
	horizontalOffset  int
	helpScrollStart   int
	pendingCount      int
	pageRows          int
	halfPageRows      int
	inBracketedPaste  bool
	bracketedPaste    strings.Builder
	shortcutTracker   config.ShortcutTracker
	keyboardShortcuts []*config.KeyAction
}

func (h *Handler) initialize() (string, error) {
	sz, err := h.lp.ScreenSize()
	if err != nil {
		return "", err
	}
	h.screenSize = sz
	h.lp.SetCursorShape(loop.BAR_CURSOR, true)
	h.lp.AllowLineWrapping(false)
	h.lp.SetWindowTitle("Search")

	h.keyboardShortcuts = config.ResolveShortcuts(NewConfig().KeyboardShortcuts)

	h.prepareInitialView()
	h.drawScreen()
	h.lp.SendOverlayReady()
	return "", nil
}

func (h *Handler) prepareInitialView() {
	h.scrollStart = min(max(0, h.scrollStart), h.maxScrollStart())
	h.searchAnchor = h.scrollStart
	h.reSearch()
	if h.query != "" && h.currentMatch >= 0 {
		h.scrollStart = min(h.matches[h.currentMatch], h.maxScrollStart())
	}
}

func matchesCaseSensitiveCommand(ev *loop.KeyEvent, key string) bool {
	if ev.Type&(loop.PRESS|loop.REPEAT) == 0 {
		return false
	}
	if ev.Text == key || ev.MatchesPressOrRepeatWithFallback(key, "ascii") {
		return true
	}
	return len(key) == 1 && key[0] >= 'A' && key[0] <= 'Z' &&
		ev.MatchesPressOrRepeat("shift+"+strings.ToLower(key))
}

func commandDigit(ev *loop.KeyEvent) (int, bool) {
	for digit := 0; digit <= 9; digit++ {
		if ev.MatchesPressOrRepeat(strconv.Itoa(digit)) {
			return digit, true
		}
	}
	return 0, false
}

func matchesAny(ev *loop.KeyEvent, keys ...string) bool {
	for _, key := range keys {
		if ev.MatchesPressOrRepeat(key) {
			return true
		}
	}
	return false
}

func (h *Handler) takeCount(defaultValue int) (int, bool) {
	if h.pendingCount == 0 {
		return defaultValue, false
	}
	ans := h.pendingCount
	h.pendingCount = 0
	return ans, true
}

func (h *Handler) pageSize() int {
	if h.pageRows > 0 {
		return h.pageRows
	}
	return h.viewportHeight()
}

func (h *Handler) halfPageSize() int {
	if h.halfPageRows > 0 {
		return h.halfPageRows
	}
	return max(1, h.viewportHeight()/2)
}

func (h *Handler) onKeyEvent(ev *loop.KeyEvent) error {
	if ev.MatchesPressOrRepeat("escape") {
		ev.Handled = true
		if h.mode == searchMode || h.mode == helpMode {
			h.mode = commandMode
			h.pendingCount = 0
			h.inBracketedPaste = false
			h.bracketedPaste.Reset()
			h.drawScreen()
		} else {
			h.lp.Quit(0)
		}
		return nil
	}

	if h.mode == searchMode {
		if ev.MatchesPressOrRepeat("enter") {
			ev.Handled = true
			h.mode = commandMode
			if h.currentMatch >= 0 {
				h.scrollStart = h.matches[h.currentMatch]
			}
			h.drawScreen()
			return nil
		}
		if ev.MatchesPressOrRepeat("backspace") {
			ev.Handled = true
			if h.query != "" {
				g := wcswidth.SplitIntoGraphemes(h.query)
				h.query = strings.Join(g[:len(g)-1], "")
				h.reSearch()
				h.drawScreen()
			} else {
				h.lp.Beep()
			}
			return nil
		}
		return nil
	}
	if h.mode == helpMode {
		return h.onHelpKey(ev)
	}

	if digit, ok := commandDigit(ev); ok && (digit != 0 || h.pendingCount != 0) {
		ev.Handled = true
		const maxCommandCount = 1_000_000
		h.pendingCount = min(maxCommandCount, h.pendingCount*10+digit)
		h.drawScreen()
		return nil
	}

	if matchesAny(ev, "/", "?") {
		ev.Handled = true
		h.mode = searchMode
		h.searchCount, _ = h.takeCount(1)
		if ev.MatchesPressOrRepeat("?") {
			h.searchDirection = searchBackward
			h.searchAnchor = max(0, min(len(h.lines)-1, h.scrollStart+h.viewportHeight()-1))
		} else {
			h.searchDirection = searchForward
			h.searchAnchor = h.scrollStart
		}
		h.query = ""
		h.inBracketedPaste = false
		h.bracketedPaste.Reset()
		h.reSearch()
		h.drawScreen()
		return nil
	}
	if ev.MatchesPressOrRepeat("q") || matchesCaseSensitiveCommand(ev, "Q") {
		ev.Handled = true
		h.lp.Quit(0)
		return nil
	}
	if ev.MatchesPressOrRepeat("h") || matchesCaseSensitiveCommand(ev, "H") {
		ev.Handled = true
		h.pendingCount = 0
		h.mode = helpMode
		h.helpScrollStart = 0
		h.drawScreen()
		return nil
	}
	if matchesCaseSensitiveCommand(ev, "N") {
		ev.Handled = true
		count, _ := h.takeCount(1)
		h.moveCurrentMatch(h.searchDirection != searchForward, count)
		return nil
	}
	if matchesCaseSensitiveCommand(ev, "n") {
		ev.Handled = true
		count, _ := h.takeCount(1)
		h.moveCurrentMatch(h.searchDirection == searchForward, count)
		return nil
	}
	if matchesAny(ev, "j", "down", "enter", "e", "ctrl+e", "ctrl+n") {
		ev.Handled = true
		count, _ := h.takeCount(1)
		h.moveScrollStart(count)
		return nil
	}
	if matchesAny(ev, "k", "up", "y", "ctrl+y", "ctrl+k", "ctrl+p") {
		ev.Handled = true
		count, _ := h.takeCount(1)
		h.moveScrollStart(-count)
		return nil
	}
	if matchesAny(ev, "page_down", "space", "f", "ctrl+f", "ctrl+v", "alt+space") {
		ev.Handled = true
		count, _ := h.takeCount(h.pageSize())
		h.moveScrollStart(count)
		return nil
	}
	if matchesAny(ev, "page_up", "b", "ctrl+b", "alt+v") {
		ev.Handled = true
		count, _ := h.takeCount(h.pageSize())
		h.moveScrollStart(-count)
		return nil
	}
	if matchesAny(ev, "z", "w") {
		ev.Handled = true
		count, explicit := h.takeCount(h.pageSize())
		if explicit {
			h.pageRows = count
		}
		if ev.MatchesPressOrRepeat("w") {
			count = -count
		}
		h.moveScrollStart(count)
		return nil
	}
	if matchesAny(ev, "d", "ctrl+d") {
		ev.Handled = true
		count, explicit := h.takeCount(h.halfPageSize())
		if explicit {
			h.halfPageRows = count
		}
		h.moveScrollStart(count)
		return nil
	}
	if matchesAny(ev, "u", "ctrl+u") {
		ev.Handled = true
		count, explicit := h.takeCount(h.halfPageSize())
		if explicit {
			h.halfPageRows = count
		}
		h.moveScrollStart(-count)
		return nil
	}
	if matchesAny(ev, "right", "alt+)") {
		ev.Handled = true
		count, _ := h.takeCount(max(1, int(h.screenSize.WidthCells)/2))
		h.moveHorizontal(count)
		return nil
	}
	if matchesAny(ev, "left", "alt+(") {
		ev.Handled = true
		count, _ := h.takeCount(max(1, int(h.screenSize.WidthCells)/2))
		h.moveHorizontal(-count)
		return nil
	}
	if matchesAny(ev, "ctrl+right", "alt+}") {
		ev.Handled = true
		h.pendingCount = 0
		h.moveHorizontal(h.maxHorizontalOffset() - h.horizontalOffset)
		return nil
	}
	if matchesAny(ev, "ctrl+left", "alt+{") {
		ev.Handled = true
		h.pendingCount = 0
		h.moveHorizontal(-h.horizontalOffset)
		return nil
	}
	if matchesCaseSensitiveCommand(ev, "G") || matchesAny(ev, ">", "alt+>", "end", "ctrl+end") {
		ev.Handled = true
		line, explicit := h.takeCount(0)
		target := h.maxScrollStart()
		if explicit {
			target = line - 1
		}
		h.moveScrollStart(target - h.scrollStart)
		return nil
	}
	if matchesAny(ev, "g", "<", "alt+<", "home", "ctrl+home") {
		ev.Handled = true
		line, explicit := h.takeCount(0)
		target := 0
		if explicit {
			target = line - 1
		}
		h.moveScrollStart(target - h.scrollStart)
		return nil
	}
	if matchesAny(ev, "p", "%") {
		ev.Handled = true
		percent, _ := h.takeCount(0)
		percent = min(percent, 100)
		target := min(h.maxScrollStart(), max(0, len(h.lines)-1)*percent/100)
		h.moveScrollStart(target - h.scrollStart)
		return nil
	}
	if matchesAny(ev, "r", "ctrl+r", "ctrl+l") || matchesCaseSensitiveCommand(ev, "R") {
		ev.Handled = true
		h.pendingCount = 0
		h.drawScreen()
		return nil
	}
	if ac := h.shortcutTracker.Match(ev, h.keyboardShortcuts); ac != nil {
		ev.Handled = true
		count, _ := h.takeCount(1)
		switch ac.Name {
		case "selection_up":
			h.moveScrollStart(-count)
		case "selection_down":
			h.moveScrollStart(count)
		}
		return nil
	}
	if h.pendingCount != 0 {
		h.pendingCount = 0
		h.drawScreen()
	}
	return nil
}

func (h *Handler) onHelpKey(ev *loop.KeyEvent) error {
	if ev.MatchesPressOrRepeat("q") || matchesCaseSensitiveCommand(ev, "Q") ||
		ev.MatchesPressOrRepeat("h") || matchesCaseSensitiveCommand(ev, "H") {
		ev.Handled = true
		h.mode = commandMode
		h.drawScreen()
		return nil
	}
	if matchesAny(ev, "j", "down", "enter", "e", "ctrl+e", "ctrl+n") {
		ev.Handled = true
		h.moveHelpStart(1)
		return nil
	}
	if matchesAny(ev, "k", "up", "y", "ctrl+y", "ctrl+k", "ctrl+p") {
		ev.Handled = true
		h.moveHelpStart(-1)
		return nil
	}
	if matchesAny(ev, "page_down", "space", "f", "ctrl+f", "ctrl+v", "z") {
		ev.Handled = true
		h.moveHelpStart(h.viewportHeight())
		return nil
	}
	if matchesAny(ev, "page_up", "b", "ctrl+b", "alt+v", "w") {
		ev.Handled = true
		h.moveHelpStart(-h.viewportHeight())
		return nil
	}
	if matchesAny(ev, "d", "ctrl+d") {
		ev.Handled = true
		h.moveHelpStart(max(1, h.viewportHeight()/2))
		return nil
	}
	if matchesAny(ev, "u", "ctrl+u") {
		ev.Handled = true
		h.moveHelpStart(-max(1, h.viewportHeight()/2))
		return nil
	}
	if matchesCaseSensitiveCommand(ev, "G") || ev.MatchesPressOrRepeat("end") {
		ev.Handled = true
		h.moveHelpStart(len(helpEntries))
		return nil
	}
	if ev.MatchesPressOrRepeat("g") || ev.MatchesPressOrRepeat("home") {
		ev.Handled = true
		h.moveHelpStart(-h.helpScrollStart)
	}
	return nil
}

func (h *Handler) onText(text string, _ bool, inBracketedPaste bool) error {
	if h.mode != searchMode {
		if !inBracketedPaste {
			h.inBracketedPaste = false
			h.bracketedPaste.Reset()
		}
		return nil
	}
	if inBracketedPaste {
		h.inBracketedPaste = true
		h.bracketedPaste.WriteString(text)
		return nil
	}
	if h.inBracketedPaste {
		text = h.bracketedPaste.String()
		h.inBracketedPaste = false
		h.bracketedPaste.Reset()
	} else if text == "" {
		return nil
	}
	text = sanitizeSearchText(text)
	if text == "" {
		return nil
	}
	h.query += text
	h.reSearch()
	h.drawScreen()
	return nil
}

func sanitizeSearchText(text string) string {
	text = wcswidth.StripEscapeCodes(text)
	if idx := strings.IndexAny(text, "\r\n"); idx >= 0 {
		text = text[:idx]
	}
	text = expandTabs(text)
	return strings.Map(func(ch rune) rune {
		if unicode.IsControl(ch) {
			return -1
		}
		return ch
	}, text)
}

func expandTabs(text string) string {
	if !strings.ContainsRune(text, '\t') {
		return text
	}
	var expanded strings.Builder
	expanded.Grow(len(text))
	column := 0
	for {
		idx := strings.IndexByte(text, '\t')
		if idx < 0 {
			expanded.WriteString(text)
			return expanded.String()
		}
		segment := text[:idx]
		expanded.WriteString(segment)
		column += wcswidth.Stringwidth(segment)
		spaces := 8 - column%8
		expanded.WriteString(strings.Repeat(" ", spaces))
		column += spaces
		text = text[idx+1:]
	}
}

func resolveMatchColumns(text string, spans []MatchSpan, width *wcswidth.WCWidthIterator) {
	if len(spans) == 0 {
		return
	}
	width.Reset()
	startIndex, endIndex := 0, 0
	for pos := 0; pos <= len(text); pos++ {
		column := width.CurrentWidth()
		for startIndex < len(spans) && spans[startIndex].startCol == pos {
			spans[startIndex].startCol = column
			startIndex++
		}
		for endIndex < len(spans) && spans[endIndex].endCol == pos {
			spans[endIndex].endCol = column
			endIndex++
		}
		if pos == len(text) {
			break
		}
		width.ParseByte(text[pos])
	}
}

func (h *Handler) onMouseEvent(ev *loop.MouseEvent) error {
	switch ev.Event_type {
	case loop.MOUSE_PRESS:
		if ev.Buttons&(loop.MOUSE_WHEEL_UP|loop.MOUSE_WHEEL_DOWN) != 0 {
			h.handleWheelEvent(ev.Buttons&(loop.MOUSE_WHEEL_UP) != 0)
		}
	}
	return nil
}

func (h *Handler) onResize(_old, newSize loop.ScreenSize) error {
	h.screenSize = newSize
	h.horizontalOffset = min(h.horizontalOffset, h.maxHorizontalOffset())
	h.helpScrollStart = min(h.helpScrollStart, max(0, len(helpEntries)-h.viewportHeight()))
	h.drawScreen()
	return nil
}

func (h *Handler) recordMatch(firstRow, lastRow int, rowOffsets []int, realIdx, matchEnd int) {
	matchIndex := len(h.matches)
	matchLine := -1
	for row := firstRow; row < lastRow; row++ {
		rowStart := 0
		if len(rowOffsets) > 0 {
			rowStart = rowOffsets[row-firstRow]
		}
		rowEnd := rowStart + len(h.lines[row].visible)
		if realIdx == matchEnd {
			if realIdx < rowEnd || (row == lastRow-1 && realIdx <= rowEnd) {
				matchLine = row
				break
			}
			continue
		}
		spanStart := max(realIdx, rowStart)
		spanEnd := min(matchEnd, rowEnd)
		if spanStart >= spanEnd {
			continue
		}
		localStart, localEnd := spanStart-rowStart, spanEnd-rowStart
		h.lines[row].matches = append(h.lines[row].matches, MatchSpan{startCol: localStart, endCol: localEnd, match: matchIndex})
		if matchLine < 0 {
			matchLine = row
		}
	}
	if matchLine >= 0 {
		h.matches = append(h.matches, matchLine)
	}
}

func (h *Handler) reSearch() {
	query := h.query
	queryLen := len(query)
	h.matches = h.matches[:0]
	h.currentMatch = -1
	h.searchError = ""
	for i := range h.lines {
		h.lines[i].matches = h.lines[i].matches[:0]
	}
	if queryLen == 0 {
		return
	}
	literal := regexp.QuoteMeta(query) == query
	var pattern *regexp.Regexp
	if !literal {
		var err error
		pattern, err = regexp.CompilePOSIX(query)
		if err != nil {
			h.searchError = err.Error()
			return
		}
	}

	columnWidth := wcswidth.CreateWCWidthIterator()
	var logicalText []byte
	var rowOffsets []int
	for firstRow := 0; firstRow < len(h.lines); {
		lastRow := firstRow + 1
		for lastRow < len(h.lines) && h.lines[lastRow].logicalLine == h.lines[firstRow].logicalLine {
			lastRow++
		}

		text := h.lines[firstRow].visible
		if lastRow-firstRow > 1 {
			logicalText = logicalText[:0]
			rowOffsets = rowOffsets[:0]
			for row := firstRow; row < lastRow; row++ {
				rowOffsets = append(rowOffsets, len(logicalText))
				logicalText = append(logicalText, h.lines[row].visible...)
			}
			text = utils.UnsafeBytesToString(logicalText)
		} else {
			rowOffsets = rowOffsets[:0]
		}
		if literal {
			offset := 0
			for {
				idx := strings.Index(text[offset:], query)
				if idx == -1 {
					break
				}
				realIdx := offset + idx
				matchEnd := realIdx + queryLen
				h.recordMatch(firstRow, lastRow, rowOffsets, realIdx, matchEnd)
				offset = matchEnd
			}
		} else {
			for _, location := range pattern.FindAllStringIndex(text, -1) {
				h.recordMatch(firstRow, lastRow, rowOffsets, location[0], location[1])
			}
		}
		for row := firstRow; row < lastRow; row++ {
			resolveMatchColumns(h.lines[row].visible, h.lines[row].matches, columnWidth)
		}
		firstRow = lastRow
	}

	if h.searchDirection == searchBackward {
		for index := len(h.matches) - 1; index >= 0; index-- {
			if h.matches[index] <= h.searchAnchor {
				h.currentMatch = index
				break
			}
		}
		if h.currentMatch < 0 && len(h.matches) > 0 {
			h.currentMatch = len(h.matches) - 1
		}
	} else {
		for index, line := range h.matches {
			if line >= h.searchAnchor {
				h.currentMatch = index
				break
			}
		}
		if h.currentMatch < 0 && len(h.matches) > 0 {
			h.currentMatch = 0
		}
	}
	if h.currentMatch >= 0 && h.searchCount > 1 {
		step := (h.searchCount - 1) % len(h.matches)
		if h.searchDirection == searchBackward {
			h.currentMatch = (h.currentMatch - step + len(h.matches)) % len(h.matches)
		} else {
			h.currentMatch = (h.currentMatch + step) % len(h.matches)
		}
	}
	h.revealCurrentMatch()
}

type screenLayout struct {
	viewsHeight            int
	panelTopY, searchBarY  int
	panelMiddleY, toolbarY int
	panelBottomY           int
}

var helpEntries = [][2]string{
	{"/regex  ?regex", "Search forward / backward"},
	{"n  N", "Repeat search (wraps) / reverse direction"},
	{"j e ^E ^N Enter Down", "Forward one line"},
	{"k y ^Y ^K ^P Up", "Backward one line"},
	{"Space f ^F ^V z PgDn", "Forward one page"},
	{"b ^B Esc-v w PgUp", "Backward one page"},
	{"d ^D  /  u ^U", "Forward / backward half a page"},
	{"g < Home  /  G > End", "First / last line"},
	{"N g/G", "Go to line N"},
	{"N p/%", "Go to N percent"},
	{"Left Right Esc-( Esc-)", "Scroll horizontally half a screen"},
	{"Ctrl-Left  Ctrl-Right", "First / last horizontal column"},
	{"N command", "Repeat a movement or search N times"},
	{"r R ^R ^L", "Redraw"},
	{"q  Q  Esc", "Quit"},
	{"h  H", "Show / close this help"},
}

func calculateScreenLayout(height int) screenLayout {
	switch {
	case height >= 7:
		return screenLayout{
			viewsHeight:  height - 5,
			panelTopY:    height - 4,
			searchBarY:   height - 3,
			panelMiddleY: height - 2,
			toolbarY:     height - 1,
			panelBottomY: height,
		}
	case height >= 5:
		return screenLayout{
			viewsHeight:  height - 4,
			panelTopY:    height - 3,
			searchBarY:   height - 2,
			toolbarY:     height - 1,
			panelBottomY: height,
		}
	case height == 4:
		return screenLayout{viewsHeight: 1, searchBarY: 2, toolbarY: 3, panelBottomY: 4}
	case height == 3:
		return screenLayout{viewsHeight: 1, searchBarY: 2, toolbarY: 3}
	case height == 2:
		return screenLayout{viewsHeight: 1, searchBarY: 2}
	case height == 1:
		return screenLayout{searchBarY: 1}
	default:
		return screenLayout{}
	}
}

func searchTextTail(text string, maxWidth int) (tail string, hidden bool) {
	if wcswidth.Stringwidth(text) <= maxWidth {
		return text, false
	}
	if maxWidth <= 1 {
		return "", text != ""
	}
	remaining := maxWidth - 1 // Reserve one cell for the left-truncation marker.
	graphemes := wcswidth.SplitIntoGraphemes(text)
	start, width := len(graphemes), 0
	for start > 0 {
		w := wcswidth.Stringwidth(graphemes[start-1])
		if width+w > remaining {
			break
		}
		start--
		width += w
	}
	return strings.Join(graphemes[start:], ""), true
}

func (h *Handler) drawScreen() {
	h.lp.StartAtomicUpdate()
	defer h.lp.EndAtomicUpdate()
	h.lp.ClearScreen()

	height := int(h.screenSize.HeightCells)
	layout := calculateScreenLayout(height)

	// Draw the scrollback or key help above the bottom panel.
	if h.mode == helpMode {
		h.drawHelpViews(1, layout.viewsHeight)
	} else {
		h.drawViews(1, layout.viewsHeight)
	}
	h.drawPanelBorder(layout.panelTopY, "╭", "╮")

	// Draw search bar/status immediately above the toolbar.
	searchBar := ""
	cursorX := 1
	if h.mode == searchMode {
		prompt := "/"
		if h.searchDirection == searchBackward {
			prompt = "?"
		}
		if h.searchCount > 1 {
			prompt = strconv.Itoa(h.searchCount) + prompt
		}
		maxQueryWidth := max(0, int(h.screenSize.WidthCells)-4-wcswidth.Stringwidth(prompt))
		tail, hidden := searchTextTail(h.query, maxQueryWidth)
		shownQuery := tail
		if hidden && maxQueryWidth > 0 {
			shownQuery = h.lp.SprintStyled("dim", "‹") + tail
		}
		searchBar = h.lp.SprintStyled("fg=bright-yellow bold", prompt) + shownQuery
		cursorX = min(3+wcswidth.Stringwidth(prompt)+wcswidth.Stringwidth(wcswidth.StripEscapeCodes(shownQuery)), max(1, int(h.screenSize.WidthCells)-1))
	} else if h.mode == helpMode {
		searchBar = h.lp.SprintStyled("fg=yellow dim", "Less-style key help")
	} else if h.pendingCount != 0 {
		searchBar = h.lp.SprintStyled("fg=yellow dim", fmt.Sprintf("Count: %d", h.pendingCount))
	} else if h.query != "" {
		prompt := "/"
		if h.searchDirection == searchBackward {
			prompt = "?"
		}
		searchBar = h.lp.SprintStyled("fg=yellow dim", "Search "+prompt) + h.query
	} else {
		searchBar = h.lp.SprintStyled("fg=yellow dim", "Press / or ? to search")
	}
	h.drawPanelLine(layout.searchBarY, " "+searchBar, "")
	h.drawPanelBorder(layout.panelMiddleY, "├", "┤")

	// Draw key hints footer
	footer := ""
	if h.mode == searchMode {
		footer = h.toolbarHint("[Enter/Esc]", "Command mode")
	} else if h.mode == helpMode {
		footer = h.toolbarHint("[j/k]", "Scroll") +
			h.toolbarHint("[g/G]", "Ends") +
			h.toolbarHint("[q/Esc/h]", "Back")
	} else if h.query != "" {
		footer = h.toolbarHint("[/?]", "Search") +
			h.toolbarHint("[n/N]", "Match") +
			h.toolbarHint("[j/k]", "Line") +
			h.toolbarHint("[Space/b]", "Page") +
			h.toolbarHint("[d/u]", "Half") +
			h.toolbarHint("[h]", "Help") +
			h.toolbarHint("[q/Esc]", "Quit")
	} else {
		footer = h.toolbarHint("[/?]", "Search") +
			h.toolbarHint("[j/k]", "Line") +
			h.toolbarHint("[Space/b]", "Page") +
			h.toolbarHint("[d/u]", "Half") +
			h.toolbarHint("[h]", "Help") +
			h.toolbarHint("[q/Esc]", "Quit")
	}

	matchCount := ""
	if h.mode == helpMode {
		matchCount = ""
	} else if h.searchError != "" {
		matchCount = h.lp.SprintStyled("fg=yellow dim", "  Invalid pattern")
	} else if h.query != "" {
		current := 0
		if h.currentMatch >= 0 {
			current = h.currentMatch + 1
		}
		matchCount = h.lp.SprintStyled("fg=yellow dim", fmt.Sprintf("  %d/%d", current, len(h.matches)))
	}
	h.drawPanelLine(layout.toolbarY, " "+footer, matchCount)
	h.drawPanelBorder(layout.panelBottomY, "╰", "╯")

	// Position the cursor only while editing the search query.
	h.lp.SetCursorVisible(h.mode == searchMode)
	if h.mode == searchMode && layout.searchBarY >= 1 {
		h.lp.MoveCursorTo(cursorX, layout.searchBarY)
	}
}

func (h *Handler) toolbarHint(keys, label string) string {
	return h.lp.SprintStyled("fg=yellow dim underline", keys) + h.lp.SprintStyled("dim", label+" ")
}

func (h *Handler) panelBorderStyle() string {
	if h.mode == searchMode {
		return "fg=bright-yellow"
	}
	return "fg=yellow dim"
}

func (h *Handler) drawPanelBorder(y int, left, right string) {
	if y < 1 {
		return
	}
	width := int(h.screenSize.WidthCells)
	if width < 1 {
		return
	}
	border := left
	if width > 1 {
		border += strings.Repeat("─", max(0, width-2)) + right
	}
	h.lp.MoveCursorTo(1, y)
	h.lp.QueueWriteString(h.lp.SprintStyled(h.panelBorderStyle(), border))
}

func (h *Handler) drawPanelLine(y int, left, right string) {
	if y < 1 {
		return
	}
	width := int(h.screenSize.WidthCells)
	if width < 1 {
		return
	}
	if width == 1 {
		h.lp.MoveCursorTo(1, y)
		h.lp.QueueWriteString(h.lp.SprintStyled(h.panelBorderStyle(), "│"))
		return
	}
	innerWidth := width - 2
	right, rightWidth := wcswidth.TruncateToVisualLengthWithWidth(right, innerWidth)
	left, leftWidth := wcswidth.TruncateToVisualLengthWithWidth(left, innerWidth-rightWidth)
	edge := h.lp.SprintStyled(h.panelBorderStyle(), "│")
	h.lp.MoveCursorTo(1, y)
	h.lp.QueueWriteString(edge + left + "\x1b[m" + strings.Repeat(" ", innerWidth-leftWidth-rightWidth) + right + "\x1b[m" + edge)
}

func (h *Handler) drawHelpViews(startY, maxRows int) {
	h.helpScrollStart = min(h.helpScrollStart, max(0, len(helpEntries)-maxRows))
	end := min(h.helpScrollStart+maxRows, len(helpEntries))
	width := int(h.screenSize.WidthCells)
	for row, entry := range helpEntries[h.helpScrollStart:end] {
		text := h.lp.SprintStyled("fg=yellow dim underline", entry[0]) + h.lp.SprintStyled("dim", "  "+entry[1])
		text, textWidth := wcswidth.TruncateToVisualLengthWithWidth(text, width)
		h.lp.MoveCursorTo(1, startY+row)
		h.lp.QueueWriteString(text + "\x1b[m" + strings.Repeat(" ", max(0, width-textWidth)))
	}
}

func (h *Handler) drawViews(startY, maxRows int) {
	h.scrollStart = min(h.scrollStart, max(0, len(h.lines)-maxRows))

	end := min(h.scrollStart+maxRows, len(h.lines))
	for row, line := range h.lines[h.scrollStart:end] {
		y := startY + row
		width := int(h.screenSize.WidthCells)
		h.lp.MoveCursorTo(1, y)
		rendered, offset := h.renderDisplayLine(line, h.horizontalOffset)
		h.lp.QueueWriteString(rendered)
		for _, span := range line.matches {
			startCol := min(max(0, span.startCol-offset), width)
			endCol := min(max(startCol, span.endCol-offset), width)
			if startCol >= endCol {
				continue
			}
			style := "reverse"
			if span.match == h.currentMatch {
				style = "reverse bold"
			}
			h.lp.StyleRegion(style, startCol, y-1, endCol-1, y-1)
		}
	}
}

func (h *Handler) renderDisplayLine(line DisplayLine, offset int) (string, int) {
	text := line.hyperlinkPrefix + line.raw
	prefix := ""
	if offset > 0 {
		// Emit the hidden prefix to establish its ANSI state, then overwrite it from column one.
		var skippedWidth int
		prefix, skippedWidth = wcswidth.TruncateToVisualLengthWithWidth(text, offset)
		text = text[len(prefix):]
		offset = skippedWidth
		prefix += "\r"
	}
	eraseRemainder := ""
	if width := int(h.screenSize.WidthCells); width > 0 {
		var textWidth int
		text, textWidth = wcswidth.TruncateToVisualLengthWithWidth(text, width)
		if textWidth < width {
			eraseRemainder = "\x1b[K"
		}
	}
	return prefix + text + closeHyperlink + "\x1b[m" + eraseRemainder, offset
}

func (h *Handler) viewportHeight() int {
	return max(1, calculateScreenLayout(int(h.screenSize.HeightCells)).viewsHeight)
}

func (h *Handler) maxScrollStart() int {
	return max(0, len(h.lines)-h.viewportHeight())
}

func (h *Handler) moveScrollStart(delta int) {
	oldScrollStart := h.scrollStart
	h.scrollStart = min(max(0, h.scrollStart+delta), h.maxScrollStart())
	if h.scrollStart == oldScrollStart {
		h.lp.Beep()
	}
	h.drawScreen()
}

func (h *Handler) moveHelpStart(delta int) {
	oldStart := h.helpScrollStart
	h.helpScrollStart = min(max(0, h.helpScrollStart+delta), max(0, len(helpEntries)-h.viewportHeight()))
	if h.helpScrollStart == oldStart {
		h.lp.Beep()
	}
	h.drawScreen()
}

func (h *Handler) maxHorizontalOffset() int {
	maxWidth := 0
	for _, line := range h.lines {
		maxWidth = max(maxWidth, line.width)
	}
	return max(0, maxWidth-int(h.screenSize.WidthCells))
}

func (h *Handler) moveHorizontal(delta int) {
	oldOffset := h.horizontalOffset
	h.horizontalOffset = min(max(0, h.horizontalOffset+delta), h.maxHorizontalOffset())
	if h.horizontalOffset == oldOffset {
		h.lp.Beep()
	}
	h.drawScreen()
}

func (h *Handler) revealCurrentMatch() {
	if h.currentMatch < 0 || h.currentMatch >= len(h.matches) || h.screenSize.WidthCells < 1 {
		return
	}
	width := int(h.screenSize.WidthCells)
	for row := h.matches[h.currentMatch]; row < len(h.lines); row++ {
		for _, span := range h.lines[row].matches {
			if span.match != h.currentMatch {
				continue
			}
			if span.startCol < h.horizontalOffset {
				h.horizontalOffset = span.startCol
			} else if span.endCol > h.horizontalOffset+width {
				h.horizontalOffset = max(span.startCol, span.endCol-width)
			}
			h.horizontalOffset = min(h.horizontalOffset, h.maxHorizontalOffset())
			return
		}
		if h.lines[row].logicalLine != h.lines[h.matches[h.currentMatch]].logicalLine {
			break
		}
	}
}

func (h *Handler) moveCurrentMatch(forwards bool, count int) {
	if h.currentMatch < 0 || len(h.matches) == 0 {
		h.lp.Beep()
		return
	}

	count %= len(h.matches)
	if forwards {
		h.currentMatch = (h.currentMatch + count) % len(h.matches)
	} else {
		h.currentMatch = (h.currentMatch - count + len(h.matches)) % len(h.matches)
	}
	h.scrollStart = h.matches[h.currentMatch]
	h.revealCurrentMatch()
	h.drawScreen()
}

var wheelScrollMultiplier = sync.OnceValue(func() float64 {
	ans := kitty.KittyConfigDefaults.Wheel_scroll_multiplier
	handleLine := func(key, val string) error {
		if key == "wheel_scroll_multiplier" {
			v, err := strconv.ParseFloat(val, 64)
			if err == nil {
				ans = v
			}
		}
		return nil
	}
	config.ReadKittyConfig(handleLine)
	return ans
})

func (h *Handler) handleWheelEvent(up bool) {
	amt := int(math.Round(wheelScrollMultiplier()))
	if amt == 0 {
		amt = 1
	}
	if up {
		amt *= -1
	}

	if h.mode == helpMode {
		h.moveHelpStart(amt)
	} else {
		h.moveScrollStart(amt)
	}
}

const closeHyperlink = "\x1b]8;;\x1b\\"

func activeHyperlinkAfter(text, active string) string {
	parser := wcswidth.EscapeCodeParser{
		HandleOSC: func(data []byte) error {
			body := utils.UnsafeBytesToString(data)
			if strings.HasPrefix(body, "8;") {
				_, uri, found := strings.Cut(body[2:], ";")
				if !found {
					return nil
				}
				if uri == "" {
					active = ""
				} else {
					active = "\x1b]" + body + "\x1b\\"
				}
			}
			return nil
		},
	}
	_ = parser.ParseString(text)
	return active
}

func parseDisplayLine(inputData string) []DisplayLine {
	if inputData == "" {
		return nil
	}
	result := make([]DisplayLine, 0, strings.Count(inputData, "\r")+strings.Count(inputData, "\n")+1)
	activeHyperlink := ""
	start, logicalLine := 0, 0
	appendLine := func(end int) {
		// Truncation helpers treat tabs as zero-width, so normalize them before searching or drawing.
		raw := expandTabs(inputData[start:end])
		visible := wcswidth.StripEscapeCodes(raw)
		result = append(result, DisplayLine{
			raw:             raw,
			visible:         visible,
			width:           wcswidth.Stringwidth(visible),
			hyperlinkPrefix: activeHyperlink,
			logicalLine:     logicalLine,
		})
		activeHyperlink = activeHyperlinkAfter(raw, activeHyperlink)
	}
	for pos := 0; pos < len(inputData); pos++ {
		if inputData[pos] != '\r' && inputData[pos] != '\n' {
			continue
		}
		appendLine(pos)
		if inputData[pos] == '\n' {
			logicalLine++
		} else if pos+1 < len(inputData) && inputData[pos+1] == '\n' {
			logicalLine++
			pos++
		}
		start = pos + 1
	}
	if start < len(inputData) {
		appendLine(len(inputData))
	}
	return result
}

func main(_ *cli.Command, opts *Options, _ []string) (rc int, err error) {
	if tty.IsTerminal(os.Stdin.Fd()) {
		return 1, fmt.Errorf("This kitten must only be run via the search action mapped to a shortcut in kitty.conf")
	}

	selection := sanitizeSearchText(opts.Selection)
	stdin, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 1, fmt.Errorf("failed to read scrollback: %w", err)
	}
	inputData := utils.UnsafeBytesToString(stdin)
	lines := parseDisplayLine(inputData)

	lp, err := loop.New()
	if err != nil {
		return 1, err
	}
	handler := &Handler{
		lp:           lp,
		lines:        lines,
		query:        selection,
		currentMatch: -1,
		mode:         commandMode,
		scrollStart:  len(lines),
	}
	lp.MouseTrackingMode(loop.FULL_MOUSE_TRACKING)
	lp.OnInitialize = func() (string, error) {
		return handler.initialize()
	}
	lp.OnFinalize = func() string { return "" }
	lp.OnKeyEvent = handler.onKeyEvent
	lp.OnText = handler.onText
	lp.OnMouseEvent = handler.onMouseEvent
	lp.OnResize = handler.onResize

	err = lp.Run()
	if err != nil {
		return 1, err
	}
	ds := lp.DeathSignalName()
	if ds != "" {
		fmt.Println("Killed by signal:", ds)
		lp.KillIfSignalled()
		return
	}
	rc = lp.ExitCode()
	return
}

func EntryPoint(parent *cli.Command) {
	create_cmd(parent, main)
}
