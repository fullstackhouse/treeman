package prepare

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/slug"
	"github.com/stubbedev/treeman/internal/store"
	"github.com/stubbedev/treeman/internal/wtlock"
)

// TestRunWaitsForConcurrentPrepare: a prepare of a worktree that another
// process is already preparing (the daemon, `finalize --local`, the MCP
// server) must queue behind it instead of rebuilding the same databases
// under its feet (#123).
func TestRunWaitsForConcurrentPrepare(t *testing.T) {
	ctx := context.Background()
	t.Setenv("TREEMAN_DB_PATH", filepath.Join(t.TempDir(), "treeman.db"))
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "tm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	wt := t.TempDir()
	repoID, err := st.EnsureRepo(ctx, wt, "repo")
	if err != nil {
		t.Fatal(err)
	}
	wtID, err := st.EnsureWorktree(ctx, repoID, wt, "wtslug", "develop")
	if err != nil {
		t.Fatal(err)
	}
	dbFile := filepath.Join(wt, "data", "ok_wtslug.db")
	if err := os.MkdirAll(filepath.Dir(dbFile), 0o755); err != nil {
		t.Fatal(err)
	}
	migrated := filepath.Join(wt, "migrated")
	cfg := &config.Config{Databases: []config.DatabaseConfig{
		{Engine: "sqlite", NameTemplate: "data/ok_{slug}.db", Migrate: &config.Step{Run: "touch " + dbFile + " " + migrated}},
	}}

	release, err := wtlock.Acquire(ctx, wtlock.Prepare, wt, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, cfg, wt, slug.Slug{Value: "wtslug", Source: slug.SourceTicket}, st, repoID, wtID, nil)
		done <- err
	}()
	select {
	case err := <-done:
		release()
		t.Fatalf("prepare ran while another prepare held the worktree: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	if _, serr := os.Stat(migrated); serr == nil {
		release()
		t.Fatal("migrate ran while another prepare held the worktree")
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("prepare after the lock was released: %v", err)
	}
	if _, serr := os.Stat(migrated); serr != nil {
		t.Fatalf("migrate did not run once the lock was free: %v", serr)
	}
}
