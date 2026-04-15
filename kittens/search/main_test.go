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

func TestWrapDisplayLinePreservesStylesAndHyperlinks(t *testing.T) {
	raw := "\x1b[31m\x1b]8;id=1;https://example.com\x1b\\abcd\x1b]8;;\x1b\\\x1b[m"
	rows := wrapDisplayLine(raw, 2)
	if len(rows) != 2 {
		t.Fatalf("expected 2 wrapped rows, got %d", len(rows))
	}

	if got := rows[0]; got != "\x1b[31m\x1b]8;id=1;https://example.com\x1b\\ab\x1b[m\x1b]8;;\x1b\\" {
		t.Fatalf("unexpected first row: %#v", got)
	}
	if got := rows[1]; got != "\x1b[31m\x1b]8;id=1;https://example.com\x1b\\cd\x1b]8;;\x1b\\\x1b[m" {
		t.Fatalf("unexpected second row: %#v", got)
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
