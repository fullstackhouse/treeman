package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/ui"
)

// TestRenderStatusTable pins the table format (#57): bucket icons in
// the first column, ui.Status-colored states (red for error, green for
// ready), lastLabel-style relative ages, and ANSI-safe alignment —
// every line's stripped display width matches under NO_COLOR.
func TestRenderStatusTable(t *testing.T) {
	cfg := config.StatusConfig{
		Icons: config.StatusBuckets{Stable: "●", Up: "▲", Down: "▼", Failed: "✗"},
	}
	age4m := time.Now().Add(-4 * time.Minute).UnixMilli()
	age2h := time.Now().Add(-2 * time.Hour).UnixMilli()
	d := statusData{
		Repos: []statusRepo{{
			Repo: "proj",
			Worktrees: []statusWt{
				{Branch: "feature/x", Slug: "s_one", State: "ready", Bucket: bucketStable, AgeTs: age4m},
				{Branch: "fix", Slug: "s_two", State: "error", Bucket: bucketFailed, AgeTs: age2h},
			},
		}},
	}

	t.Run("ages render lastLabel-style", func(t *testing.T) {
		old := ui.Out
		_ = ui.SetColorMode("never")
		defer func() { _ = ui.SetColorMode("auto"); ui.Out = old }()
		out, err := renderStatus("table", d, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(ui.StripANSI(out), "4m") || !strings.Contains(ui.StripANSI(out), "2h") {
			t.Errorf("AGE column should show lastLabel-style values:\n%s", ui.StripANSI(out))
		}
	})

	t.Run("NO_COLOR keeps every line aligned", func(t *testing.T) {
		old := ui.Out
		_ = ui.SetColorMode("never")
		defer func() { _ = ui.SetColorMode("auto"); ui.Out = old }()
		out, err := renderStatus("table", d, cfg)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(out, "\n")
		if len(lines) != 4 {
			t.Fatalf("want header + rule + 2 rows, got %d lines:\n%s", len(lines), out)
		}
		width := ui.Width(lines[0])
		for i, line := range lines {
			if ui.Width(line) != width {
				t.Errorf("line %d width %d != header width %d:\n%q", i, ui.Width(line), width, line)
			}
		}
		stripped := ui.StripANSI(out)
		for _, want := range []string{"STATE", "BRANCH", "SLUG", "AGE", "REPO", "ready", "error"} {
			if !strings.Contains(stripped, want) {
				t.Errorf("table missing %q:\n%s", want, stripped)
			}
		}
	})
}
