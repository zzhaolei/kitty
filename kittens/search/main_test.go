package search

import (
	"strings"
	"testing"
)

func TestParseDisplayLinesKeepsOnlySGRAndOSC8(t *testing.T) {
	input := "\x1b[31mred\x1b[m \x1b]133;C\x1b\\\x1b]8;id=1;https://example.com\x1b\\link\x1b]8;;\x1b\\"
	lines := parseDisplayLines(input)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}

	if got := lines[0].plain; got != "red link" {
		t.Fatalf("unexpected plain text: %#v", got)
	}
	if strings.Contains(lines[0].raw, "]133;") {
		t.Fatalf("unexpected OSC 133 residue: %#v", lines[0].raw)
	}
	if !strings.Contains(lines[0].raw, "\x1b[31mred\x1b[m") {
		t.Fatalf("expected to keep SGR formatting: %#v", lines[0].raw)
	}
	if !strings.Contains(lines[0].raw, "\x1b]8;id=1;https://example.com\x1b\\link\x1b]8;;\x1b\\") {
		t.Fatalf("expected to keep OSC8 hyperlink: %#v", lines[0].raw)
	}
}

func TestReSearchUsesVisibleTextOffsets(t *testing.T) {
	h := Handler{
		lines: []DisplayLine{{
			raw:   "\x1b[31mhello\x1b[m",
			plain: "hello",
		}},
		search: Search{text: "ell"},
	}

	h.reSearch()

	if h.matchCount != 1 {
		t.Fatalf("expected 1 match, got %d", h.matchCount)
	}
	match := h.lines[0].matchs[0]
	if match.start != 1 || match.end != 4 {
		t.Fatalf("unexpected match offsets: %+v", match)
	}
}

func TestVisualRowsForLineUsesLogicalLineWidth(t *testing.T) {
	if got := visualRowsForLine("abcd", 2); got != 2 {
		t.Fatalf("expected 2 visual rows, got %d", got)
	}
	if got := visualRowsForLine("\x1b[31mab\x1b[m你", 2); got != 2 {
		t.Fatalf("expected ansi text width to ignore escapes, got %d", got)
	}
}

func TestRenderedRawForLineOnlyHighlightsMatchedCharacters(t *testing.T) {
	h := Handler{}
	line := &DisplayLine{
		raw:   "ab你#12cd",
		plain: "ab你#12cd",
		matchs: []*Match{
			{start: len("ab你"), end: len("ab你#12")},
		},
	}

	rendered := h.renderedRawForLine(line)
	if !strings.Contains(rendered, "#12") {
		t.Fatalf("expected rendered text to keep matched text: %#v", rendered)
	}
	if !strings.HasSuffix(rendered, "cd") {
		t.Fatalf("expected trailing plain text to remain after highlight: %#v", rendered)
	}
	if strings.Contains(rendered, "#12cd\x1b[39;49m") {
		t.Fatalf("highlight leaked past the matched text: %#v", rendered)
	}
	if !strings.Contains(rendered, "#12\x1b[39;49mcd") {
		t.Fatalf("expected highlight to close before trailing text: %#v", rendered)
	}
}

func TestClampedScrollStartUsesLastPageStart(t *testing.T) {
	lines := []DisplayLine{
		{plain: "1", raw: "1"},
		{plain: "2", raw: "2"},
		{plain: "3", raw: "3"},
		{plain: "4", raw: "4"},
		{plain: "5", raw: "5"},
	}
	if got := clampedScrollStart(5, lines, 3, 10); got != 2 {
		t.Fatalf("expected initial scrollStart to land on last page start, got %d", got)
	}
	if got := clampedScrollStart(4, lines, 3, 10); got != 2 {
		t.Fatalf("expected end-of-buffer scrollStart to clamp to last page start, got %d", got)
	}
}

func TestLastPageStartAccountsForWrappedLineHeight(t *testing.T) {
	lines := []DisplayLine{
		{plain: "1234", raw: "1234"},
		{plain: "12345678", raw: "12345678"},
		{plain: "tail", raw: "tail"},
	}
	if got := lastPageStart(lines, 3, 4); got != 1 {
		t.Fatalf("expected wrapped last page to start at line 1, got %d", got)
	}
}
