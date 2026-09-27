//go:build e2e

// Package namedconns_e2e verifies the #44 acceptance criteria against
// TWO real postgres servers: two named connections under one family,
// two databases entries selecting different servers via
// `connection:`, prepares + clones landing on the correct server, and
// fingerprint isolation between the two (a cache hit for one must
// never be served by the other's template). Omitting `connection:`
// with exactly one singular block per family keeps today's behaviour.
package namedconns_e2e

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/stubbedev/treeman/e2e/harness"
	"github.com/stubbedev/treeman/internal/config"
)

const (
	mainAddr = "127.0.0.1:15441"
	repAddr  = "127.0.0.1:15442"
)

func dsn(addr, db string) string {
	return "postgres://postgres:pgpw@" + addr + "/" + db + "?sslmode=disable"
}

func up(t *testing.T) {
	t.Helper()
	t.Cleanup(harness.ComposeUp(t, harness.MustAbs(".")))
	for _, addr := range []string{mainAddr, repAddr} {
		a := addr
		harness.WaitForReady(t, "postgres:"+a, 60*time.Second, func() error {
			db, err := sql.Open("pgx", dsn(a, "postgres"))
			if err != nil {
				return err
			}
			defer db.Close()
			return db.Ping()
		})
	}
}

func pgDBExists(t *testing.T, addr, db string) bool {
	t.Helper()
	q := db2(t, addr)
	defer q.Close()
	var n int
	if err := q.QueryRow(
		`SELECT COUNT(*) FROM pg_database WHERE datname = $1`, db).Scan(&n); err != nil {
		t.Fatalf("pg_database query: %v", err)
	}
	return n > 0
}

func db2(t *testing.T, addr string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn(addr, "postgres"))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func twoServerConfig() *config.Config {
	return &config.Config{
		Connections: config.ConnectionsConfig{
			PostgresNamed: map[string]*config.PostgresConn{
				"app":       {Host: "127.0.0.1", Port: 15441, User: "postgres", Password: "pgpw"},
				"reporting": {Host: "127.0.0.1", Port: 15442, User: "postgres", Password: "pgpw"},
			},
		},
		Databases: []config.DatabaseConfig{
			{
				Engine: "postgres", NameTemplate: "nc_app_{slug}",
				Connection: "app",
				Dump:       config.DumpList{{Path: "seed.sql"}},
			},
			{
				Engine: "postgres", NameTemplate: "nc_bi_{slug}",
				Connection: "reporting",
				Dump:       config.DumpList{{Path: "seed.sql"}},
			},
		},
	}
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNamedConnectionsEndToEnd(t *testing.T) {
	harness.SkipIfNoDocker(t)
	up(t)

	wt := t.TempDir()
	write(t, wt, "seed.sql", "CREATE TABLE t (id INT); INSERT INTO t VALUES (1);")
	cfg := twoServerConfig()
	env := harness.NewEnv(t, wt)

	// Cold build: each entry lands on ITS OWN server.
	outs := env.RunPrepare(t, cfg)
	if len(outs) != 2 {
		t.Fatalf("got %d outcomes, want 2", len(outs))
	}
	byDB := map[string]bool{}
	for _, o := range outs {
		byDB[o.SourceDB] = true
	}
	if !byDB["nc_app_"+env.Slug.Value] || !byDB["nc_bi_"+env.Slug.Value] {
		t.Fatalf("missing expected source DBs: %v", byDB)
	}
	if !pgDBExists(t, mainAddr, "nc_app_"+env.Slug.Value) {
		t.Errorf("app entry did not land on pg-main")
	}
	if !pgDBExists(t, repAddr, "nc_bi_"+env.Slug.Value) {
		t.Errorf("reporting entry did not land on pg-reporting")
	}
	// Cross-server leak check: the app template must not exist on the
	// reporting server and vice versa.
	for _, o := range outs {
		if o.SourceDB == "nc_app_"+env.Slug.Value && pgDBExists(t, repAddr, o.TemplateName) {
			t.Errorf("app template leaked onto the reporting server")
		}
		if o.SourceDB == "nc_bi_"+env.Slug.Value && pgDBExists(t, mainAddr, o.TemplateName) {
			t.Errorf("reporting template leaked onto the main server")
		}
	}

	// Cache hit: same connection → same fingerprint row → hit.
	outs = env.RunPrepare(t, cfg)
	for _, o := range outs {
		if !o.CacheHit {
			t.Errorf("%s: expected cache hit on second run", o.SourceDB)
		}
	}

	// Branch-scoped entry rides its connection selector too.
	bcfg := twoServerConfig()
	bcfg.Databases = bcfg.Databases[:1]
	bcfg.Databases[0].BranchScoped = true
	bcfg.Databases[0].NameTemplate = "ncbs_app_{slug}"
	env.RunPrepare(t, bcfg)
	if !pgDBExists(t, mainAddr, "ncbs_app_"+env.Slug.Value) {
		t.Errorf("branch-scoped entry did not land on pg-main")
	}
}

// TestSingularBlockStillDefault pins the backward-compat criterion:
// omitting `connection:` with a single singular block behaves exactly
// as before.
func TestSingularBlockStillDefault(t *testing.T) {
	harness.SkipIfNoDocker(t)
	up(t)

	wt := t.TempDir()
	write(t, wt, "seed.sql", "CREATE TABLE t (id INT);")
	cfg := &config.Config{
		Connections: config.ConnectionsConfig{
			Postgres: &config.PostgresConn{Host: "127.0.0.1", Port: 15441, User: "postgres", Password: "pgpw"},
		},
		Databases: []config.DatabaseConfig{{
			Engine: "postgres", NameTemplate: "ncsolo_{slug}",
			Dump: config.DumpList{{Path: "seed.sql"}},
		}},
	}
	o := harness.AssertOutcome(t, harness.NewEnv(t, wt).RunPrepare(t, cfg), "postgres", false)
	if !pgDBExists(t, mainAddr, o.SourceDB) {
		t.Errorf("singular-block source DB %s not created", o.SourceDB)
	}
}
