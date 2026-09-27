package prepare

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/db/engineconn"
	"github.com/stubbedev/treeman/internal/engine"
	"github.com/stubbedev/treeman/internal/slug"
	"github.com/stubbedev/treeman/internal/store"
	"github.com/stubbedev/treeman/internal/template"
)

// fakeConn is a registry-driven stub: DropMatching counts calls so the
// test can prove recoverTestClone went through the registered factory
// rather than a per-engine body.
type fakeConn struct {
	mu        sync.Mutex
	dropped   []string
	closeErr  error
	closeN    int
	dropCalls int
}

func (f *fakeConn) EngineVersion(context.Context) (string, error) { return "fake-1.0", nil }
func (f *fakeConn) Ping(context.Context) error                    { return nil }
func (f *fakeConn) Close() error                                  { f.closeN++; return f.closeErr }
func (f *fakeConn) ListMatching(context.Context, string) ([]string, error) {
	return nil, errors.ErrUnsupported
}
func (f *fakeConn) Drop(context.Context, string) error { return errors.ErrUnsupported }
func (f *fakeConn) Exists(context.Context, string) (bool, error) {
	return false, errors.ErrUnsupported
}
func (f *fakeConn) SizeKB(context.Context, string) int64       { return 0 }
func (f *fakeConn) DropSnapshot(context.Context, string) error { return nil }

func (f *fakeConn) DropMatching(_ context.Context, name string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropCalls++
	f.dropped = append(f.dropped, name)
	return len(f.dropped), nil
}

// TestRecoverTestCloneRegistryDriven pins the #47 acceptance criteria:
// recoverTestClone has no per-engine switch — a family whose factory is
// registered in the engineconn registry gets recovery for free, and an
// unconfigured family is a silent no-op.
func TestRecoverTestCloneRegistryDriven(t *testing.T) {
	fake := &fakeConn{}
	engineconn.Register(engine.FamilyMySQL, engineconn.DriverFactory{
		Configured: func(cfg *config.Config, name string) bool { return cfg.Connections.Mysql != nil },
		Connect: func(context.Context, *config.Config, string) (engineconn.Conn, bool, error) {
			return fake, true, nil
		},
	})
	t.Cleanup(func() {
		// The production list re-registers on the next init is NOT a
		// thing — restore by re-registering a factory that behaves like
		// unconfigured so later tests in the package don't dial the
		// fake (init order makes re-running the real populate
		// impossible; tests in this package must tolerate this).
		engineconn.Register(engine.FamilyMySQL, engineconn.DriverFactory{
			Configured: func(*config.Config, string) bool { return false },
			Connect: func(context.Context, *config.Config, string) (engineconn.Conn, bool, error) {
				return nil, false, nil
			},
		})
	})

	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	cfg := &config.Config{
		Connections: config.ConnectionsConfig{Mysql: &config.MysqlConn{Host: "127.0.0.1", Port: 3306}},
	}
	d := config.DatabaseConfig{Engine: "mysql", NameTemplate: "app_{slug}"}
	tplCtx := template.FromSlug(slug.Slug{Value: "wt_demo", Source: slug.SourceTicket})

	if err := recoverTestClone(context.Background(), cfg, d, tplCtx, 0, 0, st); err != nil {
		t.Fatalf("recoverTestClone: %v", err)
	}
	fake.mu.Lock()
	calls, names := fake.dropCalls, append([]string(nil), fake.dropped...)
	closes := fake.closeN
	fake.mu.Unlock()
	if calls != 1 || len(names) != 1 || names[0] != "app_wt_demo" {
		t.Fatalf("fake DropMatching calls=%d names=%v, want 1 drop of app_wt_demo", calls, names)
	}
	if closes != 1 {
		t.Errorf("conn.Close called %d times, want 1", closes)
	}

	// The recovery event names the engine + dropped count.
	rows, err := st.QueryEvents(context.Background(), store.EventFilter{
		EventTypes: []string{store.EvtWorktreeRecoverDrop}, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Message, "mysql: dropped app_wt_demo*") {
		t.Fatalf("recovery event = %+v, want one mysql drop line", rows)
	}

	// Unconfigured family → silent no-op, no dial.
	cfgUnwired := &config.Config{}
	if err := recoverTestClone(context.Background(), cfgUnwired, d, tplCtx, 0, 0, st); err != nil {
		t.Errorf("unconfigured recovery should be a no-op, got %v", err)
	}
}
