//go:build e2e

// Package file_engine_e2e exercises the file-backed engine family
// (engine "sqlite") end to end against the real prepare + teardown
// pipeline. No docker needed — a file engine has no server — so this
// suite doubles as the container-free proof of the prepare flow that
// the docker suites cover for the networked engines: cold build,
// fingerprint cache hit, test-clone fanout, and `wt delete`'s
// TeardownDatabases reaping the rendered files while the cached
// template survives.
package file_engine_e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stubbedev/treeman/e2e/harness"
	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/prepare"
)

const (
	schemaV1 = "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT);\nINSERT INTO items (name) VALUES ('one');\n"
	schemaV2 = "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT);\nINSERT INTO items (name) VALUES ('one'), ('two');\n"
)

// sqliteMagic is the 16-byte header every sqlite 3 file starts with.
const sqliteMagic = "SQLite format 3\x00"

func buildConfig(baseDir string) *config.Config {
	cfg := &config.Config{
		Databases: []config.DatabaseConfig{
			{
				Engine:       "sqlite",
				NameTemplate: "data/{slug}.db",
				Dump:         config.DumpList{{Path: "fixtures/schema.sql"}},
				Inputs: []config.Input{
					{Glob: "fixtures/schema.sql", Label: "schema"},
				},
			},
		},
	}
	if baseDir != "" {
		cfg.Connections.Sqlite = &config.SqliteConn{BaseDir: baseDir}
	}
	return cfg
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertSQLiteFile(t *testing.T, path string) {
	t.Helper()
	head, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(head) < len(sqliteMagic) || string(head[:len(sqliteMagic)]) != sqliteMagic {
		t.Errorf("%s is not a sqlite database (head=%q)", path, head[:min(len(head), 20)])
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s should be gone after teardown (err=%v)", path, err)
	}
}

func TestFileEngineEndToEnd(t *testing.T) {
	baseDir := t.TempDir()
	wt := t.TempDir()
	writeFile(t, filepath.Join(wt, "fixtures/schema.sql"), schemaV1)

	cfg := buildConfig(baseDir)
	env := harness.NewEnv(t, wt)

	// ── cold build ──
	outs := env.RunPrepare(t, cfg)
	o1 := harness.AssertOutcome(t, outs, "sqlite", false)
	assertSQLiteFile(t, o1.SourceDB)
	tmplInfo, err := os.Stat(o1.TemplateName)
	if err != nil {
		t.Fatalf("template file missing after cold build: %v", err)
	}
	if tmplInfo.Size() == 0 {
		t.Error("template file is empty")
	}
	if filepath.Dir(o1.TemplateName) != filepath.Dir(o1.SourceDB) {
		t.Errorf("template %s not cached as a sibling of source %s", o1.TemplateName, o1.SourceDB)
	}

	// ── cache hit: same inputs, same fingerprint, template restore ──
	outs = env.RunPrepare(t, cfg)
	o2 := harness.AssertOutcome(t, outs, "sqlite", true)
	if o2.Fingerprint != o1.Fingerprint {
		t.Errorf("fingerprint drift: %s vs %s", o2.Fingerprint, o1.Fingerprint)
	}
	assertSQLiteFile(t, o2.SourceDB)

	// ── input change → new fingerprint → cold rebuild ──
	writeFile(t, filepath.Join(wt, "fixtures/schema.sql"), schemaV2)
	outs = env.RunPrepare(t, cfg)
	o3 := harness.AssertOutcome(t, outs, "sqlite", false)
	if o3.Fingerprint == o1.Fingerprint {
		t.Error("fingerprint unchanged after schema.sql edit")
	}
	assertSQLiteFile(t, o3.SourceDB)

	// ── fanout: `_test_{n}` copies of the template ──
	fcfg := buildConfig(baseDir)
	fcfg.Databases[0].TestClones = &config.TestClonesSpec{
		Clones:       config.ClonesSetting{Fixed: 2},
		NameTemplate: "data/{slug}_test_{n}.db",
	}
	fo := harness.AssertOutcome(t, env.RunPrepare(t, fcfg), "sqlite", true)
	if len(fo.Clones) != 2 {
		t.Fatalf("fanout: got %d clones, want 2 (%v)", len(fo.Clones), fo.Clones)
	}
	for _, c := range fo.Clones {
		assertSQLiteFile(t, c)
	}

	// ── teardown: `wt delete`'s DB layer reaps the rendered family.
	// The files live OUTSIDE the worktree (base_dir), so their removal
	// proves the engineconn-driven reap rather than directory disposal.
	// The fingerprint-keyed template survives, like every engine.
	if err := prepare.TeardownDatabases(env.Ctx, fcfg, env.Slug.Value, env.RepoID, env.WTID, env.Store); err != nil {
		t.Fatalf("TeardownDatabases: %v", err)
	}
	assertMissing(t, fo.SourceDB)
	for _, c := range fo.Clones {
		assertMissing(t, c)
	}
	if _, err := os.Stat(fo.TemplateName); err != nil {
		t.Errorf("template should survive teardown: %v", err)
	}
}

// TestFileEngineInsideWorktree covers the default placement: no
// connections.sqlite block, the database file lives at its rendered
// path inside the worktree, and teardown reaps it there.
func TestFileEngineInsideWorktree(t *testing.T) {
	wt := t.TempDir()
	writeFile(t, filepath.Join(wt, "fixtures/schema.sql"), schemaV1)
	cfg := buildConfig("")
	env := harness.NewEnv(t, wt)

	o := harness.AssertOutcome(t, env.RunPrepare(t, cfg), "sqlite", false)
	if !strings.HasPrefix(o.SourceDB, wt+string(filepath.Separator)) {
		t.Fatalf("source %s not inside worktree %s", o.SourceDB, wt)
	}
	assertSQLiteFile(t, o.SourceDB)

	if err := prepare.TeardownDatabases(env.Ctx, cfg, env.Slug.Value, env.RepoID, env.WTID, env.Store); err != nil {
		t.Fatalf("TeardownDatabases: %v", err)
	}
	assertMissing(t, o.SourceDB)
}
