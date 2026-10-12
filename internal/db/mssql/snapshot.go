package mssql

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/stubbedev/treeman/internal/db/ident"
)

// backupFile describes one file inside a backup set, as
// `RESTORE FILELISTONLY` reports it. Every RESTORE must name each of
// them in a MOVE clause, or the restore lands on the paths the source
// database occupies — which are taken.
type backupFile struct {
	logical string
	isLog   bool
}

// SnapshotCreate captures `source` as the template `template`.
//
// Two statements, because SQL Server has no `CREATE DATABASE … TEMPLATE`:
// BACKUP source to the template's `.bak`, then RESTORE that file as the
// template database. The second half is what keeps the template a real
// database — treeman's cache-hit probe is `DatabaseExists(template)`,
// the same for every engine — and it is not wasted work either: the
// `.bak` it leaves behind is what every clone restores from, so a
// template is captured once and hydrated N times.
//
// `source` is read-only here, and unlike the postgres driver nothing is
// fenced: BACKUP is an online, transactionally consistent read, so
// there is no window to close and no unfence that can leak if the
// context is cancelled mid-capture.
func (d *Driver) SnapshotCreate(ctx context.Context, source, template string) error {
	qsource, err := ident.QuoteMSSQL(source)
	if err != nil {
		return err
	}
	if err := ident.ValidateMSSQL(template); err != nil {
		return err
	}
	if err := d.backupTo(ctx, qsource, d.backupPath(template)); err != nil {
		return err
	}
	return d.restoreFromBackup(ctx, d.backupPath(template), template)
}

// SnapshotRestore hydrates `target` from the template `template` — one
// RESTORE from the `.bak` SnapshotCreate left behind. The template
// survives: a prepare restores it concurrently into the worktree's own
// database and every per-test clone.
//
// When the `.bak` is gone but the template database is still there
// (someone cleaned the backup directory, or the template was captured
// by an older treeman), it is re-derived from the template rather than
// failing — the alternative is a cold rebuild of a template that is
// demonstrably still good.
func (d *Driver) SnapshotRestore(ctx context.Context, template, target string) error {
	qtemplate, err := ident.QuoteMSSQL(template)
	if err != nil {
		return err
	}
	if err := ident.ValidateMSSQL(target); err != nil {
		return err
	}
	bak := d.backupPath(template)
	present, err := d.serverFileExists(ctx, bak)
	if err != nil {
		return err
	}
	if !present {
		exists, err := d.DatabaseExists(ctx, template)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("mssql snapshot %q: neither the template database nor %s exists", template, bak)
		}
		if err := d.backupTo(ctx, qtemplate, bak); err != nil {
			return err
		}
	}
	return d.restoreFromBackup(ctx, bak, target)
}

// DropSnapshot drops exactly the named database and removes the `.bak`
// that belongs to it. Idempotent, and callable on a name that was never
// a template — the spare-claim path uses it to clear a target before a
// claim, where there is no backup file to find.
func (d *Driver) DropSnapshot(ctx context.Context, template string) error {
	if err := d.DropDatabase(ctx, template); err != nil {
		return err
	}
	return d.deleteServerFile(ctx, d.backupPath(template))
}

// SnapshotBackupPath is where the template's backup set lives ON THE
// SERVER. Exported because when a RESTORE fails there is nothing on
// this machine to look at, and the path is the first thing a human
// needs — `docker exec <ctr> ls -l` on it answers most of the
// questions a restore error raises.
func (d *Driver) SnapshotBackupPath(template string) string {
	return d.backupPath(template)
}

// backupTo writes a fresh, self-contained backup set over whatever is
// at `path`.
//
//   - FORMAT + INIT: one backup set per file, never appended to, so the
//     file's size is the database's size and `WITH FILE = 1` is always
//     the right set. An appending backup device grows without bound and
//     makes every restore guess at a set number.
//   - COPY_ONLY: leaves the source's differential base alone. Nothing
//     treeman clones is on a backup schedule, but a database that is
//     (a `dump:` source pointed at something real) must not have its
//     chain disturbed by a provisioning run.
//   - No COMPRESSION: unsupported on Express, and it buys little at the
//     sizes a test template runs to.
func (d *Driver) backupTo(ctx context.Context, quotedDB, path string) error {
	const q = "BACKUP DATABASE %s TO DISK = @p1 WITH FORMAT, INIT, COPY_ONLY, SKIP"
	if err := drain(ctx, d.DB, fmt.Sprintf(q, quotedDB), path); err != nil {
		return fmt.Errorf("BACKUP DATABASE %s TO %s: %w", quotedDB, path, err)
	}
	return nil
}

// restoreFromBackup restores the backup set at `path` as the database
// `name`, which is dropped first so the restore is an overwrite by
// exact name (never a prefix match — a target name is routinely a
// prefix of a sibling worktree's databases).
//
// Every file in the set is moved to a path carrying a fresh random
// token. Naming them after `name` would be the obvious choice and is
// wrong: `RenameDatabase` (the spare claim) renames the database but
// not its files, so a claimed spare keeps files named for the spare,
// and the next restore of that spare's own name would collide with
// files still in use.
func (d *Driver) restoreFromBackup(ctx context.Context, path, name string) error {
	qname, err := ident.QuoteMSSQL(name)
	if err != nil {
		return err
	}
	files, err := d.backupFileList(ctx, path)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("mssql restore %s: backup set %s lists no files", qname, path)
	}
	if err := d.DropDatabase(ctx, name); err != nil {
		return err
	}
	token, err := fileToken()
	if err != nil {
		return err
	}
	var q strings.Builder
	q.WriteString("RESTORE DATABASE " + qname + " FROM DISK = @p1 WITH RECOVERY, REPLACE")
	args := []any{path}
	for i, f := range files {
		dir, ext := d.dataDir, ".mdf"
		if f.isLog {
			dir, ext = d.logDir, ".ldf"
		}
		if i > 0 {
			// Secondary data files cannot all be `.mdf`; the suffix
			// only has to be unique and stable within one restore.
			ext = fmt.Sprintf("_%d%s", i, ext)
		}
		args = append(args, f.logical, d.join(dir, name+"_"+token+ext))
		fmt.Fprintf(&q, ", MOVE @p%d TO @p%d", len(args)-1, len(args))
	}
	if err := drain(ctx, d.DB, q.String(), args...); err != nil {
		return fmt.Errorf("RESTORE DATABASE %s FROM %s: %w", qname, path, err)
	}
	// The restored database inherits the source's recovery model, which
	// is SIMPLE for anything EnsureDB made — but a `dump:` source or a
	// hand-built database need not be, and a FULL-recovery clone grows
	// a log nobody will ever back up.
	return d.setSimpleRecovery(ctx, name)
}

// backupFileList reads the logical file names out of a backup set.
// Columns are located by name rather than by position: FILELISTONLY's
// result set has grown with nearly every release (22 columns on 2025),
// and only three of them have ever mattered here.
func (d *Driver) backupFileList(ctx context.Context, path string) ([]backupFile, error) {
	rows, err := d.DB.QueryContext(ctx, "RESTORE FILELISTONLY FROM DISK = @p1", path)
	if err != nil {
		return nil, fmt.Errorf("RESTORE FILELISTONLY FROM %s: %w", path, err)
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	iLogical, iType := -1, -1
	for i, c := range cols {
		switch c {
		case "LogicalName":
			iLogical = i
		case "Type":
			iType = i
		}
	}
	if iLogical < 0 || iType < 0 {
		return nil, fmt.Errorf("RESTORE FILELISTONLY FROM %s: no LogicalName/Type column in %v", path, cols)
	}
	cells := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range cells {
		ptrs[i] = &cells[i]
	}
	var out []backupFile
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		// 'D' data, 'L' log, 'F'/'S' FILESTREAM and full-text — all of
		// which restore as data files.
		out = append(out, backupFile{
			logical: cellString(cells[iLogical]),
			isLog:   cellString(cells[iType]) == "L",
		})
	}
	return out, rows.Err()
}

// serverFileExists asks the SERVER whether a path is a readable file.
// os.Stat would answer about treeman's own filesystem, which for a
// containerised server is a different machine.
func (d *Driver) serverFileExists(ctx context.Context, path string) (bool, error) {
	var isFile, isDir, parentExists sql.NullInt64
	err := d.DB.QueryRowContext(ctx,
		`DECLARE @r TABLE (f int, d int, p int);
		 INSERT INTO @r EXEC master.sys.xp_fileexist @p1;
		 SELECT f, d, p FROM @r`, path).Scan(&isFile, &isDir, &parentExists)
	if err != nil {
		return false, fmt.Errorf("xp_fileexist %s: %w", path, err)
	}
	return isFile.Int64 == 1, nil
}

// deleteServerFile removes a file from the server's filesystem.
// Best-effort by design: sys.xp_delete_files arrived in SQL Server 2019
// and needs sysadmin, so on an older or less-privileged server a stale
// `.bak` is left behind rather than failing a teardown over it. The
// leak is bounded — one file per template name, overwritten in place by
// the next capture of that template.
func (d *Driver) deleteServerFile(ctx context.Context, path string) error {
	present, err := d.serverFileExists(ctx, path)
	if err != nil || !present {
		return nil //nolint:nilerr // a probe failure is not a teardown failure
	}
	_, _ = d.DB.ExecContext(ctx, "EXEC master.sys.xp_delete_files @p1", path)
	return nil
}

// fileToken is the random component of a restored database's file
// names. Eight hex characters: these live in one directory alongside
// at most a few hundred siblings, so collision resistance matters far
// less than the names staying readable in a `ls`.
func fileToken() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("mssql file token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func cellString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	}
	return ""
}

// drain runs a statement that reports progress as result sets rather
// than as a row count — BACKUP and RESTORE both do ("Processed N pages
// …"). ExecContext discards those sets, and with them any error the
// server raises after the first one, so the statement is issued as a
// query and every set is walked to completion.
func drain(ctx context.Context, db *sql.DB, q string, args ...any) error {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for {
		for rows.Next() {
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if !rows.NextResultSet() {
			break
		}
	}
	return rows.Err()
}
