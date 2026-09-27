//go:build e2e

package cli_surface_e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDryRunFlagsPreviewWithoutEngineIO pins the #60 acceptance
// criteria: `wt delete --dry-run` resolves the target and prints the
// per-engine drop plan while touching nothing — against a config whose
// engines point at a CLOSED port, so any engine connection would fail
// loudly (and a real teardown would hang/error) — and the worktree
// survives. `db reset --dry-run` on the same repo reports the no-op
// honestly.
func TestDryRunFlagsPreviewWithoutEngineIO(t *testing.T) {
	e := newEnv(t)
	repo, wtA, _ := makeWorktreeFixture(t, e)
	// A second config whose mysql points at a closed port: the dry run
	// must not dial it. The fixture's row supplies the registry slug.
	writeConfig(t, repo, `worktrees:
  root: .worktrees
connections:
  mysql:
    host: 127.0.0.1
    port: 33990
    user: nobody
databases:
  - engine: mysql
    name_template: app_{slug}
    test_clones:
      clones: 2
      name_template: app_{slug}_test_{n}
`)

	res := e.run(t, repo, "wt", "delete", filepath.Base(wtA), "--dry-run", "--yes")
	if res.err != nil {
		t.Fatalf("delete --dry-run: %v\nstdout:\n%s\nstderr:\n%s", res.err, res.stdout, res.stderr)
	}
	combined := res.stdout + res.stderr
	if !strings.Contains(combined, "dry run") || !strings.Contains(combined, "app_") {
		t.Errorf("dry run should print the drop plan:\n%s", combined)
	}
	// The worktree must still exist, untouched.
	if _, err := os.Stat(wtA); err != nil {
		t.Fatalf("worktree %s vanished under --dry-run: %v", wtA, err)
	}
	// And nothing was torn down: the row is still live in wt list.
	list := e.run(t, repo, "wt", "list", "--json")
	if !strings.Contains(list.stdout, filepath.Base(wtA)) {
		t.Errorf("worktree row missing after --dry-run:\n%s", list.stdout)
	}

	// db reset --dry-run on a repo without branch_scoped DBs says so.
	reset := e.run(t, repo, "db", "reset", "--dry-run")
	if reset.err != nil {
		t.Fatalf("db reset --dry-run: %v\nstderr:\n%s", reset.err, reset.stderr)
	}
	if !strings.Contains(reset.stdout+reset.stderr, "no branch_scoped databases") {
		t.Errorf("reset dry run should report the no-op honestly:\n%s", reset.stdout+reset.stderr)
	}
}
