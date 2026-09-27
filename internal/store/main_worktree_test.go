package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureMainWorktreeInsertsAndResurrects covers the two paths the
// daemon's enroll flow uses:
//  1. First enable → fresh INSERT with is_main=1.
//  2. Disable (soft delete) → re-enable → resurrect same row with
//     is_main=1 restored.
func TestEnsureMainWorktreeInsertsAndResurrects(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	repoID, _ := s.EnsureRepo(ctx, "/repos/x", "x")
	id1, err := s.EnsureMainWorktree(ctx, repoID, "/repos/x", "main_develop", "develop")
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.LookupMainWorktree(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if row.ID != id1 || !row.IsMain {
		t.Fatalf("first ensure: row=%+v", row)
	}

	if err := s.MarkWorktreeDeleted(ctx, id1); err != nil {
		t.Fatal(err)
	}
	dead, _ := s.LookupMainWorktree(ctx, repoID)
	if dead.ID != 0 {
		t.Errorf("soft-deleted row should not appear active: %+v", dead)
	}

	id2, err := s.EnsureMainWorktree(ctx, repoID, "/repos/x", "main_feature", "feature")
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id1 {
		t.Errorf("resurrect should reuse row id: got %d want %d", id2, id1)
	}
	row, _ = s.LookupMainWorktree(ctx, repoID)
	if !row.IsMain || row.Slug != "main_feature" || row.Branch != "feature" {
		t.Errorf("resurrected row not restored correctly: %+v", row)
	}
}

// TestEnsureWorktreeIdempotentByPath confirms a repeat create at the
// same path converges to one row instead of erroring on the UNIQUE
// path constraint — the guarantee the create-race recovery relies on
// (daemon + in-process fallback both registering the same path).
func TestEnsureWorktreeIdempotentByPath(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	repoID, _ := s.EnsureRepo(ctx, "/repos/x", "x")
	id1, err := s.EnsureWorktree(ctx, repoID, "/repos/x/wt/a", "wt_a", "a")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.EnsureWorktree(ctx, repoID, "/repos/x/wt/a", "wt_a", "a")
	if err != nil {
		t.Fatalf("repeat create must not error: %v", err)
	}
	if id2 != id1 {
		t.Errorf("repeat create should reuse row id: got %d want %d", id2, id1)
	}
}

// TestEnsureWorktreeSkipsNoopUpdate pins the read-only refresh fast
// path: an identical re-ensure performs no UPDATE (counted via a
// temp trigger), while a branch change or a resurrect of a
// soft-deleted row still writes — ResolveIdentity funnels every
// worktree-scoped operation through here, so the no-op case is the
// hot one.
func TestEnsureWorktreeSkipsNoopUpdate(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	updCount := func() int {
		t.Helper()
		var n int
		if err := s.DB.QueryRowContext(ctx, "SELECT n FROM upd_log").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	repoID, _ := s.EnsureRepo(ctx, "/repos/x", "x")
	id, err := s.EnsureWorktree(ctx, repoID, "/repos/x/wt/a", "wt_a", "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `CREATE TABLE upd_log(n INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO upd_log VALUES (0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx,
		`CREATE TRIGGER wt_upd AFTER UPDATE ON worktrees BEGIN UPDATE upd_log SET n = n + 1; END`); err != nil {
		t.Fatal(err)
	}

	// Identical ensure: SELECT only, no UPDATE fired.
	if _, err := s.EnsureWorktree(ctx, repoID, "/repos/x/wt/a", "wt_a", "a"); err != nil {
		t.Fatal(err)
	}
	if n := updCount(); n != 0 {
		t.Errorf("identical re-ensure fired %d UPDATE(s), want 0", n)
	}

	// Branch change must still write.
	if _, err := s.EnsureWorktree(ctx, repoID, "/repos/x/wt/a", "wt_a", "b"); err != nil {
		t.Fatal(err)
	}
	if n := updCount(); n != 1 {
		t.Errorf("branch-change re-ensure fired %d UPDATE(s), want 1", n)
	}

	// Resurrecting a soft-deleted row must clear deleted_at.
	if err := s.MarkWorktreeDeleted(ctx, id); err != nil {
		t.Fatal(err)
	}
	if n := updCount(); n != 2 {
		t.Errorf("MarkWorktreeDeleted fired %d tracked UPDATE(s), want 2", n)
	}
	if _, err := s.EnsureWorktree(ctx, repoID, "/repos/x/wt/a", "wt_a", "b"); err != nil {
		t.Fatal(err)
	}
	if n := updCount(); n != 3 {
		t.Errorf("resurrect re-ensure fired %d UPDATE(s), want 3", n)
	}
	row, err := s.LookupActiveWorktreeByPath(ctx, "/repos/x/wt/a")
	if err != nil {
		t.Fatalf("resurrected row not active: %v", err)
	}
	if row.ID != id || row.Branch != "b" {
		t.Errorf("resurrected row = id %d branch %q, want id %d branch b", row.ID, row.Branch, id)
	}
}

// TestEnsureMainWorktreeUniquePerRepo confirms the partial unique
// index refuses a second active main row for the same repo. Inserting
// a *separate* path with is_main=1 should fail — the schema treats
// "one main per repo" as an invariant.
func TestEnsureMainWorktreeUniquePerRepo(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	repoID, _ := s.EnsureRepo(ctx, "/repos/x", "x")
	if _, err := s.EnsureMainWorktree(ctx, repoID, "/repos/x", "main_a", "a"); err != nil {
		t.Fatal(err)
	}
	// Direct INSERT bypasses the EnsureMainWorktree path-based dedup
	// and exercises the partial unique index directly.
	_, err = s.DB.ExecContext(ctx,
		"INSERT INTO worktrees(repo_id, path, slug, branch, created_at, is_main) VALUES (?, ?, ?, ?, ?, 1)",
		repoID, "/repos/x/other", "main_b", "b", 1)
	if err == nil {
		t.Fatalf("expected unique index violation on second active is_main row")
	}
	if !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("want UNIQUE constraint error, got: %v", err)
	}
}
