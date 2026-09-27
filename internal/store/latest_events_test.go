package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestLatestEventPerWorktree(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "treeman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	repoID, _ := st.EnsureRepo(ctx, "/tmp/repo", "repo")
	wtA, _ := st.EnsureWorktree(ctx, repoID, "/tmp/repo/a", "a", "a")
	wtB, _ := st.EnsureWorktree(ctx, repoID, "/tmp/repo/b", "b", "b")

	_ = st.WriteEvent(ctx, LevelInfo, EvtWorktreeCreateEnd, "old", repoID, wtA, "", 0, nil)
	_ = st.WriteEvent(ctx, LevelInfo, EvtWorktreeDeleteStart, "newest", repoID, wtA, "", 0, nil)
	// Unrelated event type on B must not surface as B's latest.
	_ = st.WriteEvent(ctx, LevelInfo, EvtDBTeardownProgress, "noise", repoID, wtB, "", 0, nil)

	latest, err := st.LatestEventPerWorktree(ctx, []int64{wtA, wtB}, []string{
		EvtWorktreeCreateStart, EvtWorktreeCreateEnd, EvtWorktreeDeleteStart, EvtWorktreeDeleteEnd,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := latest[wtA].EventType; got != EvtWorktreeDeleteStart {
		t.Errorf("wtA latest = %q, want %q (newest wins)", got, EvtWorktreeDeleteStart)
	}
	if _, ok := latest[wtB]; ok {
		t.Error("wtB has no event of the filtered types; must be absent from the map")
	}

	// Degenerate inputs are cheap no-ops, not errors.
	if m, err := st.LatestEventPerWorktree(ctx, nil, []string{EvtWorktreeCreateEnd}); err != nil || len(m) != 0 {
		t.Errorf("empty ids: %v %v", m, err)
	}
	if m, err := st.LatestEventPerWorktree(ctx, []int64{wtA}, nil); err != nil || len(m) != 0 {
		t.Errorf("empty types: %v %v", m, err)
	}
}

// TestLatestEventPerWorktreeBounded pins the GROUP BY MAX(id) bounding:
// with 10k matching events across 5 worktrees, exactly one row per
// worktree is returned — the max-id one — instead of walking the full
// ordered set and discarding all but the first per worktree.
func TestLatestEventPerWorktreeBounded(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "treeman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	repoID, _ := st.EnsureRepo(ctx, "/tmp/repo", "repo")
	wtIDs := make([]int64, 0, 5)
	for i := range 5 {
		id, werr := st.EnsureWorktree(ctx, repoID,
			fmt.Sprintf("/tmp/repo/w%d", i), fmt.Sprintf("w%d", i), "main")
		if werr != nil {
			t.Fatal(werr)
		}
		wtIDs = append(wtIDs, id)
	}

	// Interleave rounds so every worktree owns ~2k matching events and
	// its max id lands in the final round. Bulk-inserted in one tx —
	// WriteEvent's per-call overhead would dominate the test.
	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO events(ts, level, repo_id, worktree_id, event_type, message, payload_json)
		 VALUES (?, 'info', ?, ?, ?, ?, '{}')`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stmt.Close() }()
	now := time.Now().UnixMilli()
	for round := range 2000 {
		for _, id := range wtIDs {
			msg := "history"
			if round == 1999 {
				msg = "newest"
			}
			if _, err := stmt.ExecContext(ctx, now+int64(round), repoID, id, EvtWorktreeCreateEnd, msg); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	latest, err := st.LatestEventPerWorktree(ctx, wtIDs, []string{EvtWorktreeCreateEnd})
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != len(wtIDs) {
		t.Fatalf("got %d rows for %d worktrees, want exactly one each", len(latest), len(wtIDs))
	}
	for _, id := range wtIDs {
		e, ok := latest[id]
		if !ok {
			t.Fatalf("worktree %d missing from result", id)
		}
		if e.Message != "newest" {
			t.Errorf("worktree %d latest = %q, want the max-id event (newest)", id, e.Message)
		}
	}
}

// TestMigrateUserVersionShortCircuit pins the upgrade contract: the
// first open stamps PRAGMA user_version, and a second open of the same
// file skips the full migration probe (the stamp says it's current).
func TestMigrateUserVersionShortCircuit(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "treeman.db")

	st, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	var v int
	if err := st.DB.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	latest, err := latestMigrationVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v != latest {
		t.Fatalf("user_version = %d after first open, want %d", v, latest)
	}
	_ = st.Close()

	st2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer func() { _ = st2.Close() }()
	// A re-open must not have regressed the stamp (and took the
	// short-circuit path because current >= latest).
	if err := st2.DB.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != latest {
		t.Fatalf("user_version = %d after second open, want %d", v, latest)
	}
}

// TestOpenSharedReusesHandle pins the CLI-side handle contract: two
// calls on the same path yield the same *Store, different paths don't.
func TestOpenSharedReusesHandle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	a, err := OpenShared(ctx, filepath.Join(dir, "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	a2, err := OpenShared(ctx, filepath.Join(dir, "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	if a != a2 {
		t.Fatal("OpenShared on the same path must return the same handle")
	}
	b, err := OpenShared(ctx, filepath.Join(dir, "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	if b == a {
		t.Fatal("OpenShared on different paths must return different handles")
	}
}
