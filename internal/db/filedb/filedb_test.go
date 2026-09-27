package filedb

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyAndRemoveDatabase(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	if err := os.WriteFile(src, []byte("main-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src+"-wal", []byte("wal-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "nested", "dst.db")
	if err := CopyDatabase(src, dst); err != nil {
		t.Fatalf("CopyDatabase: %v", err)
	}
	for _, p := range []string{dst, dst + "-wal"} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s after copy: %v", p, err)
		}
	}
	got, err := os.ReadFile(dst + "-wal")
	if err != nil || string(got) != "wal-bytes" {
		t.Errorf("sidecar copy wrong: %q err=%v", got, err)
	}

	removed, err := RemoveDatabase(dst)
	if err != nil {
		t.Fatalf("RemoveDatabase: %v", err)
	}
	if len(removed) != 2 {
		t.Errorf("removed %v, want main + sidecar", removed)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("dst still present after remove (err=%v)", err)
	}
	if _, err := os.Stat(dst + "-wal"); !os.IsNotExist(err) {
		t.Errorf("sidecar still present after remove (err=%v)", err)
	}
}

func TestExistsAndSizeKB(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "db.sqlite")
	if ok, err := Exists(p); ok || err != nil {
		t.Fatalf("missing file: Exists=%t err=%v", ok, err)
	}
	if err := os.WriteFile(p, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, err := Exists(p); !ok || err != nil {
		t.Fatalf("regular file: Exists=%t err=%v", ok, err)
	}
	if got := SizeKB(p); got != 4 {
		t.Errorf("SizeKB=%d, want 4", got)
	}
	// A directory is not a database file.
	if ok, err := Exists(dir); ok || err != nil {
		t.Errorf("directory: Exists=%t err=%v", ok, err)
	}
}

func TestCloneSiblings(t *testing.T) {
	dir := t.TempDir()
	files := []string{"app.db", "app.db-wal", "app_test_1.db", "app_test_2.db", "unrelated.db", "apps.db"}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := CloneSiblings(dir, "app.db")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "app_test_1.db"), filepath.Join(dir, "app_test_2.db")}
	if len(got) != len(want) {
		t.Fatalf("CloneSiblings=%v, want %v", got, want)
	}
	for _, p := range want {
		found := false
		for _, g := range got {
			if g == p {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %s in %v", p, got)
		}
	}
	// "apps.db" and "unrelated.db" must never be family matches.
	for _, g := range got {
		if strings.HasSuffix(g, "apps.db") || strings.HasSuffix(g, "unrelated.db") {
			t.Errorf("non-family file matched: %s", g)
		}
	}
}

func TestApplySQLDumpAndEngineVersion(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "seeded.db")
	dump := filepath.Join(dir, "schema.sql")
	if err := os.WriteFile(dump, []byte(
		"CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT);\n"+
			"INSERT INTO items (name) VALUES ('one'), ('two');\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ApplySQLDump(context.Background(), db, dump); err != nil {
		t.Fatalf("ApplySQLDump: %v", err)
	}
	if ok, err := Exists(db); !ok || err != nil {
		t.Fatalf("dump didn't create the database file: %t %v", ok, err)
	}
	version, err := EngineVersion(context.Background(), db)
	if err != nil || version == "" {
		t.Fatalf("EngineVersion=%q err=%v, want a sqlite version", version, err)
	}

	// A non-sqlite file reports no version rather than failing — a
	// duckdb database must still participate in copies + teardowns.
	bin := filepath.Join(dir, "warehouse.duckdb")
	if err := os.WriteFile(bin, []byte("DUCKDB-format-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, err := EngineVersion(context.Background(), bin); v != "" {
		t.Errorf("duckdb file version=%q err=%v, want empty", v, err)
	}
}

func TestTemplatePath(t *testing.T) {
	got := TemplatePath("/wt/data/app.db", "0123456789abcdef0123456789abcdef")
	if want := filepath.Join("/wt/data", "_tm_0123456789abcdef.db"); got != want {
		t.Errorf("TemplatePath=%q, want %q", got, want)
	}
}

func TestSourcePath(t *testing.T) {
	p, err := SourcePath("", "/worktrees/dev", "data/app.db")
	if err != nil || p != filepath.Join("/worktrees/dev", "data/app.db") {
		t.Fatalf("default base: %q err=%v", p, err)
	}
	p, err = SourcePath("/abs/base", "/worktrees/dev", "data/app.db")
	if err != nil || p != filepath.Join("/abs/base", "data/app.db") {
		t.Fatalf("base_dir: %q err=%v", p, err)
	}
	if _, err := SourcePath("relative/base", "/worktrees/dev", "app.db"); err == nil {
		t.Error("relative base_dir accepted")
	}
	_, err = SourcePath("", "/worktrees/dev", "../escape.db")
	if err == nil {
		t.Error(".. escape accepted")
	}
}

func TestReflinkFallsBackCleanly(t *testing.T) {
	// The copy must succeed whether or not the filesystem supports
	// reflinks — errors from reflink are a fallback trigger, never a
	// failure (ErrUnsupported on !linux, EOPNOTSUPP/EXDEV on ext4...).
	dir := t.TempDir()
	src := filepath.Join(dir, "a.db")
	dst := filepath.Join(dir, "b.db")
	if err := os.WriteFile(src, []byte(strings.Repeat("x", 1024)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyFileReflink(src, dst); err != nil {
		t.Fatalf("copyFileReflink: %v", err)
	}
	info, err := os.Stat(dst)
	if err != nil || info.Size() != 1024 {
		t.Errorf("dst size=%d err=%v, want 1024", info.Size(), err)
	}
}
