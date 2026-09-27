package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadLayeredForWorktreeHierarchicalFragments pins the #66
// acceptance criteria: a `.treeman.yaml` fragment at
// `services/foo/` applies only to worktrees under that path — a
// worktree elsewhere never sees it, the main root never does, and
// deeper fragments override shallower ones.
func TestLoadLayeredForWorktreeHierarchicalFragments(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".treeman.yaml"), []byte(
		"worktrees:\n    root: .worktrees\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "services", "foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "services", "foo", ".treeman.yaml"), []byte(
		"databases:\n    - engine: postgres\n      name_template: foo_svc_{slug}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A worktree "under that path" means physically nested below the
	// fragment dir (worktrees.root pointed inside it, or a nested
	// checkout). A sibling (services/foo-wt) or a repo-root worktree
	// must NOT see the fragment.
	wtUnder := filepath.Join(root, "services", "foo", "pool", "w1")
	if err := os.MkdirAll(wtUnder, 0o755); err != nil {
		t.Fatal(err)
	}
	wtElsewhere := filepath.Join(root, "pool", "w2")
	if err := os.MkdirAll(wtElsewhere, 0o755); err != nil {
		t.Fatal(err)
	}

	cfgUnder, err := LoadLayeredForWorktree(root, wtUnder)
	if err != nil {
		t.Fatalf("worktree under services/foo: %v", err)
	}
	if len(cfgUnder.Databases) != 1 || cfgUnder.Databases[0].NameTemplate != "foo_svc_{slug}" {
		t.Errorf("fragment must apply under services/foo, got %+v", cfgUnder.Databases)
	}

	cfgElsewhere, err := LoadLayeredForWorktree(root, wtElsewhere)
	if err != nil {
		t.Fatalf("worktree outside services/foo: %v", err)
	}
	if len(cfgElsewhere.Databases) != 0 {
		t.Errorf("fragment must NOT apply outside services/foo, got %+v", cfgElsewhere.Databases)
	}

	cfgRoot, err := LoadLayered(root)
	if err != nil {
		t.Fatalf("repo-root load: %v", err)
	}
	if len(cfgRoot.Databases) != 0 {
		t.Errorf("repo-root load must ignore fragments, got %+v", cfgRoot.Databases)
	}

	// Deeper fragment overrides the shallower one's overlapping key:
	// the worktree must live under services/foo/api for that
	// fragment's scope.
	if err := os.MkdirAll(filepath.Join(root, "services", "foo", "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "services", "foo", "api", ".treeman.yaml"), []byte(
		"databases:\n    - engine: postgres\n      name_template: api_svc_{slug}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgDeep, err := LoadLayeredForWorktree(root, filepath.Join(root, "services", "foo", "api", "pool", "w3"))
	if err != nil {
		t.Fatalf("deep worktree: %v", err)
	}
	if len(cfgDeep.Databases) != 1 || cfgDeep.Databases[0].NameTemplate != "api_svc_{slug}" {
		t.Errorf("deeper fragment should override: %+v", cfgDeep.Databases)
	}
}

// TestFragmentPaths pins the fragment-walk order: shallow → deep,
// only for directories the worktree actually sits under.
func TestFragmentPaths(t *testing.T) {
	got := fragmentPaths("/repo", filepath.Join("/repo", "services", "foo", "wt"))
	want := []string{
		filepath.Join("/repo", "services", ".treeman.yaml"),
		filepath.Join("/repo", "services", "foo", ".treeman.yaml"),
		filepath.Join("/repo", "services", "foo", "wt", ".treeman.yaml"),
	}
	if len(got) != len(want) {
		t.Fatalf("fragmentPaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("fragmentPaths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if fragmentPaths("/repo", "/elsewhere/wt") != nil {
		t.Errorf("worktree outside repo must yield no fragments")
	}
	if fragmentPaths("/repo", "/repo") != nil {
		t.Errorf("wtRoot == mainRoot must yield no fragments")
	}
}
