package ui

import (
	"bytes"
	"strings"
	"testing"
)

// TestTableSetWidthTruncatesLastColumn pins the width-fitting
// contract: in a narrow terminal the LAST column (PATH/COMMAND) is
// truncated with an ellipsis, every physical line stays within the
// budget, and untruncated columns keep natural widths.
func TestTableSetWidthTruncatesLastColumn(t *testing.T) {
	tbl := NewTable("ID", "PATH")
	tbl.Row("1", "/very/long/path/to/a/worktree/that/overflows/any/sane/terminal/width")
	tbl.Row("2", "/short")

	var buf bytes.Buffer
	tbl.SetWidth(30).Render(&buf)

	for line := range strings.SplitSeq(strings.TrimSuffix(buf.String(), "\n"), "\n") {
		if got := Width(line); got > 30 {
			t.Errorf("rendered line is %d cols (budget 30): %q", got, line)
		}
	}
	if !strings.Contains(buf.String(), "…") {
		t.Errorf("overflowing cell was not ellipsized:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "/short") {
		t.Errorf("short row lost:\n%s", buf.String())
	}

	// No SetWidth: natural widths, no truncation.
	var natural bytes.Buffer
	nat := NewTable("ID", "PATH")
	nat.Row("1", strings.Repeat("x", 60))
	nat.Render(&natural)
	if !strings.Contains(natural.String(), strings.Repeat("x", 60)) {
		t.Errorf("width-less render should not truncate:\n%s", natural.String())
	}
}

// TestTruncateANSIAware pins Truncate: short strings pass through,
// plain strings are cut at w with an ellipsis, styled strings keep
// their escape codes balanced, and the result never exceeds w cols.
func TestTruncateANSIAware(t *testing.T) {
	if got := Truncate("short", 10); got != "short" {
		t.Errorf("short string rewritten: %q", got)
	}
	got := Truncate("0123456789abcdef", 10)
	if Width(got) > 10 || !strings.HasSuffix(got, "…") {
		t.Errorf("plain cut wrong: %q (width %d)", got, Width(got))
	}
	// Construct the styled input directly: Red() degrades to plain
	// text when color is off (as it is under go test), and the point
	// here is an ANSI-carrying input.
	styled := "\x1b[31m0123456789abcdef\x1b[0m"
	cut := Truncate(styled, 10)
	if Width(cut) > 10 {
		t.Errorf("styled cut exceeds budget: %q (width %d)", cut, Width(cut))
	}
	if strings.Count(cut, "\x1b[0m") == 0 {
		t.Errorf("styled cut dropped the reset sequence: %q", cut)
	}
}

// TestTermWidthCOLUMNSOverride pins the $COLUMNS override: a sane
// value wins over the ioctl (this test's stdout is a pipe, so the
// fallback path would otherwise always report 80).
func TestTermWidthCOLUMNSOverride(t *testing.T) {
	t.Setenv("COLUMNS", "123")
	if got := TermWidth(); got != 123 {
		t.Errorf("TermWidth with COLUMNS=123 = %d, want 123", got)
	}
}

// TestTableAlignmentWithStyledCells pins the ANSI-aware width math:
// colored cells must not drift column alignment, padding is computed
// against the stripped width, and every body row lines up under its
// header.
func TestTableAlignmentWithStyledCells(t *testing.T) {
	tbl := NewTable("ENGINE", "TEMPLATE")
	tbl.Row(Cyan("mysql"), "_tm_0123456789abcdef")
	tbl.Row(Green("pg"), "x")

	var buf bytes.Buffer
	tbl.Render(&buf)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 4 { // header, separator, 2 body rows
		t.Fatalf("got %d lines, want 4:\n%s", len(lines), buf.String())
	}
	// Strip ANSI and check the second column starts at the same offset
	// in the header and both body rows.
	idx := func(s, sub string) int { return strings.Index(stripANSI(s), sub) }
	col2 := idx(lines[0], "TEMPLATE")
	if col2 <= 0 {
		t.Fatalf("TEMPLATE header not found: %q", lines[0])
	}
	if got := idx(lines[2], "_tm_"); got != col2 {
		t.Errorf("row 1 second column at %d, want %d (styled first cell broke alignment)", got, col2)
	}
	if got := idx(lines[3], "x"); got != col2 {
		t.Errorf("row 2 second column at %d, want %d", got, col2)
	}
}

func TestTableEmptyRendersNothing(t *testing.T) {
	var buf bytes.Buffer
	(&Table{}).Render(&buf)
	if buf.Len() != 0 {
		t.Errorf("empty table rendered %q", buf.String())
	}
}

// stripANSI removes escape sequences using the same Width machinery
// the renderer pads with — if Width and this disagree, alignment
// assertions above will catch it.
func stripANSI(s string) string {
	var out strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case inEsc:
			if r == 'm' {
				inEsc = false
			}
		case r == '\x1b':
			inEsc = true
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}
