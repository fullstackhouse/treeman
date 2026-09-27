//go:build e2e

package cli_surface_e2e

import (
	"strings"
	"testing"
)

// TestTUIRequiresTTY pins the `treeman tui` non-TTY acceptance
// criterion: piping (no TTY on stdin) must exit promptly with a clear
// message instead of hanging, and the hint must name the plain
// commands that cover the same ground.
func TestTUIRequiresTTY(t *testing.T) {
	e := newEnv(t)

	res := e.run(t, e.home, "tui")
	if res.err == nil {
		t.Fatal("tui without a TTY should exit non-zero")
	}
	if !strings.Contains(res.stderr, "needs a terminal") {
		t.Errorf("stderr %q should explain the TTY requirement", res.stderr)
	}
	if !strings.Contains(res.stderr, "worktree list") {
		t.Errorf("stderr %q should point at the plain-command equivalents", res.stderr)
	}
}
