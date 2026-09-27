package ui

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// capture redirects the package-level Out and Err writers for the
// duration of f.
func capture(t *testing.T, f func()) (string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	oldOut, oldErr := Out, Err
	Out = &out
	Err = &errb
	defer func() { Out, Err = oldOut, oldErr }()
	f()
	return out.String(), errb.String()
}

// TestRenderErrorOutput pins the #73 acceptance criteria: refusal
// errors name their remediation command as a distinct run: line,
// wrapped errors render cause and wrapper on separate lines, and known
// error classes get their curated hint even without a backticked
// command.
func TestRenderErrorOutput(t *testing.T) {
	t.Run("backticked command becomes a run hint", func(t *testing.T) {
		out, serr := capture(t, func() {
			RenderError(
				errors.New(`refusing to delete the worktree you're in — use ` + "`" + `cd "$(treeman worktree back --remove)"` + "`"),
			)
		})
		if !strings.Contains(serr, "✗") && !strings.Contains(serr, "refusing to delete the worktree you're in") {
			t.Errorf("error line missing:\n%s", serr)
		}
		if !strings.Contains(serr, `run: cd "$(treeman worktree back --remove)"`) {
			t.Errorf("run hint missing:\n%s", serr)
		}
		if out != "" {
			t.Errorf("errors belong on stderr, stdout got %q", out)
		}
	})

	t.Run("wrapped error renders each layer", func(t *testing.T) {
		cause := errors.New("connection refused")
		_, serr := capture(t, func() {
			RenderError(fmt.Errorf("finalize: %w", cause))
		})
		if !strings.Contains(serr, "finalize:") || !strings.Contains(serr, "↳ connection refused") {
			t.Errorf("cause chain not rendered:\n%s", serr)
		}
	})

	t.Run("joined errors each get a cause line", func(t *testing.T) {
		joined := errors.Join(errors.New("left failed"), errors.New("right failed"))
		_, serr := capture(t, func() {
			RenderError(fmt.Errorf("teardown: %w", joined))
		})
		for _, want := range []string{"↳ left failed", "↳ right failed"} {
			if !strings.Contains(serr, want) {
				t.Errorf("missing %q:\n%s", want, serr)
			}
		}
	})

	t.Run("known class gets curated hint", func(t *testing.T) {
		_, serr := capture(t, func() {
			RenderError(fmt.Errorf("run prepare: %w", errors.New("daemon unreachable")))
		})
		if !strings.Contains(serr, "run: treeman daemon start") {
			t.Errorf("known-class hint missing:\n%s", serr)
		}
	})

	t.Run("nil is a no-op", func(t *testing.T) {
		out, serr := capture(t, func() { RenderError(nil) })
		if out != "" || serr != "" {
			t.Errorf("nil should print nothing, got %q / %q", out, serr)
		}
	})
}
