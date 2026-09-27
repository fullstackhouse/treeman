// fileConn — the Conn view over the file-backed engine family
// (sqlite / duckdb). "Names" are rendered file paths (the per-worktree
// database file); every operation is a filesystem action plus the
// optional sqlite version probe. The connection holds no handle, so
// Close is a no-op (like esConn/s3Conn).
//
// Every name must be ABSOLUTE. Prepare / teardown / recovery resolve
// rendered templates onto the family base (worktree root or
// connections.sqlite.base_dir) before they reach this view; a bare
// name would resolve against the daemon's CWD, which is never what a
// caller means and would make a drop a filesystem gamble.
package engineconn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	dbfile "github.com/stubbedev/treeman/internal/db/filedb"
)

// errRelativeName is returned for any non-absolute database path.
var errRelativeName = errors.New(
	"file engine: database path must be absolute (prepare/teardown resolve rendered name_templates onto the family base)")

func absName(name string) (string, error) {
	if !filepath.IsAbs(name) {
		return "", fmt.Errorf("%w: %q", errRelativeName, name)
	}
	return name, nil
}

type fileConn struct{}

func (fileConn) Close() error { return nil }

// EngineVersion probes sqlite_version() on a database file. The
// receiver ignores its `ctx`-bound configured name — the version is a
// property of the file being probed, so this stays "" (unknown) unless
// a caller probes a specific file through the driver directly.
func (fileConn) EngineVersion(context.Context) (string, error) { return "", nil }

func (c fileConn) Exists(_ context.Context, n string) (bool, error) {
	p, err := absName(n)
	if err != nil {
		return false, err
	}
	return dbfile.Exists(p)
}

// DropMatching reaps `n` plus its clone family: the main file, its
// sidecars, and same-dir siblings sharing its stem (the `_test_{n}`
// fan-out convention). Mirrors the name-prefix semantics the networked
// engines apply to database names.
func (c fileConn) DropMatching(_ context.Context, n string) (int, error) {
	p, err := absName(n)
	if err != nil {
		return 0, err
	}
	dropped, err := dbfile.RemoveDatabase(p)
	if err != nil {
		return len(dropped), err
	}
	siblings, err := dbfile.CloneSiblings(filepath.Dir(p), filepath.Base(p))
	if err != nil {
		return len(dropped), err
	}
	for _, s := range siblings {
		removed, err := dbfile.RemoveDatabase(s)
		if err != nil {
			return len(dropped), err
		}
		dropped = append(dropped, removed...)
	}
	return len(dropped), nil
}

// DropSnapshot removes exactly the named template file (+ sidecars).
func (c fileConn) DropSnapshot(_ context.Context, n string) error {
	p, err := absName(n)
	if err != nil {
		return err
	}
	_, err = dbfile.RemoveDatabase(p)
	return err
}

func (c fileConn) ListMatching(_ context.Context, prefix string) ([]string, error) {
	p, err := absName(prefix)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(p)
	// Family match = shared stem (app.db → app.db, app.db-wal,
	// app_test_1.db), the same notion DropMatching reaps. Inspection
	// only — never a drop target by itself.
	stem := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), stem) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out, nil
}

func (c fileConn) SizeKB(_ context.Context, n string) int64 {
	p, err := absName(n)
	if err != nil {
		return 0
	}
	return dbfile.SizeKB(p)
}
