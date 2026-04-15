package search

import (
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/kovidgoyal/kitty"
	"github.com/kovidgoyal/kitty/tools/cli"
	"github.com/kovidgoyal/kitty/tools/config"
	"github.com/kovidgoyal/kitty/tools/tty"
	"github.com/kovidgoyal/kitty/tools/tui/loop"
	"github.com/kovidgoyal/kitty/tools/tui/sgr"
	"github.com/kovidgoyal/kitty/tools/utils"
	"github.com/kovidgoyal/kitty/tools/wcswidth"
)

var debugPrintln = tty.DebugPrintln
var _ = debugPrintln
var _ = fmt.Print

type DisplayLine struct {
	raw    string
	plain  string
	matchs []*Match
}

type RenderedLine struct {
	raw  string
	line int
}

type Search struct {
	text         string
	currentMatch *Match
}

type Match struct {
	line int

	start   int
	end     int
	current bool

	prev *Match
	next *Match
}

type Handler struct {
	lp               *loop.Loop
	lines            []DisplayLine
	search           Search
	screenSize       loop.ScreenSize
	viewsStartY      int
	viewsHeight      int
	scrollStart      int
	wrappedWidth     int
	matchCount       int
	currentMatchIdx  int
	renderedDirty    bool
	renderedLines    []RenderedLine
	lineRowOffsets   []int
	shortcutTracker  config.ShortcutTracker
	keyboardShortcut []*config.KeyAction
}

func (h *Handler) initialize() (string, error) {
	sz, err := h.lp.ScreenSize()
	if err != nil {
		return "", err
	}
	h.screenSize = sz
	h.lp.SetCursorVisible(true)
	h.lp.SetCursorShape(loop.BAR_CURSOR, true)
	h.lp.AllowLineWrapping(false)
	h.lp.SetWindowTitle("Search")

	h.keyboardShortcut = config.ResolveShortcuts(NewConfig().KeyboardShortcuts)

	h.reSearch()
	h.drawScreen()
	h.lp.SendOverlayReady()
	return "", nil
}

func (h *Handler) onKeyEvent(ev *loop.KeyEvent) error {
	if ev.MatchesPressOrRepeat("escape") {
		ev.Handled = true
		if h.search.text != "" {
			h.search.text = ""
			h.reSearch()
			h.drawScreen()
		} else {
			h.lp.Quit(0)
		}
		return nil
	}
	if ev.MatchesPressOrRepeat("up") {
		ev.Handled = true
		h.moveScrollStart(-1)
		return nil
	}
	if ev.MatchesPressOrRepeat("down") {
		ev.Handled = true
		h.moveScrollStart(1)
		return nil
	}
	if ev.MatchesPressOrRepeat("enter") {
		ev.Handled = true
		if h.search.currentMatch == nil {
			h.lp.Beep()
			return nil
		}

		h.search.currentMatch.current = false
		h.search.currentMatch = h.search.currentMatch.prev
		h.search.currentMatch.current = true

		if h.currentMatchIdx > 0 {
			h.currentMatchIdx--
		}
		if h.currentMatchIdx <= 0 {
			h.currentMatchIdx = h.matchCount
		}
		h.renderedDirty = true
		h.scrollStart = h.scrollStartForMatch(h.search.currentMatch)
		h.drawScreen()
		return nil
	}
	if ev.MatchesPressOrRepeat("shift+enter") {
		ev.Handled = true
		if h.search.currentMatch == nil {
			h.lp.Beep()
			return nil
		}

		h.search.currentMatch.current = false
		h.search.currentMatch = h.search.currentMatch.next
		h.search.currentMatch.current = true

		if h.currentMatchIdx <= h.matchCount {
			h.currentMatchIdx++
		}
		if h.currentMatchIdx > h.matchCount {
			h.currentMatchIdx = 1
		}
		h.renderedDirty = true
		h.scrollStart = h.scrollStartForMatch(h.search.currentMatch)
		h.drawScreen()
		return nil
	}
	if ac := h.shortcutTracker.Match(ev, h.keyboardShortcut); ac != nil {
		ev.Handled = true
		switch ac.Name {
		case "selection_up":
			h.moveScrollStart(-1)
		case "selection_down":
			h.moveScrollStart(1)
		}
		return nil
	}

	if ev.MatchesPressOrRepeat("page_up") {
		ev.Handled = true
		delta := max(1, int(h.screenSize.HeightCells)-4)
		h.moveScrollStart(-delta)
		return nil
	}
	if ev.MatchesPressOrRepeat("page_down") {
		ev.Handled = true
		delta := max(1, int(h.screenSize.HeightCells)-4)
		h.moveScrollStart(delta)
		return nil
	}
	if ev.MatchesPressOrRepeat("home") || ev.MatchesPressOrRepeat("ctrl+home") {
		ev.Handled = true
		h.moveScrollStart(-h.scrollStart)
		return nil
	}
	if ev.MatchesPressOrRepeat("end") || ev.MatchesPressOrRepeat("ctrl+end") {
		ev.Handled = true
		h.ensureRenderedLines()
		h.moveScrollStart(len(h.renderedLines) - h.scrollStart)
		return nil
	}
	if ev.MatchesPressOrRepeat("backspace") {
		ev.Handled = true
		if h.search.text != "" {
			g := wcswidth.SplitIntoGraphemes(h.search.text)
			h.search.text = strings.Join(g[:len(g)-1], "")
			h.reSearch()
			h.drawScreen()
		} else {
			h.lp.Beep()
		}
		return nil
	}
	return nil
}

func (h *Handler) onText(text string, fromKeyEvent bool, inBracketedPaste bool) error {
	h.search.text += text
	h.reSearch()
	h.drawScreen()
	return nil
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

func (h *Handler) reSearch() {
	query := h.search.text

	h.matchCount = 0
	h.search.currentMatch = nil

	var firstMatch *Match
	var lastMatch *Match
	for i := range h.lines {
		line := &h.lines[i]
		line.matchs = nil
		if query == "" {
			continue
		}

		queryLen := len(query)
		offset := 0
		for {
			idx := strings.Index(line.plain[offset:], query)
			if idx == -1 {
				break
			}

			realIdx := offset + idx

			match := &Match{
				line:    i,
				start:   realIdx,
				end:     realIdx + queryLen,
				current: false,
			}

			if firstMatch == nil {
				firstMatch = match
			} else {
				lastMatch.next = match
				match.prev = lastMatch
			}
			lastMatch = match
			line.matchs = append(line.matchs, match)

			h.matchCount++
			offset = match.end
		}
	}

	h.currentMatchIdx = h.matchCount
	if firstMatch != nil && lastMatch != nil {
		lastMatch.next = firstMatch
		firstMatch.prev = lastMatch

		lastMatch.current = true
		h.search.currentMatch = lastMatch
	}
	h.renderedDirty = true
}

func (h *Handler) drawScreen() {
	h.lp.StartAtomicUpdate()
	defer h.lp.EndAtomicUpdate()
	h.lp.ClearScreen()

	height := int(h.screenSize.HeightCells)

	// Layout: line 1 = search bar, lines 2..height-2 = views,
	// line height-1 = help text, line height = key hints
	searchBarY := 1
	viewsStartY := 2
	hintsY := height
	viewsHeight := max(hintsY-viewsStartY, 1)

	h.viewsStartY = viewsStartY
	h.viewsHeight = viewsHeight

	// Draw search bar
	h.lp.MoveCursorTo(1, searchBarY)
	h.lp.QueueWriteString(h.lp.SprintStyled("fg=bright-yellow", "> "))
	h.lp.QueueWriteString(h.search.text)

	// Draw views
	h.drawViews(viewsStartY, viewsHeight)

	// Draw key hints footer
	h.lp.MoveCursorTo(1, hintsY)
	footer := h.lp.SprintStyled("fg=bright-yellow", "[Esc]") + " Quit  "
	if len(h.search.text) != 0 {
		footer += h.lp.SprintStyled("fg=bright-yellow", "Enter/Shift+Enter") + " Navigate"
	}

	matchCount := ""
	if h.search.text != "" {
		matchCount = fmt.Sprintf("  %d/%d", h.currentMatchIdx, h.matchCount)
	}
	h.lp.QueueWriteString(" " + footer + h.lp.SprintStyled("dim", matchCount))

	// Position cursor at end of search text for typing
	h.lp.MoveCursorTo(3+wcswidth.Stringwidth(h.search.text), searchBarY)
}

func (h *Handler) drawViews(startY, maxRows int) {
	h.ensureRenderedLines()
	h.scrollStart = min(h.scrollStart, max(0, len(h.renderedLines)-maxRows))

	end := min(h.scrollStart+maxRows, len(h.renderedLines))
	for row, line := range h.renderedLines[h.scrollStart:end] {
		h.lp.MoveCursorTo(1, startY+row)
		h.lp.QueueWriteString(line.raw)
	}
}

func (h *Handler) moveScrollStart(delta int) {
	h.ensureRenderedLines()
	maxScrollStart := max(0, len(h.renderedLines)-max(1, h.viewsHeight))
	next := min(max(0, h.scrollStart+delta), maxScrollStart)
	if next == h.scrollStart {
		h.lp.Beep()
	}
	h.scrollStart = next
	h.drawScreen()
}

type KittyOpts struct {
	WheelScrollMultiplier float64
	CopyOnSelect          bool
}

func readRelevantKittyOpts() KittyOpts {
	ans := KittyOpts{WheelScrollMultiplier: kitty.KittyConfigDefaults.Wheel_scroll_multiplier}
	handleLine := func(key, val string) error {
		switch key {
		case "wheel_scroll_multiplier":
			v, err := strconv.ParseFloat(val, 64)
			if err == nil {
				ans.WheelScrollMultiplier = v
			}
		case "copy_on_select":
			ans.CopyOnSelect = strings.ToLower(val) == "clipboard"
		}
		return nil
	}
	config.ReadKittyConfig(handleLine)
	return ans
}

var RelevantKittyOpts = sync.OnceValue(func() KittyOpts {
	return readRelevantKittyOpts()
})

func (h *Handler) handleWheelEvent(up bool) {
	amt := int(math.Round(RelevantKittyOpts().WheelScrollMultiplier))
	if amt == 0 {
		amt = 1
	}
	if up {
		amt *= -1
	}

	h.moveScrollStart(amt)
}

func parseDisplayLines(inputData string) []DisplayLine {
	lines := strings.Split(strings.ReplaceAll(inputData, "\r\n", "\n"), "\n")
	result := make([]DisplayLine, 0, len(lines))
	for _, line := range lines {
		raw, plain := sanitizeDisplayLine(line)
		result = append(result, DisplayLine{raw: raw, plain: plain})
	}
	return result
}

func sanitizeDisplayLine(line string) (raw, plain string) {
	var rawBuf, plainBuf strings.Builder
	rawBuf.Grow(len(line))
	plainBuf.Grow(len(line))

	parser := wcswidth.EscapeCodeParser{
		HandleRune: func(ch rune) error {
			rawBuf.WriteRune(ch)
			plainBuf.WriteRune(ch)
			return nil
		},
		HandleCSI: func(data []byte) error {
			if len(data) > 0 && data[len(data)-1] == 'm' {
				rawBuf.WriteString("\x1b[")
				rawBuf.Write(data)
			}
			return nil
		},
		HandleOSC: func(data []byte) error {
			if isOSC8Payload(data) {
				rawBuf.WriteString("\x1b]")
				rawBuf.Write(data)
				rawBuf.WriteString("\x1b\\")
			}
			return nil
		},
	}
	_ = parser.ParseString(line)
	return rawBuf.String(), plainBuf.String()
}

func isOSC8Payload(data []byte) bool {
	return len(data) >= 2 && data[0] == '8' && data[1] == ';'
}

type activeSGRState struct {
	state sgr.SGR
}

func (s *activeSGRState) apply(raw string) {
	if csiResetsSGR(raw) {
		s.state = sgr.SGR{}
	}
	s.state.ApplySGR(sgr.SGRFromCSI(raw))
}

func (s activeSGRState) openingEscapeCodes() string {
	csi := s.state.AsCSI()
	if csi == "" {
		return ""
	}
	return "\x1b[" + csi
}

func (s activeSGRState) closingEscapeCodes() string {
	if s.state.IsEmpty() {
		return ""
	}
	return "\x1b[m"
}

func csiResetsSGR(raw string) bool {
	raw = strings.TrimSuffix(raw, "m")
	if raw == "" {
		return true
	}
	for _, part := range strings.Split(raw, ";") {
		if part == "0" || strings.HasPrefix(part, "0:") {
			return true
		}
	}
	return false
}

type activeHyperlinkState struct {
	params string
	url    string
}

func (s *activeHyperlinkState) apply(raw string) {
	parts := strings.SplitN(raw, ";", 3)
	if len(parts) != 3 || parts[0] != "8" {
		return
	}
	s.params, s.url = parts[1], parts[2]
}

func (s activeHyperlinkState) openingEscapeCodes() string {
	if s.params == "" && s.url == "" {
		return ""
	}
	return "\x1b]8;" + s.params + ";" + s.url + "\x1b\\"
}

func (s activeHyperlinkState) closingEscapeCodes() string {
	if s.params == "" && s.url == "" {
		return ""
	}
	return "\x1b]8;;\x1b\\"
}

func wrapDisplayLine(raw string, width int) []string {
	if raw == "" || width <= 0 {
		return []string{raw}
	}

	rows := make([]string, 0, max(1, (wcswidth.Stringwidth(raw)+width-1)/width))
	row := make([]byte, 0, len(raw)+32)
	rowWidth := 0
	sgrState := activeSGRState{}
	hyperlinkState := activeHyperlinkState{}

	appendOpenStates := func() {
		row = append(row, sgrState.openingEscapeCodes()...)
		row = append(row, hyperlinkState.openingEscapeCodes()...)
	}
	appendClosedRow := func() {
		closed := make([]byte, 0, len(row)+32)
		closed = append(closed, row...)
		closed = append(closed, sgrState.closingEscapeCodes()...)
		closed = append(closed, hyperlinkState.closingEscapeCodes()...)
		rows = append(rows, utils.UnsafeBytesToString(closed))
	}
	startNewRow := func() {
		row = row[:0]
		rowWidth = 0
		appendOpenStates()
	}

	parser := wcswidth.EscapeCodeParser{
		HandleRune: func(ch rune) error {
			graphemeWidth := wcswidth.Stringwidth(string(ch))
			if rowWidth > 0 && rowWidth+graphemeWidth > width {
				appendClosedRow()
				startNewRow()
			}
			row = utf8.AppendRune(row, ch)
			rowWidth += graphemeWidth
			return nil
		},
		HandleCSI: func(data []byte) error {
			if len(data) > 0 && data[len(data)-1] == 'm' {
				row = append(row, 0x1b, '[')
				row = append(row, data...)
				sgrState.apply(utils.UnsafeBytesToString(data))
			}
			return nil
		},
		HandleOSC: func(data []byte) error {
			if isOSC8Payload(data) {
				row = append(row, 0x1b, ']')
				row = append(row, data...)
				row = append(row, 0x1b, '\\')
				hyperlinkState.apply(utils.UnsafeBytesToString(data))
			}
			return nil
		},
	}
	_ = parser.ParseString(raw)
	appendClosedRow()
	return rows
}

func (h *Handler) renderedRawForLine(line *DisplayLine) string {
	if len(line.matchs) == 0 {
		return line.raw
	}
	spans := make([]*sgr.Span, 0, len(line.matchs))
	for _, m := range line.matchs {
		span := sgr.NewSpan(m.start, m.end-m.start).SetForeground("black").SetClosingForeground(nil).SetClosingBackground(nil)
		if m.current {
			span.SetBackground("orange")
		} else {
			span.SetBackground("yellow")
		}
		spans = append(spans, span)
	}
	return sgr.InsertFormatting(line.raw, spans...)
}

func (h *Handler) ensureRenderedLines() {
	width := max(1, int(h.screenSize.WidthCells))
	if !h.renderedDirty && h.wrappedWidth == width {
		return
	}

	h.wrappedWidth = width
	h.renderedLines = h.renderedLines[:0]
	if cap(h.lineRowOffsets) < len(h.lines) {
		h.lineRowOffsets = make([]int, len(h.lines))
	} else {
		h.lineRowOffsets = h.lineRowOffsets[:len(h.lines)]
	}

	for i := range h.lines {
		h.lineRowOffsets[i] = len(h.renderedLines)
		for _, row := range wrapDisplayLine(h.renderedRawForLine(&h.lines[i]), width) {
			h.renderedLines = append(h.renderedLines, RenderedLine{raw: row, line: i})
		}
	}
	h.renderedDirty = false
}

func (h *Handler) scrollStartForMatch(m *Match) int {
	if m == nil || m.line < 0 || m.line >= len(h.lines) {
		return h.scrollStart
	}
	h.ensureRenderedLines()
	if h.wrappedWidth <= 0 {
		return h.scrollStart
	}

	row := h.lineRowOffsets[m.line]
	if m.start > 0 {
		row += wcswidth.Stringwidth(h.lines[m.line].plain[:m.start]) / h.wrappedWidth
	}
	return row
}

func main(_ *cli.Command, opts *Options, _ []string) (rc int, err error) {
	if tty.IsTerminal(os.Stdin.Fd()) {
		return 1, fmt.Errorf("This kitten must only be run via the search action mapped to a shortcut in kitty.conf")
	}

	var (
		inputData string
		selection = opts.Selection
	)
	stdin, err := io.ReadAll(os.Stdin)
	if err != nil {
		inputData = ""
	} else {
		inputData = utils.UnsafeBytesToString(stdin)
	}
	lines := parseDisplayLines(inputData)

	lp, err := loop.New()
	if err != nil {
		return 1, err
	}
	handler := &Handler{
		lp:    lp,
		lines: lines,
		search: Search{
			text: selection,
		},
		scrollStart:   len(lines),
		renderedDirty: true,
	}
	lp.MouseTrackingMode(loop.FULL_MOUSE_TRACKING)
	lp.OnInitialize = func() (string, error) {
		return handler.initialize()
	}
	lp.OnFinalize = func() string { return "" }
	lp.OnKeyEvent = handler.onKeyEvent
	lp.OnText = handler.onText
	lp.OnMouseEvent = handler.onMouseEvent
	lp.OnResize = func(oldSize, newSize loop.ScreenSize) error {
		handler.screenSize = newSize
		handler.renderedDirty = true
		handler.drawScreen()
		return nil
	}

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
