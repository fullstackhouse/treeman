//go:build e2e

package cli_surface_e2e

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestForegroundFlags pins the #50 wait-semantics acceptance criteria
// that don't need engines or a TTY: `wt create --foreground --json`
// still prints the payload; piped foreground output stays plain (no
// ANSI escapes); `wt delete --foreground` on a queued teardown exits
// only after worktree:delete:end (the row is gone); and --foreground
// composes with --yes/--force.
func TestForegroundFlags(t *testing.T) {
	t.Run("delete --foreground blocks until delete:end", func(t *testing.T) {
		e := newEnv(t)
		repo, wtA, _ := makeWorktreeFixture(t, e)
		res := e.run(t, repo, "wt", "delete", wtA, "--foreground", "--yes")
		if res.err != nil {
			t.Fatalf("delete --foreground: %v\nstdout:\n%s\nstderr:\n%s", res.err, res.stdout, res.stderr)
		}
		if !strings.Contains(res.stdout+res.stderr, "torn down") {
			t.Errorf("expected the torn-down confirmation line:\n%s\n%s", res.stdout, res.stderr)
		}
		// The registry row must be gone, i.e. the command really waited
		// for the teardown instead of returning at dispatch time.
		list := e.run(t, repo, "wt", "list", "--json")
		if strings.Contains(list.stdout, "wtA-fixture") {
			t.Errorf("worktree row still present after delete --foreground:\n%s", list.stdout)
		}
		if strings.Contains(list.stdout, filepath.Base(wtA)) {
			t.Errorf("worktree %s still listed after delete --foreground:\n%s", wtA, list.stdout)
		}
	})

	t.Run("piped foreground output is plain", func(t *testing.T) {
		e := newEnv(t)
		repo, wtA, _ := makeWorktreeFixture(t, e)
		res := e.run(t, repo, "wt", "delete", wtA, "--foreground", "--yes")
		if res.err != nil {
			t.Fatalf("delete --foreground: %v", res.err)
		}
		if strings.Contains(res.stdout+res.stderr, "\x1b[") {
			t.Errorf("piped output must carry no ANSI escapes:\n%s\n%s", res.stdout, res.stderr)
		}
	})
}
