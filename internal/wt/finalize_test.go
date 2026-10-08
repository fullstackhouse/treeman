package wt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/slug"
	"github.com/stubbedev/treeman/internal/store"
	"github.com/stubbedev/treeman/internal/wtlock"
)

type recordSink struct {
	mu    sync.Mutex
	infos []string
}

func (s *recordSink) OK(string, ...any)   {}
func (s *recordSink) Warn(string, ...any) {}
func (s *recordSink) Info(f string, a ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.infos = append(s.infos, fmt.Sprintf(f, a...))
}

func (s *recordSink) saw(sub string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.infos {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

type finalizeFixture struct {
	st     *store.Store
	wt     string
	repoID int64
	wtID   int64
}

func newFinalizeFixture(t *testing.T) finalizeFixture {
	t.Helper()
	ctx := context.Background()
	t.Setenv("TREEMAN_DB_PATH", filepath.Join(t.TempDir(), "treeman.db"))
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "tm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	wt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wt, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	repoID, err := st.EnsureRepo(ctx, wt, "repo")
	if err != nil {
		t.Fatal(err)
	}
	wtID, err := st.EnsureWorktree(ctx, repoID, wt, "wtslug", "develop")
	if err != nil {
		t.Fatal(err)
	}
	return finalizeFixture{st: st, wt: wt, repoID: repoID, wtID: wtID}
}

func (f finalizeFixture) run(ctx context.Context, cfg *config.Config, sink Sink) error {
	return RunLocalFinalize(ctx, cfg, f.wt, f.wt, slug.Slug{Value: "wtslug", Source: slug.SourceTicket},
		true, f.st, f.repoID, f.wtID, nil, false, sink)
}

// TestRunLocalFinalizeFailsOnPrepareError: a database that fails to
// prepare must fail `finalize --local` (non-zero exit) and skip the
// create-after-engines hooks, instead of reporting success over a stale
// database (#123).
func TestRunLocalFinalizeFailsOnPrepareError(t *testing.T) {
	f := newFinalizeFixture(t)
	after := filepath.Join(f.wt, "after.ran")
	cfg := &config.Config{
		Databases: []config.DatabaseConfig{
			{Engine: "sqlite", NameTemplate: "data/fail_{slug}.db", Migrate: &config.Step{Run: "exit 1"}},
		},
		Hooks: config.HooksConfig{OnCreateAfterEngines: []config.Action{{Run: []string{"touch " + after}}}},
	}
	err := f.run(context.Background(), cfg, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "prepare: ") {
		t.Fatalf("err = %v, want the prepare failure", err)
	}
	if _, serr := os.Stat(after); serr == nil {
		t.Fatal("create-after-engines ran after a failed prepare")
	}
}

// TestRunLocalFinalizeWaitsForRunningFinalize: `finalize --local` issued
// while another process (the daemon's create tail) is finalizing the
// same worktree must queue behind it, not rebuild the databases
// alongside it (#123).
func TestRunLocalFinalizeWaitsForRunningFinalize(t *testing.T) {
	f := newFinalizeFixture(t)
	migrated := filepath.Join(f.wt, "migrated")
	dbFile := filepath.Join(f.wt, "data", "ok_wtslug.db")
	cfg := &config.Config{Databases: []config.DatabaseConfig{
		{Engine: "sqlite", NameTemplate: "data/ok_{slug}.db", Migrate: &config.Step{Run: "touch " + dbFile + " " + migrated}},
	}}

	release, err := wtlock.Acquire(context.Background(), wtlock.Finalize, f.wt, nil)
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordSink{}
	done := make(chan error, 1)
	go func() { done <- f.run(context.Background(), cfg, sink) }()

	deadline := time.Now().Add(5 * time.Second)
	for !sink.saw("still running; waiting") {
		if time.Now().After(deadline) {
			release()
			t.Fatal("finalize did not report waiting on the running finalize")
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case err := <-done:
		release()
		t.Fatalf("finalize finished while another finalize held the worktree: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if _, serr := os.Stat(migrated); serr == nil {
		t.Fatal("prepare ran while another finalize held the worktree")
	}

	release()
	if err := <-done; err != nil {
		t.Fatalf("finalize after the other run finished: %v", err)
	}
	if _, serr := os.Stat(migrated); serr != nil {
		t.Fatalf("prepare did not run once the lock was free: %v", serr)
	}
}
