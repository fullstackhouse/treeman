package wt

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stubbedev/treeman/internal/store"
)

// TestOneSharedHandlePerInvocation pins the #79 acceptance criterion:
// a `wt` invocation's helpers — LookupWorktree, teardownInFlight,
// inlineTeardown, Create — all land on ONE process-wide SQLite handle.
// The delete/create paths switched from store.Open (private handle +
// close) to store.OpenShared, so repeated helper calls against the
// same DB must not grow the shared-cache count.
func TestOneSharedHandlePerInvocation(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "treeman.db")
	t.Setenv("TREEMAN_DB_PATH", dbPath)

	// Prime the store so helpers hit a readable DB (best-effort guards
	// answer false without failing when it's empty — that's fine here;
	// what matters is the handle count).
	if _, err := store.Open(ctx, dbPath); err != nil {
		t.Fatal(err)
	}
	before := store.SharedHandleCount()

	repo := t.TempDir()
	for range 3 {
		_, _ = LookupWorktree(ctx, repo, "whatever", nil)
		if teardownInFlight(ctx, repo, filepath.Join(repo, "wt-a")) {
			t.Errorf("teardownInFlight must answer false on a fresh store")
		}
	}

	if got := store.SharedHandleCount() - before; got != 1 {
		t.Errorf("invocation added %d shared handles, want exactly 1", got)
	}
}
