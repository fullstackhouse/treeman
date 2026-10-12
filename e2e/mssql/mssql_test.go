//go:build e2e

// SQL Server end-to-end: cold build → cache hit → input edit →
// rebuild, with the fan-out clones and `wt delete` teardown. Same
// shape as the postgres suite; what differs is that there is no
// `dump:` here (config rejects it for this family — RESTORE reads the
// server's filesystem, not treeman's), so the schema arrives through
// `migrate:` alone.
package mssql_e2e

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/microsoft/go-mssqldb"

	"github.com/stubbedev/treeman/e2e/harness"
	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/prepare"
)

const (
	addr     = "127.0.0.1:11433"
	password = "treemanE2E!pw1"
)

func TestMSSQLEndToEnd(t *testing.T) {
	harness.SkipIfNoDocker(t)
	t.Cleanup(harness.ComposeUp(t, harness.MustAbs(".")))
	waitForServer(t)

	wt := t.TempDir()
	copyTree(t, "fixtures", filepath.Join(wt, "fixtures"))
	cfg := buildConfig()
	env := harness.NewEnv(t, wt)

	outs := env.RunPrepare(t, cfg)
	o1 := harness.AssertOutcome(t, outs, "mssql", false)
	t.Logf("pass1: template=%s sourceDB=%s", o1.TemplateName, o1.SourceDB)
	assertTables(t, o1.SourceDB, []string{"products", "orders"})

	// The collation on the connection must reach the database it
	// creates — a clone that orders and compares text differently from
	// the real server is the subtle failure this knob exists to stop.
	if got := collationOf(t, o1.SourceDB); got != "Polish_CI_AS" {
		t.Errorf("source DB collation = %q, want Polish_CI_AS", got)
	}

	outs = env.RunPrepare(t, cfg)
	o2 := harness.AssertOutcome(t, outs, "mssql", true)
	if o2.Fingerprint != o1.Fingerprint {
		t.Errorf("fingerprint drift: %s vs %s", o1.Fingerprint, o2.Fingerprint)
	}
	// A cache hit restores from the template, so the clone must still
	// carry the data — a RESTORE that only brought the schema would
	// pass every "does the table exist" check.
	assertRowCount(t, o2.SourceDB, "dbo.products", 3)

	addMigration(t, wt)
	outs = env.RunPrepare(t, cfg)
	o3 := harness.AssertOutcome(t, outs, "mssql", false)
	if o3.Fingerprint == o1.Fingerprint {
		t.Errorf("fingerprint unchanged after edit")
	}
	assertTables(t, o3.SourceDB, []string{"products", "orders", "shipments"})

	if len(o3.Clones) != 2 {
		t.Fatalf("fanout: got %d clones, want 2 (%v)", len(o3.Clones), o3.Clones)
	}
	for _, c := range o3.Clones {
		if !dbExists(t, c) {
			t.Errorf("clone DB %s missing after fanout", c)
			continue
		}
		assertTables(t, c, []string{"products", "orders", "shipments"})
		assertRowCount(t, c, "dbo.products", 3)
	}

	// ── teardown: `wt delete` drops the per-worktree DBs, keeps the cache ──
	if err := prepare.TeardownDatabases(env.Ctx, cfg, env.Slug.Value, env.RepoID, env.WTID, env.Store); err != nil {
		t.Fatalf("TeardownDatabases: %v", err)
	}
	if dbExists(t, o3.SourceDB) {
		t.Errorf("source DB %s still exists after teardown", o3.SourceDB)
	}
	for _, c := range o3.Clones {
		if dbExists(t, c) {
			t.Errorf("clone DB %s still exists after teardown", c)
		}
	}
	if !dbExists(t, o3.TemplateName) {
		t.Errorf("template DB %s was dropped by teardown (cache must survive)", o3.TemplateName)
	}
}

// TestRejectsDump pins the one place this family deliberately refuses
// what the other SQL engines accept. The message has to name the
// reason, because "dump: is not supported" alone reads as an oversight.
func TestRejectsDump(t *testing.T) {
	cfg := buildConfig()
	cfg.Databases[0].Dump = config.DumpList{{Path: "fixtures/seed.sql"}}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("a `dump:` on an mssql entry must be rejected")
	}
	for _, want := range []string{"dump:", "RESTORE reads the path on the SQL Server host"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func buildConfig() *config.Config {
	return &config.Config{
		Connections: config.ConnectionsConfig{
			Mssql: &config.MssqlConn{
				Host:      "127.0.0.1",
				Port:      11433,
				User:      "sa",
				Password:  password,
				Collation: "Polish_CI_AS",
			},
		},
		Databases: []config.DatabaseConfig{
			{
				Engine:       "mssql",
				NameTemplate: "treeman_e2e_{slug}",
				Migrate: &config.Step{
					Run: "./fixtures/migrate.sh",
					Env: map[string]string{
						"DB_DATABASE":     "{target_db}",
						"MSSQL_CONTAINER": "treeman-e2e-mssql",
						"MSSQL_PASSWORD":  password,
					},
				},
				Inputs: []config.Input{
					{Glob: "fixtures/migrations/*.sql", Label: "migrations"},
				},
				// Fanout: 2 clones, so teardown's per-worktree cleanup
				// (source + clones) is exercised.
				TestClones: &config.TestClonesSpec{
					Clones:       config.ClonesSetting{Fixed: 2},
					NameTemplate: "treeman_e2e_{slug}_w{n}",
				},
			},
		},
	}
}

func waitForServer(t *testing.T) {
	t.Helper()
	// The compose healthcheck already gates on sqlcmd, but the TCP
	// probe is what the driver will do, and on an emulated arm64 host
	// the two can be seconds apart.
	harness.WaitForReady(t, "mssql:11433", 180*time.Second, func() error {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			return err
		}
		_ = c.Close()
		return nil
	})
}

func open(t *testing.T, dbName string) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("sqlserver://sa:%s@%s?database=%s&encrypt=disable",
		"treemanE2E%21pw1", addr, dbName)
	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dbName, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func assertTables(t *testing.T, dbName string, want []string) {
	t.Helper()
	db := open(t, dbName)
	got := map[string]bool{}
	rows, err := db.QueryContext(context.Background(),
		"SELECT name FROM sys.tables WHERE schema_id = SCHEMA_ID('dbo')")
	if err != nil {
		t.Fatalf("list tables in %s: %v", dbName, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		got[n] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("%s: table %q missing (have %v)", dbName, w, got)
		}
	}
}

func assertRowCount(t *testing.T, dbName, table string, want int) {
	t.Helper()
	db := open(t, dbName)
	var n int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s.%s: %v", dbName, table, err)
	}
	if n != want {
		t.Errorf("%s.%s has %d rows, want %d", dbName, table, n, want)
	}
}

func collationOf(t *testing.T, dbName string) string {
	t.Helper()
	db := open(t, "master")
	var c string
	if err := db.QueryRowContext(context.Background(),
		"SELECT CONVERT(nvarchar(128), DATABASEPROPERTYEX(@p1, 'Collation'))", dbName).Scan(&c); err != nil {
		t.Fatalf("collation of %s: %v", dbName, err)
	}
	return c
}

func dbExists(t *testing.T, dbName string) bool {
	t.Helper()
	db := open(t, "master")
	var n int
	err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM sys.databases WHERE name = @p1", dbName).Scan(&n)
	if err != nil {
		t.Fatalf("exists %s: %v", dbName, err)
	}
	return n > 0
}

func addMigration(t *testing.T, wt string) {
	t.Helper()
	body := "CREATE TABLE dbo.shipments (id int IDENTITY PRIMARY KEY, order_id int NOT NULL REFERENCES dbo.orders(id));\nGO\n"
	if err := os.WriteFile(filepath.Join(wt, "fixtures/migrations/002_shipments.sql"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		s, d := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		if e.IsDir() {
			copyTree(t, s, d)
			continue
		}
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(s)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(d, body, info.Mode()); err != nil {
			t.Fatal(err)
		}
	}
}
