package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLifecycleOnCreateDefersWhileCreateInFlight asserts the watcher
// doesn't register + finalize a worktree whose daemon worktree_create
// is still between `git worktree add` and registering its row. Doing
// so ran prepare with an empty env instead of the CLI's (#122).
func TestLifecycleOnCreateDefersWhileCreateInFlight(t *testing.T) {
	st := newTestState(t)
	ctx := t.Context()
	repoPath := t.TempDir()
	wtPath := filepath.Join(repoPath, ".worktrees", "feat")
	adminDir := filepath.Join(repoPath, ".git", "worktrees", "feat")
	for _, d := range []string{wtPath, adminDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(adminDir, "gitdir"), []byte(wtPath+"/.git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repoID, err := st.Store.EnsureRepo(ctx, repoPath, "repo")
	if err != nil {
		t.Fatal(err)
	}
	lw := &LifecycleWatcher{
		repoID:        repoID,
		repoPath:      repoPath,
		st:            st,
		seen:          map[string]string{},
		debounce:      time.Hour,
		pendingCreate: map[string]*time.Timer{},
	}
	defer lw.cancelPendingCreate(adminDir)

	unmark := st.MarkCreateInFlight(repoPath + "/")
	lw.onCreate(ctx, adminDir)
	unmark()

	if row, err := st.Store.LookupActiveWorktreeByPath(ctx, wtPath); err == nil && row.ID != 0 {
		t.Fatalf("watcher registered worktree #%d while a create was in flight", row.ID)
	}
	lw.pendingMu.Lock()
	_, rescheduled := lw.pendingCreate[adminDir]
	lw.pendingMu.Unlock()
	if !rescheduled {
		t.Fatal("deferred CREATE was not rescheduled")
	}
	if st.IsCreateInFlight(repoPath) {
		t.Fatal("create marker not cleared by unmark")
	}
}
