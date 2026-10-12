// Package mssql is the treeman Microsoft SQL Server driver. Uses
// microsoft/go-mssqldb (the `sqlserver` database/sql driver) over TDS.
//
// WHAT MAKES THIS DRIVER DIFFERENT FROM postgres/mysql: SQL Server has
// no `CREATE DATABASE … TEMPLATE` and no cross-database file clone a
// client can drive. `DBCC CLONEDATABASE` copies schema and statistics
// but no rows; a database snapshot (`CREATE DATABASE … AS SNAPSHOT OF`)
// is read-only. The only client-drivable copy is BACKUP + RESTORE, so
// a template here is a pair: the template DATABASE (which is what
// treeman's `DatabaseExists` cache-hit probe asks about, same as every
// other engine) and a `.bak` file beside it that every clone restores
// from. One backup per template, one restore per clone.
//
// All of it runs over TDS, including the file paths: `TO DISK`, `FROM
// DISK` and `MOVE … TO` all accept variables, so paths are bound as
// parameters and only database names are interpolated — through
// ident.QuoteMSSQL, whose charset is the same strict [A-Za-z0-9_] the
// other engines use.
package mssql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	// Register the go-mssqldb database/sql driver under "sqlserver".
	_ "github.com/microsoft/go-mssqldb"

	"golang.org/x/sync/errgroup"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/db/adapter"
	"github.com/stubbedev/treeman/internal/db/containerip"
	"github.com/stubbedev/treeman/internal/db/ident"
)

// DefaultPort is the SQL Server default TDS port.
const DefaultPort uint16 = 1433

// Driver wraps a pool against `master` so database-level DDL works.
type Driver struct {
	DB  *sql.DB
	cfg config.MssqlConn

	// Server-side directories, resolved once on Connect from config or
	// SERVERPROPERTY. They are paths on the SERVER, not on the machine
	// treeman runs on: with SQL Server in a container the two have no
	// relation, which is why neither is ever touched with os.*.
	dataDir   string
	logDir    string
	backupDir string
	sep       string // path separator the server uses: "/" or "\"
}

// Connect opens a pool against `master`, from which CREATE / DROP /
// ALTER DATABASE and BACKUP / RESTORE are all issued. Container and
// compose refs are resolved exactly as in the postgres driver; there is
// no unix-socket mode (SQL Server speaks TDS over TCP only).
func Connect(ctx context.Context, cfg config.MssqlConn) (*Driver, error) {
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	opts := containerip.Opts{
		Container:      cfg.Container,
		ComposeService: cfg.ComposeService,
		ComposeProject: cfg.ComposeProject,
		Engine:         cfg.ContainerEngine,
		Network:        cfg.Network,
		InternalPort:   cfg.Port,
	}
	if err := adapter.ResolveAndProbe(ctx, "mssql", opts, &cfg.Host, &cfg.Port); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlserver", dsn(cfg, "master"))
	if err != nil {
		return nil, fmt.Errorf("open mssql: %w", err)
	}
	adapter.ConfigurePool(db, int(cfg.PoolMax))
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping mssql: %w", err)
	}
	d := &Driver{DB: db, cfg: cfg}
	if err := d.resolveServerDirs(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return d, nil
}

// dsn renders a `sqlserver://` URL for one database. encrypt=disable
// because every supported target is a local or CI server reached over a
// private network — the same posture the other drivers take with
// sslmode=disable — and SQL Server 2022+ otherwise refuses a
// self-signed certificate it generated itself.
func dsn(cfg config.MssqlConn, database string) string {
	u := url.URL{
		Scheme: "sqlserver",
		User:   url.UserPassword(cfg.User, cfg.Password),
		Host:   net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))),
	}
	q := url.Values{}
	q.Set("database", database)
	q.Set("encrypt", "disable")
	// A connect that hangs past the caller's patience is worse than one
	// that fails: every call site wraps this in a context, and the
	// driver's own default is 30s.
	q.Set("dial timeout", "10")
	u.RawQuery = q.Encode()
	return u.String()
}

// resolveServerDirs fixes the three server-side directories this driver
// writes into, from SERVERPROPERTY.
func (d *Driver) resolveServerDirs(ctx context.Context) error {
	var data, logp, backup sql.NullString
	err := d.DB.QueryRowContext(ctx, `
		SELECT CONVERT(nvarchar(4000), SERVERPROPERTY('InstanceDefaultDataPath')),
		       CONVERT(nvarchar(4000), SERVERPROPERTY('InstanceDefaultLogPath')),
		       CONVERT(nvarchar(4000), SERVERPROPERTY('InstanceDefaultBackupPath'))`,
	).Scan(&data, &logp, &backup)
	if err != nil {
		return fmt.Errorf("read mssql default paths: %w", err)
	}
	return d.applyServerDirs(data.String, logp.String, backup.String)
}

// applyServerDirs resolves the directories against the config. Config
// wins; otherwise the server's own answers do, which are right for a
// stock container (`/var/opt/mssql/data/`) and for a Windows install
// alike. `data_dir` covers the log directory too when set, because a
// caller relocating the data files almost never means to leave the
// logs behind — and the separator is read off whichever path came back
// rather than assumed, because the server's OS is not ours.
func (d *Driver) applyServerDirs(data, logp, backup string) error {
	d.sep = "/"
	for _, p := range []string{data, logp, backup} {
		if strings.Contains(p, `\`) {
			d.sep = `\`
			break
		}
	}
	d.dataDir = trimSep(firstNonEmpty(d.cfg.DataDir, data), d.sep)
	d.logDir = trimSep(firstNonEmpty(d.cfg.DataDir, logp, data), d.sep)
	d.backupDir = trimSep(firstNonEmpty(d.cfg.BackupDir, backup, data), d.sep)
	if d.dataDir == "" || d.backupDir == "" {
		return errors.New("mssql: server reported no default data/backup path — set `data_dir` and `backup_dir` on the connection")
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func trimSep(p, sep string) string { return strings.TrimRight(p, sep) }

func (d *Driver) join(dir, file string) string { return dir + d.sep + file }

// backupPath is where a template's `.bak` lives. One file per template
// name, overwritten WITH INIT, so the set of files on disk is bounded
// by the set of live templates.
func (d *Driver) backupPath(template string) string {
	return d.join(d.backupDir, template+".bak")
}

func (d *Driver) Close() error { return d.DB.Close() }

// OpenScoped returns a fresh pool scoped to `dbName`. SQL Server does
// have `USE`, but a pooled connection's current database is not a
// property a caller can rely on, so each target database gets its own
// pool — same contract as the postgres driver. The caller MUST Close it.
func (d *Driver) OpenScoped(ctx context.Context, dbName string) (*sql.DB, error) {
	if err := ident.ValidateMSSQL(dbName); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlserver", dsn(d.cfg, dbName))
	if err != nil {
		return nil, fmt.Errorf("open mssql %s: %w", dbName, err)
	}
	adapter.ConfigurePool(db, int(d.cfg.PoolMax))
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping mssql %s: %w", dbName, err)
	}
	return db, nil
}

func (d *Driver) EngineVersion(ctx context.Context) (string, error) {
	var v string
	if err := d.DB.QueryRowContext(ctx, "SELECT @@VERSION").Scan(&v); err != nil {
		return "", err
	}
	return v, nil
}

// MaxConnections returns the server's user-connection cap. The
// configured value is 0 when it means "unlimited", in which case the
// engine's own ceiling (32767) is the honest answer for the fanout
// auto-tuner — it sizes a goroutine pool against a connection budget,
// and 0 would starve it.
func (d *Driver) MaxConnections(ctx context.Context) (int, error) {
	var n int
	if err := d.DB.QueryRowContext(ctx, "SELECT @@MAX_CONNECTIONS").Scan(&n); err != nil {
		return 0, err
	}
	if n <= 0 {
		n = 32767
	}
	return n, nil
}

// WriteWatermark declines to answer. The caller skips work when the
// token is unchanged between two calls, so a token that can miss a
// write is worse than none: SQL Server's per-database write counters
// (`sys.dm_db_index_usage_stats.user_updates`) are per INDEX, so writes
// to a heap with no index move nothing, and the rows are dropped when a
// database goes offline or the instance restarts. "" means "no sound
// token", which makes the caller do the work rather than skip it.
func (d *Driver) WriteWatermark(context.Context, string) (string, error) { return "", nil }

// EnsureDB creates `name` if it is absent. The collation comes from the
// connection when set: a clone that sorts and compares differently from
// the app's real server is a subtle, expensive difference to debug, and
// SQL Server's install default (SQL_Latin1_General_CP1_CI_AS) is rarely
// what a non-English application uses.
func (d *Driver) EnsureDB(ctx context.Context, name string) error {
	qname, err := ident.QuoteMSSQL(name)
	if err != nil {
		return err
	}
	exists, err := d.DatabaseExists(ctx, name)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	collate, err := d.createCollateClause()
	if err != nil {
		return err
	}
	//nolint:gosec // qname via ident.QuoteMSSQL, collation via the same validator
	stmt := "CREATE DATABASE " + qname + collate
	if _, err := d.DB.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("CREATE DATABASE %s: %w", qname, err)
	}
	// SIMPLE recovery on every database treeman owns: these are
	// disposable, nobody restores them to a point in time, and FULL
	// recovery grows a log file that is never backed up and so never
	// truncates.
	return d.setSimpleRecovery(ctx, name)
}

// createCollateClause is the ` COLLATE <name>` suffix for CREATE
// DATABASE, or "" to take the server's default. The collation runs
// through the identifier validator even though it is not an
// identifier: SQL Server collation names are exactly [A-Za-z0-9_],
// and the value is interpolated into DDL.
func (d *Driver) createCollateClause() (string, error) {
	c := d.cfg.Collation
	if c == "" {
		return "", nil
	}
	if err := ident.ValidateMSSQL(c); err != nil {
		return "", fmt.Errorf("mssql collation %q: %w", c, err)
	}
	return " COLLATE " + c, nil
}

func (d *Driver) setSimpleRecovery(ctx context.Context, name string) error {
	qname, err := ident.QuoteMSSQL(name)
	if err != nil {
		return err
	}
	if _, err := d.DB.ExecContext(ctx, "ALTER DATABASE "+qname+" SET RECOVERY SIMPLE"); err != nil {
		return fmt.Errorf("ALTER DATABASE %s SET RECOVERY SIMPLE: %w", qname, err)
	}
	return nil
}

// DropMatching drops every database whose name starts with `prefix` —
// the worktree's own database plus its per-test clones. Four at a time:
// each DROP takes an exclusive lock on the database being dropped, not
// a server-wide one, so they genuinely overlap, and the limit keeps the
// connection count modest.
func (d *Driver) DropMatching(ctx context.Context, prefix string) ([]string, error) {
	matched, err := d.ListMatching(ctx, prefix)
	if err != nil {
		return nil, err
	}
	g, gctx := errgroup.WithContext(ctx)
	limit := min(4, max(len(matched), 1))
	g.SetLimit(limit)
	for _, n := range matched {
		g.Go(func() error { return d.DropDatabase(gctx, n) })
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return matched, nil
}

// ListMatching returns every database named `prefix…`, ordered. LIKE
// metacharacters in the prefix are escaped, because every templated
// name carries a literal `_` and an unescaped one is a single-character
// wildcard — which would make a sibling worktree's database a match for
// this one's prefix. Same guard as the mysql and postgres drivers.
func (d *Driver) ListMatching(ctx context.Context, prefix string) ([]string, error) {
	like := strings.NewReplacer(`\`, `\\`, `_`, `\_`, `%`, `\%`, `[`, `\[`).Replace(prefix) + "%"
	rows, err := d.DB.QueryContext(ctx,
		`SELECT name FROM sys.databases
		  WHERE name LIKE @p1 ESCAPE '\' AND database_id > 4
		  ORDER BY name`, like)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DatabaseExists reports whether the named database is present.
func (d *Driver) DatabaseExists(ctx context.Context, name string) (bool, error) {
	var n int
	err := d.DB.QueryRowContext(ctx, "SELECT 1 FROM sys.databases WHERE name = @p1", name).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

// DropDatabase drops EXACTLY the named database — never a prefix match.
// branch_scoped teardown must use this rather than DropMatching: the
// namespace there is often a bare application database name that is a
// prefix of every linked worktree's `name_<slug>`, so a prefix drop
// would take the siblings with it. Idempotent.
//
// SINGLE_USER WITH ROLLBACK IMMEDIATE first, because unlike Postgres
// there is no pg_terminate_backend to call and DROP DATABASE fails
// outright while anybody is connected — including a pool that a jest
// worker left idle.
func (d *Driver) DropDatabase(ctx context.Context, name string) error {
	qname, err := ident.QuoteMSSQL(name)
	if err != nil {
		return err
	}
	exists, err := d.DatabaseExists(ctx, name)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	// Best-effort: a database already in single-user mode, or one with
	// no connections at all, makes this a no-op either way, and the
	// DROP below is the statement whose failure matters.
	_, _ = d.DB.ExecContext(ctx, "ALTER DATABASE "+qname+" SET SINGLE_USER WITH ROLLBACK IMMEDIATE")
	if _, err := d.DB.ExecContext(ctx, "DROP DATABASE IF EXISTS "+qname); err != nil {
		return fmt.Errorf("DROP DATABASE %s: %w", qname, err)
	}
	return nil
}

// RenameDatabase renames `from` to `to` — a catalog-only operation,
// constant-time whatever the database holds. It fails, never
// overwrites, when `to` is already taken, which is the atomicity the
// spare-claim path leans on: of two racing claimers exactly one wins.
//
// It does NOT rename the files on disk, which is why every restore in
// this driver names its files after a fresh random token rather than
// after the database: a spare renamed to `…_test_w1` keeps the spare's
// `.mdf`, and a later restore of the spare's own name would otherwise
// collide with a file that is still in use.
func (d *Driver) RenameDatabase(ctx context.Context, from, to string) error {
	qfrom, err := ident.QuoteMSSQL(from)
	if err != nil {
		return err
	}
	if err := ident.ValidateMSSQL(to); err != nil {
		return err
	}
	// MODIFY NAME is refused while anyone else is connected. The spare
	// being claimed is idle by construction, but a doctor probe or a
	// leftover pool is enough to lose the race, so the lock is taken
	// explicitly and released whatever happens.
	if _, err := d.DB.ExecContext(ctx, "ALTER DATABASE "+qfrom+" SET SINGLE_USER WITH ROLLBACK IMMEDIATE"); err != nil {
		return fmt.Errorf("ALTER DATABASE %s SET SINGLE_USER: %w", qfrom, err)
	}
	_, err = d.DB.ExecContext(ctx, "ALTER DATABASE "+qfrom+" MODIFY NAME = ["+to+"]")
	if err != nil {
		// Hand the database back before returning, or a lost race
		// leaves a single-user spare nothing can ever claim.
		d.restoreMultiUser(ctx, from)
		return fmt.Errorf("ALTER DATABASE %s MODIFY NAME: %w", qfrom, err)
	}
	d.restoreMultiUser(ctx, to)
	return nil
}

// restoreMultiUser puts a database back into MULTI_USER on a fresh,
// short-lived context, so it runs even when the caller's ctx is already
// cancelled — otherwise a cancellation mid-rename strands a database
// that admits exactly one session forever.
func (d *Driver) restoreMultiUser(ctx context.Context, name string) {
	qname, err := ident.QuoteMSSQL(name)
	if err != nil {
		return
	}
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, _ = d.DB.ExecContext(bg, "ALTER DATABASE "+qname+" SET MULTI_USER")
}
