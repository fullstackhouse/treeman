//go:build e2e

// Driver-level SQL Server checks: the clone lifecycle against a real
// server, which is the only place the engine's own behaviour can be
// observed. A mocked driver here would re-assert what we already
// believe — and the BACKUP/RESTORE clone path, the rename-based spare
// claim and the LIKE-escaping guard are exactly where that belief has
// somewhere to be wrong.
package mssql_e2e

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stubbedev/treeman/e2e/harness"
	"github.com/stubbedev/treeman/internal/config"
	dbmssql "github.com/stubbedev/treeman/internal/db/mssql"
)

// driverConn is the connection the driver tests dial. Collation is set
// so the EnsureDB path is covered.
func driverConn() config.MssqlConn {
	return config.MssqlConn{
		Host: "127.0.0.1", Port: 11433, User: "sa", Password: password,
		PoolMax: 8, Collation: "Polish_CI_AS",
	}
}

const (
	drvPrefix   = "tmdrv_"
	drvSource   = drvPrefix + "src"
	drvTemplate = "_tm_" + "deadbeefdeadbeef"
)

func connectDriver(t *testing.T) (*dbmssql.Driver, context.Context) {
	t.Helper()
	harness.SkipIfNoDocker(t)
	t.Cleanup(harness.ComposeUp(t, harness.MustAbs(".")))
	waitForServer(t)
	ctx := context.Background()
	d, err := dbmssql.Connect(ctx, driverConn())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	cleanup := func() {
		if _, err := d.DropMatching(ctx, drvPrefix); err != nil {
			t.Logf("cleanup DropMatching: %v", err)
		}
		if err := d.DropSnapshot(ctx, drvTemplate); err != nil {
			t.Logf("cleanup DropSnapshot: %v", err)
		}
	}
	cleanup()
	t.Cleanup(cleanup)
	return d, ctx
}

// seedSource builds a source database with the things a clone is
// easiest to get wrong about: rows in a non-dbo schema, a heap (no
// index at all), a scalar function, and non-ASCII text.
func seedSource(t *testing.T, d *dbmssql.Driver, ctx context.Context) {
	t.Helper()
	if err := d.EnsureDB(ctx, drvSource); err != nil {
		t.Fatalf("EnsureDB: %v", err)
	}
	// Twice: EnsureDB is called on every cold build and must be a no-op
	// on the second.
	if err := d.EnsureDB(ctx, drvSource); err != nil {
		t.Fatalf("EnsureDB (second call): %v", err)
	}
	scoped, err := d.OpenScoped(ctx, drvSource)
	if err != nil {
		t.Fatalf("OpenScoped: %v", err)
	}
	defer func() { _ = scoped.Close() }()
	for _, q := range []string{
		"CREATE SCHEMA app",
		"CREATE TABLE app.orders (id int IDENTITY PRIMARY KEY, note nvarchar(100))",
		"CREATE TABLE dbo.heap (v nvarchar(50))",
		"CREATE FUNCTION dbo.answer() RETURNS int AS BEGIN RETURN 42 END",
		"INSERT INTO app.orders(note) VALUES (N'zażółć'), (N'gęślą'), (N'jaźń'), (N'x'), (N'y')",
		"INSERT INTO dbo.heap(v) VALUES (N'a'), (N'b')",
	} {
		if _, err := scoped.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
}

// assertClone proves a clone is a copy of the source rather than a
// same-shaped empty: the rows, the scalar function body, and the
// non-ASCII text round-tripped through BACKUP/RESTORE.
func assertClone(t *testing.T, d *dbmssql.Driver, ctx context.Context, name string) {
	t.Helper()
	scoped, err := d.OpenScoped(ctx, name)
	if err != nil {
		t.Errorf("OpenScoped %s: %v", name, err)
		return
	}
	defer func() { _ = scoped.Close() }()
	var rows, answer int
	var note string
	if err := scoped.QueryRowContext(ctx, "SELECT COUNT(*) FROM app.orders").Scan(&rows); err != nil {
		t.Errorf("%s count: %v", name, err)
		return
	}
	if err := scoped.QueryRowContext(ctx, "SELECT dbo.answer()").Scan(&answer); err != nil {
		t.Errorf("%s function: %v", name, err)
		return
	}
	if err := scoped.QueryRowContext(ctx, "SELECT note FROM app.orders WHERE id = 1").Scan(&note); err != nil {
		t.Errorf("%s note: %v", name, err)
		return
	}
	if rows != 5 || answer != 42 || note != "zażółć" {
		t.Errorf("%s: rows=%d answer=%d note=%q — want 5, 42, %q", name, rows, answer, note, "zażółć")
	}
}

// TestSnapshotCloneLifecycle is the core contract: capture once,
// hydrate many, concurrently, from one template.
func TestSnapshotCloneLifecycle(t *testing.T) {
	d, ctx := connectDriver(t)
	seedSource(t, d, ctx)

	if err := d.SnapshotCreate(ctx, drvSource, drvTemplate); err != nil {
		t.Fatalf("SnapshotCreate: %v", err)
	}
	// The template is a real database, because DatabaseExists is what
	// the cache-hit probe asks — the same question for every engine.
	ok, err := d.DatabaseExists(ctx, drvTemplate)
	if err != nil || !ok {
		t.Fatalf("template database missing after SnapshotCreate (ok=%v err=%v)", ok, err)
	}
	assertClone(t, d, ctx, drvTemplate)

	// The source must be untouched: callers treat it as read-only and
	// restore the template over the clones, not over it.
	assertClone(t, d, ctx, drvSource)

	// A fan-out is concurrent restores of ONE template — which for this
	// engine means several readers of one backup file at once.
	const clones = 8
	var wg sync.WaitGroup
	errs := make([]error, clones)
	for i := range clones {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = d.SnapshotRestore(ctx, drvTemplate, fmt.Sprintf("%sw%d", drvPrefix, i+1))
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent SnapshotRestore %d: %v", i+1, err)
		}
	}
	for i := range clones {
		assertClone(t, d, ctx, fmt.Sprintf("%sw%d", drvPrefix, i+1))
	}

	// Restoring over an existing clone is how a stale one is replaced.
	if err := d.SnapshotRestore(ctx, drvTemplate, drvPrefix+"w1"); err != nil {
		t.Errorf("SnapshotRestore over an existing target: %v", err)
	}
	assertClone(t, d, ctx, drvPrefix+"w1")
}

// TestEnsureDBAppliesCollation pins the connection's collation reaching
// the databases treeman creates, and SIMPLE recovery reaching both the
// created and the restored ones — a FULL-recovery clone grows a log
// nobody will ever back up.
func TestEnsureDBAppliesCollation(t *testing.T) {
	d, ctx := connectDriver(t)
	seedSource(t, d, ctx)
	if err := d.SnapshotCreate(ctx, drvSource, drvTemplate); err != nil {
		t.Fatalf("SnapshotCreate: %v", err)
	}
	for _, name := range []string{drvSource, drvTemplate} {
		scoped, err := d.OpenScoped(ctx, name)
		if err != nil {
			t.Fatalf("OpenScoped %s: %v", name, err)
		}
		var collation, recovery string
		err = scoped.QueryRowContext(ctx, `
			SELECT CONVERT(nvarchar(128), DATABASEPROPERTYEX(DB_NAME(), 'Collation')),
			       CONVERT(nvarchar(32), DATABASEPROPERTYEX(DB_NAME(), 'Recovery'))`,
		).Scan(&collation, &recovery)
		_ = scoped.Close()
		if err != nil {
			t.Fatalf("properties of %s: %v", name, err)
		}
		if collation != "Polish_CI_AS" {
			t.Errorf("%s collation = %q, want Polish_CI_AS", name, collation)
		}
		if recovery != "SIMPLE" {
			t.Errorf("%s recovery = %q, want SIMPLE", name, recovery)
		}
	}
}

// TestListMatchingEscapesUnderscore is the sibling-wipe guard. Every
// templated name carries a literal `_`; unescaped it is a
// single-character wildcard, and a prefix reap would then match another
// worktree's databases.
func TestListMatchingEscapesUnderscore(t *testing.T) {
	d, ctx := connectDriver(t)
	if err := d.EnsureDB(ctx, drvSource); err != nil {
		t.Fatalf("EnsureDB: %v", err)
	}
	// Same length as drvSource, differing only where the `_` is — a
	// wildcard match, never a prefix match.
	decoy := "tmdrvXsrc"
	if err := d.EnsureDB(ctx, decoy); err != nil {
		t.Fatalf("EnsureDB decoy: %v", err)
	}
	t.Cleanup(func() { _ = d.DropDatabase(ctx, decoy) })

	names, err := d.ListMatching(ctx, drvPrefix+"s")
	if err != nil {
		t.Fatalf("ListMatching: %v", err)
	}
	for _, n := range names {
		if n == decoy {
			t.Fatalf("ListMatching(%q) matched %q — LIKE metacharacters are not escaped", drvPrefix+"s", decoy)
		}
	}
	if len(names) != 1 || names[0] != drvSource {
		t.Errorf("ListMatching(%q) = %v, want [%s]", drvPrefix+"s", names, drvSource)
	}
}

// TestClaimSpareRename covers the prewarm claim and the file-name trap
// behind it: MODIFY NAME moves the database but not its files, so a
// restore that named files after the database would collide with files
// the renamed one still holds open.
func TestClaimSpareRename(t *testing.T) {
	d, ctx := connectDriver(t)
	seedSource(t, d, ctx)
	if err := d.SnapshotCreate(ctx, drvSource, drvTemplate); err != nil {
		t.Fatalf("SnapshotCreate: %v", err)
	}
	spare, claimed := drvPrefix+"spare1", drvPrefix+"claimed"
	if err := d.SnapshotRestore(ctx, drvTemplate, spare); err != nil {
		t.Fatalf("restore spare: %v", err)
	}
	if err := d.RenameDatabase(ctx, spare, claimed); err != nil {
		t.Fatalf("RenameDatabase: %v", err)
	}
	if ok, _ := d.DatabaseExists(ctx, spare); ok {
		t.Errorf("%s still present after rename", spare)
	}
	assertClone(t, d, ctx, claimed)

	// Replenishment: the freed spare name must be restorable again
	// while the claimed database still holds the original files.
	if err := d.SnapshotRestore(ctx, drvTemplate, spare); err != nil {
		t.Fatalf("re-restore of the freed spare name: %v", err)
	}
	assertClone(t, d, ctx, spare)
}

// TestRenameRaceHasOneWinner is the atomicity the spare-claim path
// leans on across processes: of two claimers of one target, exactly one
// wins and the other fails rather than overwriting.
func TestRenameRaceHasOneWinner(t *testing.T) {
	d, ctx := connectDriver(t)
	seedSource(t, d, ctx)
	if err := d.SnapshotCreate(ctx, drvSource, drvTemplate); err != nil {
		t.Fatalf("SnapshotCreate: %v", err)
	}
	a, b, target := drvPrefix+"racea", drvPrefix+"raceb", drvPrefix+"won"
	for _, n := range []string{a, b} {
		if err := d.SnapshotRestore(ctx, drvTemplate, n); err != nil {
			t.Fatalf("restore %s: %v", n, err)
		}
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, from := range []string{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = d.RenameDatabase(ctx, from, target)
		}()
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		if err == nil {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("%d of 2 racing claimers won, want exactly 1 (errs: %v)", won, errs)
	}
	assertClone(t, d, ctx, target)
	// The loser must still be usable — the claim path moves on to the
	// next slot rather than giving up, and a database left in
	// single-user mode would wedge it.
	loser := a
	if errs[0] == nil {
		loser = b
	}
	assertClone(t, d, ctx, loser)
}

// TestDropSnapshotRemovesBackup proves the template's backup file goes
// with the template database. The file is on the server, so the
// observable is that a restore can no longer be served from it.
func TestDropSnapshotRemovesBackup(t *testing.T) {
	d, ctx := connectDriver(t)
	seedSource(t, d, ctx)
	if err := d.SnapshotCreate(ctx, drvSource, drvTemplate); err != nil {
		t.Fatalf("SnapshotCreate: %v", err)
	}
	if err := d.DropSnapshot(ctx, drvTemplate); err != nil {
		t.Fatalf("DropSnapshot: %v", err)
	}
	if ok, _ := d.DatabaseExists(ctx, drvTemplate); ok {
		t.Fatalf("template %s survived DropSnapshot", drvTemplate)
	}
	// Idempotent — GC and the orphan sweep both call it speculatively.
	if err := d.DropSnapshot(ctx, drvTemplate); err != nil {
		t.Errorf("DropSnapshot (second call): %v", err)
	}
	if err := d.SnapshotRestore(ctx, drvTemplate, drvPrefix+"w1"); err == nil {
		t.Error("SnapshotRestore from a dropped snapshot must fail — the backup file was left behind")
	}
}

// TestSnapshotRestoreRederivesBackup covers the half-state: the
// template database is still good but its backup file is gone (someone
// cleaned the directory, or the template predates this driver). A cold
// rebuild of a demonstrably-good template is the wrong answer.
func TestSnapshotRestoreRederivesBackup(t *testing.T) {
	d, ctx := connectDriver(t)
	seedSource(t, d, ctx)
	if err := d.SnapshotCreate(ctx, drvSource, drvTemplate); err != nil {
		t.Fatalf("SnapshotCreate: %v", err)
	}
	// Remove the file behind the template's back, the way a cleaned
	// backup directory would.
	bak := d.SnapshotBackupPath(drvTemplate)
	if _, err := d.DB.ExecContext(ctx, "EXEC master.sys.xp_delete_files @p1", bak); err != nil {
		t.Fatalf("remove %s: %v", bak, err)
	}
	if err := d.SnapshotRestore(ctx, drvTemplate, drvPrefix+"w1"); err != nil {
		t.Fatalf("SnapshotRestore after the backup file was removed: %v", err)
	}
	assertClone(t, d, ctx, drvPrefix+"w1")
}

// TestDropMatchingEvictsConnections pins the difference from Postgres:
// there is no pg_terminate_backend here, so a DROP with an idle pooled
// connection open on the database fails unless the driver takes it
// single-user first. A jest worker leaving a pool behind is the normal
// case, not the exotic one.
func TestDropMatchingEvictsConnections(t *testing.T) {
	d, ctx := connectDriver(t)
	seedSource(t, d, ctx)
	if err := d.SnapshotCreate(ctx, drvSource, drvTemplate); err != nil {
		t.Fatalf("SnapshotCreate: %v", err)
	}
	if err := d.SnapshotRestore(ctx, drvTemplate, drvPrefix+"w1"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	live, err := d.OpenScoped(ctx, drvPrefix+"w1")
	if err != nil {
		t.Fatalf("OpenScoped: %v", err)
	}
	if err := live.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	dropped, err := d.DropMatching(ctx, drvPrefix)
	_ = live.Close()
	if err != nil {
		t.Fatalf("DropMatching with a live connection: %v", err)
	}
	if len(dropped) == 0 {
		t.Fatal("DropMatching dropped nothing")
	}
	left, err := d.ListMatching(ctx, drvPrefix)
	if err != nil {
		t.Fatalf("ListMatching: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("databases left after DropMatching: %v", left)
	}
}

// TestRowCountSampleMatchesAcrossClone is what capture verification
// rests on: the sample the source yields must count the same in the
// copy. The sample names carry their schema, because a table name
// alone is not unique in a SQL Server database.
func TestRowCountSampleMatchesAcrossClone(t *testing.T) {
	d, ctx := connectDriver(t)
	seedSource(t, d, ctx)
	sample, err := d.RowCountSample(ctx, drvSource)
	if err != nil {
		t.Fatalf("RowCountSample: %v", err)
	}
	if len(sample) == 0 {
		t.Fatal("RowCountSample is empty — capture verification would record every capture unverified")
	}
	if _, ok := sample["app.orders"]; !ok {
		t.Errorf("sample %v does not name app.orders schema-qualified", sample)
	}
	if err := d.SnapshotCreate(ctx, drvSource, drvTemplate); err != nil {
		t.Fatalf("SnapshotCreate: %v", err)
	}
	names := make([]string, 0, len(sample))
	for k := range sample {
		names = append(names, k)
	}
	got, err := d.RowCountsFor(ctx, drvTemplate, names)
	if err != nil {
		t.Fatalf("RowCountsFor: %v", err)
	}
	for k, want := range sample {
		if got[k] != want {
			t.Errorf("clone %s = %d, source = %d", k, got[k], want)
		}
	}
}

// TestWriteWatermarkDeclines pins the deliberate refusal. A token that
// can miss a write is worse than none: the caller SKIPS a capture when
// the token is unchanged.
func TestWriteWatermarkDeclines(t *testing.T) {
	d, ctx := connectDriver(t)
	seedSource(t, d, ctx)
	wm, err := d.WriteWatermark(ctx, drvSource)
	if err != nil {
		t.Fatalf("WriteWatermark: %v", err)
	}
	if wm != "" {
		t.Errorf("WriteWatermark = %q, want \"\" (no sound per-database write counter on this engine)", wm)
	}
}
