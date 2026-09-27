// Package filedb implements the file-backed engine family (engine
// "sqlite" / "duckdb"): a "database" is a regular file, the per-worktree
// namespace is a rendered file path, and every snapshot operation is a
// file copy. There is no server: the driver's only stateful client — a
// database/sql handle through the vendored pure-Go sqlite driver —
// exists to apply `.sql` seed dumps and probe the engine version of a
// sqlite file. DuckDB files carry no SQL executable from Go, so they
// seed by copying a base file and report no engine version.
package filedb

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	// side-effect import: registers the pure-Go "sqlite" database/sql
	// driver used by ApplySQLDump + EngineVersion (already vendored for
	// the store).
	_ "modernc.org/sqlite"

	"github.com/stubbedev/treeman/internal/config"
)

// sidecarSuffixes lists the companion files sqlite and duckdb create
// around a main database file. A copy or remove that ignores them
// leaves half-a-database behind (uncommitted WAL frames, a stale shm
// index), so every database-wide operation treats main + sidecars as
// one unit.
var sidecarSuffixes = []string{"-wal", "-shm", ".wal", ".wal2", ".tmp"}

// SidecarPaths returns the sidecar paths that may accompany `path`.
// Presence is not guaranteed — callers treat these as best-effort.
func SidecarPaths(path string) []string {
	out := make([]string, 0, len(sidecarSuffixes))
	for _, s := range sidecarSuffixes {
		out = append(out, path+s)
	}
	return out
}

// Exists reports whether `path` is a live database file (the sidecars
// alone don't count).
func Exists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.Mode().IsRegular(), nil
}

// SizeKB is the main file's size in KiB (sidecars excluded, matching
// how the networked engines report data-only sizes).
func SizeKB(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size() / 1024
}

// CopyDatabase copies the main file plus any sidecars present. The
// copy goes through the reflink path (FICLONE) so on btrfs/XFS a
// whole template materialises as copy-on-write metadata instead of a
// byte copy.
func CopyDatabase(src, dst string) error {
	if err := copyFileReflink(src, dst); err != nil {
		return fmt.Errorf("copy %s → %s: %w", src, dst, err)
	}
	for _, side := range SidecarPaths(src) {
		if _, err := os.Stat(side); err != nil {
			continue
		}
		if err := copyFileReflink(side, dst+strings.TrimPrefix(side, src)); err != nil {
			return fmt.Errorf("copy sidecar %s: %w", side, err)
		}
	}
	return nil
}

// copyFileReflink copies one regular file, attempting a reflink first
// and falling back to a streamed copy when the filesystem doesn't
// support it.
func copyFileReflink(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create parent dir for %s: %w", dst, err)
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	sf, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = sf.Close() }()
	df, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if reflink(df, sf) == nil {
		return df.Close()
	}
	if _, err := io.Copy(df, sf); err != nil {
		_ = df.Close()
		return err
	}
	return df.Close()
}

// RemoveDatabase deletes the main file and its sidecars. Missing files
// are not an error; returns the removed paths.
func RemoveDatabase(path string) ([]string, error) {
	var removed []string
	for _, p := range append([]string{path}, SidecarPaths(path)...) {
		if err := os.Remove(p); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, fmt.Errorf("remove %s: %w", p, err)
		}
		removed = append(removed, p)
	}
	return removed, nil
}

// CloneSiblings returns the clone-family files in `dir`: every file
// whose stem extends `base`'s stem with the `_` separator (the
// `_test_{n}` fan-out convention renders as `app.db` →
// `app_test_1.db`). Sidecars share the stem without the `_` and are
// deliberately not matches — they ride along with their own main file
// via RemoveDatabase.
func CloneSiblings(dir, base string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	prefix := stem + "_"
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, prefix) {
			out = append(out, filepath.Join(dir, name))
		}
	}
	return out, nil
}

// ApplySQLDump executes a .sql dump file against the sqlite database
// at `dbPath` (creating it when missing). The whole script runs in one
// Exec — the modernc driver streams multi-statement scripts natively.
func ApplySQLDump(ctx context.Context, dbPath, dumpPath string) error {
	body, err := os.ReadFile(dumpPath)
	if err != nil {
		return fmt.Errorf("read dump %s: %w", dumpPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return fmt.Errorf("create parent dir for %s: %w", dbPath, err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", dbPath, err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, string(body)); err != nil {
		return fmt.Errorf("apply dump %s to %s: %w", dumpPath, dbPath, err)
	}
	return nil
}

// EngineVersion returns the sqlite_version() string of the database
// file, opened read-only. Files that aren't sqlite (a duckdb database,
// arbitrary binary) report "" — callers treat that as "unknown", the
// same as the S3 family.
func EngineVersion(ctx context.Context, path string) (string, error) {
	if !strings.HasSuffix(strings.ToLower(path), ".db") &&
		!strings.HasSuffix(strings.ToLower(path), ".sqlite") &&
		!strings.HasSuffix(strings.ToLower(path), ".sqlite3") {
		return "", nil
	}
	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return "", nil
	}
	defer func() { _ = db.Close() }()
	var v string
	if err := db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&v); err != nil {
		return "", nil
	}
	return v, nil
}

// SourcePath joins a rendered name_template onto the family's base
// directory — connections.sqlite.base_dir when set (absolute), else
// the worktree root — and refuses paths that escape it. Both prepare
// and teardown resolve the same rendered name through this, so the
// file that gets built is the file that gets reaped.
func SourcePath(baseDir, worktreePath, rendered string) (string, error) {
	base := worktreePath
	if baseDir != "" {
		if !filepath.IsAbs(baseDir) {
			return "", fmt.Errorf("sqlite: base_dir %q must be absolute", baseDir)
		}
		base = baseDir
	}
	p := filepath.Join(base, rendered)
	if !strings.HasPrefix(p, base+string(filepath.Separator)) {
		return "", fmt.Errorf("sqlite: rendered path %q escapes %q", p, base)
	}
	return p, nil
}

// SourcePathFrom is SourcePath with the base pulled from the config's
// connections.sqlite block — the one-liner every render site (prepare,
// teardown, recovery) resolves the family's paths through.
func SourcePathFrom(cfg *config.Config, worktreePath, rendered string) (string, error) {
	return SourcePathFor(cfg, "", worktreePath, rendered)
}

// SourcePathFor is SourcePathFrom for a NAMED connections.sqlite block:
// `connection: <name>` on the database entry picks which base_dir the
// per-worktree files live under (#44).
func SourcePathFor(cfg *config.Config, connName, worktreePath, rendered string) (string, error) {
	var baseDir string
	if cfg != nil && cfg.Connections.Sqlite != nil {
		if sc, err := cfg.Connections.ResolveSqlite(connName); err == nil && sc != nil {
			baseDir = sc.BaseDir
		}
	}
	return SourcePath(baseDir, worktreePath, rendered)
}

// TemplatePath is the sibling file a template is cached under: the
// fingerprint-derived name of the networked engines, colocated with
// the rendered source file so restores stay on the same filesystem
// (the reflink path needs it). Extends the source's extension so
// external tooling recognises the file type.
func TemplatePath(sourcePath, fingerprint string) string {
	ext := filepath.Ext(sourcePath)
	return filepath.Join(filepath.Dir(sourcePath), "_tm_"+fingerprint[:16]+ext)
}
