package cmd

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stubbedev/treeman/internal/store"
)

// TestCollectStatusRepoFilter pins the #56 --repo criterion: the
// summary aggregates only the requested repo's worktrees, while the
// unfiltered shape still covers every repo.
func TestCollectStatusRepoFilter(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "treeman.db")
	t.Setenv("TREEMAN_DB_PATH", dbPath)

	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	repoAPath := t.TempDir()
	repoBPath := t.TempDir()
	repoA, err := st.EnsureRepo(ctx, repoAPath, "app-a")
	if err != nil {
		t.Fatal(err)
	}
	repoB, err := st.EnsureRepo(ctx, repoBPath, "app-b")
	if err != nil {
		t.Fatal(err)
	}
	pathA := filepath.Join(t.TempDir(), "wt-a1")
	pathB := filepath.Join(t.TempDir(), "wt-b1")
	if _, err := st.EnsureWorktree(ctx, repoA, pathA, "a1", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureWorktree(ctx, repoB, pathB, "b1", "main"); err != nil {
		t.Fatal(err)
	}

	all, err := collectStatus(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 2 {
		t.Errorf("unfiltered total = %d, want 2", all.Total)
	}

	onlyA, err := collectStatus(ctx, repoAPath)
	if err != nil {
		t.Fatal(err)
	}
	if onlyA.Total != 1 || len(onlyA.Repos) != 1 || onlyA.Repos[0].Repo != filepath.Base(repoAPath) {
		t.Errorf("--repo filter should scope to one repo with 1 wt, got total=%d repos=%+v", onlyA.Total, onlyA.Repos)
	}

	none, err := collectStatus(ctx, "/nonexistent/repo")
	if err != nil {
		t.Fatal(err)
	}
	if none.Total != 0 {
		t.Errorf("unknown repo should summarize to 0, got %d", none.Total)
	}
}
