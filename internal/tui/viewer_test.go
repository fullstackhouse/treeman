package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/viewport"
)

func TestFindLineFrom(t *testing.T) {
	lines := []string{
		"CREATE TABLE products (id int);",
		"INSERT INTO products VALUES (1);",
		"-- migration 002",
		"CREATE INDEX idx_products ON products(id);",
		"INSERT INTO products VALUES (2);",
	}

	forward := findLineFrom(lines, "INSERT", 0, len(lines), 1)
	if forward != 1 {
		t.Errorf("forward scan from 0 = %d, want 1", forward)
	}
	if got := findLineFrom(lines, "INSERT", forward+1, len(lines), 1); got != 4 {
		t.Errorf("next match after %d = %d, want 4 (n repeats)", forward, got)
	}
	back := findLineFrom(lines, "INSERT", len(lines)-1, -1, -1)
	if back != 4 {
		t.Errorf("backward scan from end = %d, want 4", back)
	}
	if got := findLineFrom(lines, "INSERT", back-1, -1, -1); got != 1 {
		t.Errorf("prev match before %d = %d, want 1 (N repeats)", back, got)
	}
	if got := findLineFrom(lines, "nope", 0, len(lines), 1); got != -1 {
		t.Errorf("missing query = %d, want -1", got)
	}
	// Out-of-range starts are tolerated (viewport offsets can sit past
	// the last line after resizes).
	if got := findLineFrom(lines, "CREATE", len(lines), len(lines), -1); got != -1 {
		t.Errorf("empty scan range = %d, want -1", got)
	}
}

func TestViewerJumpWraps(t *testing.T) {
	vp := viewport.New(40, 3)
	lines := []string{"alpha", "beta", "gamma", "alpha"}
	vp.SetContent(strings.Join(lines, "\n"))
	m := &viewerModel{
		vp:    vp,
		keys:  newViewerKeys(),
		title: "t",
		lines: lines,
		query: "alpha",
	}
	// Forward from the top lands on line 0…
	m.jumpToQuery(0, 1)
	if m.vp.YOffset != 0 {
		t.Fatalf("first forward jump = %d, want 0", m.vp.YOffset)
	}
	// …then wraps: the later alpha is line 3, clamped to the viewport's
	// max offset (4 lines - 3 height = 1), which still shows it.
	m.jumpToQuery(m.vp.YOffset+1, 1)
	if m.vp.YOffset != 1 {
		t.Errorf("wrapped forward jump = %d, want 1 (clamped max offset)", m.vp.YOffset)
	}
	// Backward from just above the wrapped match finds the FIRST alpha
	// (line 0) — the previous match scanning up.
	m.jumpToQuery(m.vp.YOffset-1, -1)
	if m.vp.YOffset != 0 {
		t.Errorf("backward jump = %d, want 0 (the first alpha)", m.vp.YOffset)
	}
}
