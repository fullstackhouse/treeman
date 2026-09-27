package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestIncludeMergesFragment pins the #78 acceptance criteria: a repo
// config with `include: [shared/dbs.yaml]` loads IDENTICAL databases
// to an inlined copy, the including file's own keys win over the
// fragment, includes merge before the file's keys, and a cycle errors
// instead of looping.
func TestIncludeMergesFragment(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	frag := `databases:
  - engine: mysql
    name_template: fleet_{slug}
    branch_scoped: true
`
	write("shared/dbs.yaml", frag)

	// Included form.
	write(".treeman.yaml", "include: [shared/dbs.yaml]\n")
	included, err := LoadLayered(dir)
	if err != nil {
		t.Fatalf("LoadLayered with include: %v", err)
	}
	if len(included.Databases) != 1 || included.Databases[0].NameTemplate != "fleet_{slug}" {
		t.Fatalf("included databases = %+v, want the fragment's entry", included.Databases)
	}

	// Inlined form must produce the identical databases slice.
	write(".treeman.yaml", frag)
	inlined, err := LoadLayered(dir)
	if err != nil {
		t.Fatalf("LoadLayered inlined: %v", err)
	}
	if !reflect.DeepEqual(included.Databases, inlined.Databases) {
		t.Errorf("included != inlined:\n%+v\n%+v", included.Databases, inlined.Databases)
	}

	// The including file's own keys override the fragment.
	write(".treeman.yaml", "include: [shared/dbs.yaml]\ndatabases:\n  - engine: postgres\n    name_template: override_{slug}\n")
	overridden, err := LoadLayered(dir)
	if err != nil {
		t.Fatalf("LoadLayered override: %v", err)
	}
	if len(overridden.Databases) != 1 || overridden.Databases[0].Engine != "postgres" {
		t.Errorf("including file should override the fragment, got %+v", overridden.Databases)
	}
}

// TestIncludeCycleErrors pins the cycle guard: a file including itself
// (directly or transitively) must error, not loop.
func TestIncludeCycleErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".treeman.yaml", "include: [b.yaml]\n")
	write("b.yaml", "include: [.treeman.yaml]\n")

	if _, err := LoadLayered(dir); err == nil {
		t.Fatal("mutual include should error")
	}
}
