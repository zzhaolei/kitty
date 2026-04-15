package search

import (
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/kovidgoyal/kitty"
	"github.com/kovidgoyal/kitty/tools/cli"
	"github.com/kovidgoyal/kitty/tools/config"
	"github.com/kovidgoyal/kitty/tools/tty"
	"github.com/kovidgoyal/kitty/tools/tui/loop"
	"github.com/kovidgoyal/kitty/tools/tui/sgr"
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
	matchCount       int
	currentMatchIdx  int
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
	h.lp.AllowLineWrapping(true)
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
		h.scrollStart = h.search.currentMatch.line
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
		h.scrollStart = h.search.currentMatch.line
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
		h.moveScrollStart(max(0, len(h.lines)-max(1, h.viewsHeight)) - h.scrollStart)
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
	h.scrollStart = clampedScrollStart(h.scrollStart, h.lines, maxRows, max(1, int(h.screenSize.WidthCells)))
	h.lp.MoveCursorTo(1, startY)

	rowsUsed := 0
	width := max(1, int(h.screenSize.WidthCells))
	for i := h.scrollStart; i < len(h.lines); i++ {
		line := h.renderedRawForLine(&h.lines[i])
		lineRows := visualRowsForLine(h.lines[i].plain, width)
		if rowsUsed+lineRows > maxRows {
			break
		}
		h.lp.QueueWriteString(line)
		rowsUsed += lineRows
		if rowsUsed >= maxRows {
			break
		}
		if i+1 < len(h.lines) {
			h.lp.QueueWriteString("\r\n")
		}
	}
}

func (h *Handler) moveScrollStart(delta int) {
	maxScrollStart := maxScrollStartForPage(h.lines, max(1, h.viewsHeight), max(1, int(h.screenSize.WidthCells)))
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

func visualRowsForLine(plain string, width int) int {
	if width <= 0 {
		return 1
	}
	return max(1, (wcswidth.Stringwidth(plain)+width-1)/width)
}

func lastPageStart(lines []DisplayLine, maxRows, width int) int {
	maxRows = max(1, maxRows)
	width = max(1, width)
	if len(lines) == 0 {
		return 0
	}
	rowsUsed := 0
	start := len(lines) - 1
	for i := len(lines) - 1; i >= 0; i-- {
		lineRows := visualRowsForLine(lines[i].plain, width)
		if rowsUsed+lineRows > maxRows {
			break
		}
		rowsUsed += lineRows
		start = i
	}
	return start
}

func maxScrollStartForPage(lines []DisplayLine, maxRows, width int) int {
	return lastPageStart(lines, maxRows, width)
}

func clampedScrollStart(scrollStart int, lines []DisplayLine, maxRows, width int) int {
	maxScrollStart := maxScrollStartForPage(lines, maxRows, width)
	if scrollStart >= len(lines) {
		return maxScrollStart
	}
	return min(max(0, scrollStart), maxScrollStart)
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
		inputData = string(stdin)
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
		scrollStart: len(lines),
	}
	lp.MouseTrackingMode(loop.NO_MOUSE_TRACKING)
	lp.OnInitialize = func() (string, error) {
		return handler.initialize()
	}
	lp.OnFinalize = func() string { return "" }
	lp.OnKeyEvent = handler.onKeyEvent
	lp.OnText = handler.onText
	lp.OnMouseEvent = handler.onMouseEvent
	lp.OnResize = func(oldSize, newSize loop.ScreenSize) error {
		handler.screenSize = newSize
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
