//go:build e2e

package mysql_e2e

import (
	"database/sql"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/stubbedev/treeman/e2e/harness"
	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/prepare"
	"github.com/stubbedev/treeman/internal/snapshot"
	"github.com/stubbedev/treeman/internal/store"
)

// TestMySQLPrewarmClaimWithoutLogicalRestore exercises
// `databases[].prewarm` on MySQL (#53): the replenisher builds
// `<template>_spareN` via the physical-clone path, and the next
// prepare's cache hit CLAIMS a spare — a physical clone of the spare,
// no logical dump load — observable as snapshots:prewarm:claim events.
func TestMySQLPrewarmClaimWithoutLogicalRestore(t *testing.T) {
	harness.SkipIfNoDocker(t)
	composeDir := harness.MustAbs(".")
	t.Cleanup(harness.ComposeUp(t, composeDir))
	harness.WaitForReady(t, "mysql:13306", 60*time.Second, func() error {
		c, err := net.DialTimeout("tcp", "127.0.0.1:13306", 1*time.Second)
		if err != nil {
			return err
		}
		_ = c.Close()
		return nil
	})

	wt := t.TempDir()
	copyTree(t, "fixtures", filepath.Join(wt, "fixtures"))
	cfg := buildConfig()
	cfg.Databases[0].Prewarm = 2
	// Two paratest clones → three restores per cache hit (source + 2
	// clones) against a pool of 2: both spares are claimed and the third
	// restore falls back to the staged clone.
	cfg.Databases[0].TestClones = &config.TestClonesSpec{
		Clones:       config.ClonesSetting{Fixed: 2},
		NameTemplate: "treeman_e2e_{slug}_w{n}",
	}
	env := harness.NewEnv(t, wt)

	// ── pass 1: cold build; the detached replenisher fills the pool ──
	outs := env.RunPrepare(t, cfg)
	o1 := harness.AssertOutcome(t, outs, "mysql", false)
	spare1 := snapshot.SpareName(o1.TemplateName, 1)
	spare2 := snapshot.SpareName(o1.TemplateName, 2)
	waitForMySQLSpares(t, spare1, spare2)

	// ── teardown: the worktree's databases go, the template + spares
	// stay. Without it pass 2 is the fingerprint-gated no-op cache hit
	// (#38) — nothing to restore, so nothing to claim. ──
	if err := prepare.TeardownDatabases(env.Ctx, cfg, env.Slug.Value, env.RepoID, env.WTID, env.Store); err != nil {
		t.Fatalf("TeardownDatabases: %v", err)
	}

	// ── pass 2: cache hit claims spares instead of logical restores ──
	outs = env.RunPrepare(t, cfg)
	o2 := harness.AssertOutcome(t, outs, "mysql", true)
	if o2.Fingerprint != o1.Fingerprint {
		t.Fatalf("fingerprint drift: %s vs %s", o1.Fingerprint, o2.Fingerprint)
	}
	assertTablesPresent(t, "127.0.0.1:13306", o2.SourceDB, []string{"products", "orders"})

	claims, err := env.Store.QueryEvents(env.Ctx, store.EventFilter{
		RepoID:     env.RepoID,
		EventTypes: []string{store.EvtSnapshotsPrewarmClaim},
	})
	if err != nil {
		t.Fatalf("query claim events: %v", err)
	}
	// Pool of 2 vs three restores (source + 2 clones): both spares must
	// have been claimed, the third restore fell back.
	if len(claims) != 2 {
		t.Errorf("prewarm claims = %d, want 2 (pool size)", len(claims))
	}
	for _, c := range claims {
		if !strings.Contains(c.PayloadJSON, "mysql") {
			t.Errorf("claim event payload = %s, want engine=mysql", c.PayloadJSON)
		}
	}
	// The replenisher tops the pool back up after the claims.
	waitForMySQLSpares(t, spare1, spare2)

	// ── teardown: spares survive with the template ──
	if err := prepare.TeardownDatabases(env.Ctx, cfg, env.Slug.Value, env.RepoID, env.WTID, env.Store); err != nil {
		t.Fatalf("TeardownDatabases: %v", err)
	}
	for _, s := range []string{spare1, spare2} {
		if !mysqlDBExists(t, s) {
			t.Errorf("spare %s was dropped by worktree teardown (must survive with the template)", s)
		}
	}

	// ── purge: spare family dies with the template ──
	if dropped, errs := snapshot.PurgeRepo(env.Ctx, cfg, env.Store, env.RepoID); len(errs) > 0 {
		t.Fatalf("PurgeRepo (dropped=%d): %v", dropped, errs)
	}
	for _, name := range []string{o1.TemplateName, spare1, spare2} {
		if mysqlDBExists(t, name) {
			t.Errorf("%s still exists after purge", name)
		}
	}
}

// waitForMySQLSpares polls information_schema until every named spare
// exists — the replenisher is a detached goroutine.
func waitForMySQLSpares(t *testing.T, names ...string) {
	t.Helper()
	deadline := time.Now().Add(120 * time.Second)
	for {
		all := true
		for _, n := range names {
			if !mysqlDBExists(t, n) {
				all = false
				break
			}
		}
		if all {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("spares never appeared: %v", names)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// mysqlDBExists reports whether the named database is live on the
// e2e MySQL server.
func mysqlDBExists(t *testing.T, name string) bool {
	t.Helper()
	dsn := fmt.Sprintf("root:rootpw@tcp(127.0.0.1:13306)/?multiStatements=true")
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open mysql: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = ?", name,
	).Scan(&n); err != nil {
		t.Fatalf("query schemata: %v", err)
	}
	return n > 0
}
