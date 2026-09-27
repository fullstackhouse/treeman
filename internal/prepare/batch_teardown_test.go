package prepare

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/db/engineconn"
	"github.com/stubbedev/treeman/internal/engine"
	"github.com/stubbedev/treeman/internal/slug"
	"github.com/stubbedev/treeman/internal/store"
	"github.com/stubbedev/treeman/internal/template"
)

// countingConn is a fake engineconn.Conn that counts dials at the
// connect-func level (the pool, not the conn, is under test) and
// records every DropMatching target.
type countingConn struct {
	dropped *[]string
}

func (c countingConn) Close() error                                  { return nil }
func (c countingConn) EngineVersion(context.Context) (string, error) { return "", nil }
func (c countingConn) Exists(context.Context, string) (bool, error)  { return false, nil }
func (c countingConn) DropMatching(_ context.Context, n string) (int, error) {
	*c.dropped = append(*c.dropped, n)
	return 1, nil
}
func (c countingConn) DropSnapshot(context.Context, string) error { return nil }
func (c countingConn) ListMatching(context.Context, string) ([]string, error) {
	return nil, nil
}
func (c countingConn) SizeKB(context.Context, string) int64 { return 0 }

// TestTeardownSlugsConnReusePins the #88 contract: a batch teardown
// over N slugs dials each engine family at most once, still renders
// and drops every slug's namespaces, and counts successful slugs.
func TestTeardownSlugsConnReuse(t *testing.T) {
	cfg := &config.Config{
		Databases: []config.DatabaseConfig{
			{Engine: "mysql", NameTemplate: "app_{slug}"},
			{Engine: "postgres", NameTemplate: "app_{slug}"},
		},
	}
	// Both engines count as configured: engineconn.Configured is
	// bypassed via the injected connect in teardownSlugsVia? No —
	// Configured is still consulted; force it by declaring connections.
	cfg.Connections.Mysql = &config.MysqlConn{Host: "127.0.0.1", Port: 3306}
	cfg.Connections.Postgres = &config.PostgresConn{Host: "127.0.0.1", Port: 5432}

	var dials atomic.Int64
	dropped := &[]string{}
	fakeConnect := func(context.Context, *config.Config, engine.Family, string) (engineconn.Conn, bool, error) {
		dials.Add(1)
		return countingConn{dropped: dropped}, true, nil
	}

	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	repoID, _ := st.EnsureRepo(context.Background(), "/repo", "repo")

	slugs := []string{"app_alpha", "app_beta", "app_gamma"}
	purged := teardownSlugsVia(context.Background(), cfg, slugs, repoID, 0, st, fakeConnect)

	if purged != len(slugs) {
		t.Errorf("purged = %d, want %d", purged, len(slugs))
	}
	if got := dials.Load(); got != 2 {
		t.Errorf("dials = %d, want 2 (one per engine family, not per slug)", got)
	}
	if len(*dropped) != 2*len(slugs) {
		t.Errorf("drops = %d, want %d (every slug x every engine)", len(*dropped), 2*len(slugs))
	}
	for _, sl := range slugs {
		want, err := template.Render("app_{slug}", template.FromSlug(slug.Slug{Value: sl}))
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, d := range *dropped {
			if d == want {
				n++
			}
		}
		if n != 2 {
			t.Errorf("target %q dropped %d times, want 2 (mysql + postgres); drops: %v", want, n, *dropped)
		}
	}
}

// TestTeardownSlugsConnReuseFailureIsolation pins that one slug's
// connect/drop failure skips that slug without stranding the rest.
func TestTeardownSlugsConnReuseFailureIsolation(t *testing.T) {
	// Unconfigured engines (no connections block entry) are skipped,
	// not failed: redis has no connection here, so only the mysql drop
	// should happen per slug.
	cfg := &config.Config{
		Databases: []config.DatabaseConfig{
			{Engine: "mysql", NameTemplate: "app_{slug}"},
			{Engine: "redis"},
		},
		Connections: config.ConnectionsConfig{Mysql: &config.MysqlConn{Host: "127.0.0.1", Port: 3306}},
	}
	dropped := &[]string{}
	fakeConnect := func(context.Context, *config.Config, engine.Family, string) (engineconn.Conn, bool, error) {
		return countingConn{dropped: dropped}, true, nil
	}

	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	purged := teardownSlugsVia(context.Background(), cfg, []string{"app_a", "app_b"}, 1, 0, st, fakeConnect)
	if purged != 2 {
		t.Errorf("unconfigured redis must be skipped, not fail: purged = %d, want 2", purged)
	}
	if len(*dropped) != 2 {
		t.Errorf("drops = %d, want 2", len(*dropped))
	}
}
