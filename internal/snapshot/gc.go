package snapshot

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/db/engineconn"
	"github.com/stubbedev/treeman/internal/engine"
	"github.com/stubbedev/treeman/internal/store"
)

// EvictExcess drops every cached template DB above
// `cfg.Snapshots.CapPerRepo` for the given repo, ordered
// by LRU (oldest `last_used_at` first). Called as a fire-and-forget
// goroutine after every `RecordSnapshot` so the inline cost is just
// a goroutine spawn — the engine-side DROP DATABASE runs in the
// background while the foreground completes the prepare.
//
// Errors are logged at WARN level and swallowed: a stale template
// row left behind is harmless (the next prepare with the same
// fingerprint will hit it; if the DB was already dropped the
// `DatabaseExists` check in prepare will fall back to a cold build
// and overwrite).
//
// Safe to invoke when the daemon-side periodic sweep is also
// running — the SQLite UPSERT on `RecordSnapshot` is idempotent and
// `DROP DATABASE IF EXISTS` is engine-side idempotent.
func EvictExcess(ctx context.Context, cfg *config.Config, st *store.Store, repoID int64) {
	capPerRepo := cfg.Snapshots.CapPerRepo
	candidates, err := st.ListLRUEvictable(ctx, repoID, capPerRepo)
	if err != nil {
		slog.Warn("snapshot eviction lookup", "repo_id", repoID, "err", err)
		return
	}
	evictCandidates(ctx, cfg, st, candidates, repoID, store.EvtSnapshotsEvictCap, "snapshot eviction",
		func(c store.SnapshotEvictionCandidate) string {
			return fmt.Sprintf("evicted %s (%s)", c.TemplateName, c.Engine)
		})
}

// evictCandidates drops each candidate's engine-side template and SQLite
// row, then writes one eviction event. Shared by the LRU / per-source /
// age sweeps — the only things that vary between them are the candidate
// query (done by the caller), the repo scope, the event type, the log
// prefix, and the message text.
//
// Pinned fingerprints (held by an in-flight prepare) are skipped — the
// sweep is best-effort and the next tick picks them up once the pin
// clears. A per-candidate drop/delete failure is logged and skipped so
// one bad row can't strand the rest; the row survives for a later retry.
//
// The size sweep (running-total accounting) and PurgeRepo (no pin check,
// error-collecting) keep their own loops.
// evictCandidates drops each candidate's engine-side template, then
// deletes its row + writes the event. Candidates are processed as a
// batch: one connection per engine family (re-dialing per candidate
// made N-template sweeps pay N x (dial + auth + ping + container
// resolve), #55), with the drops themselves running under a bounded
// errgroup — the store writes stay serial so the SQLite side is never
// contended. Pinned fingerprints are skipped.
func evictCandidates(
	ctx context.Context, cfg *config.Config, st *store.Store,
	cands []store.SnapshotEvictionCandidate, repoID int64,
	eventType, logPrefix string, msg func(store.SnapshotEvictionCandidate) string,
) {
	pool := newDropPool(cfg, engineconn.Connect)
	defer pool.close()

	results := make([]error, len(cands))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(4)
	for i, c := range cands {
		if IsPinned(c.Fingerprint) {
			continue
		}
		g.Go(func() error {
			if err := pool.drop(gctx, c); err != nil {
				slog.Warn(logPrefix+" drop", "template", c.TemplateName, "engine", c.Engine, "err", err)
				results[i] = err
			}
			return nil
		})
	}
	_ = g.Wait()
	for i, c := range cands {
		if results[i] != nil || IsPinned(c.Fingerprint) {
			continue
		}
		if err := st.DeleteSnapshot(ctx, c.Fingerprint); err != nil {
			slog.Warn(logPrefix+" delete row", "fp", c.Fingerprint, "template", c.TemplateName, "err", err)
		}
		_ = st.WriteEvent(ctx, store.LevelInfo, eventType, msg(c),
			repoID, 0, "", 0, map[string]string{
				"engine":      c.Engine,
				"template":    c.TemplateName,
				"source_db":   c.SourceDB,
				"fingerprint": c.Fingerprint,
			})
	}
}

// PurgeRepo drops every cached template for the given repo and
// removes the corresponding snapshot rows. Used by MCP
// `snapshots_purge` (and any future CLI surface) when the user wants
// the next prepare to rebuild from scratch — e.g. after a schema
// migration framework changes its dump format.
//
// Returns the count of rows dropped + a multi-error of any per-row
// failures. Continues past per-row failures so a single bad engine
// row doesn't strand the rest.
func PurgeRepo(ctx context.Context, cfg *config.Config, st *store.Store, repoID int64) (dropped int, errs []error) {
	cands, err := st.ListSnapshotsForRepo(ctx, repoID)
	if err != nil {
		return 0, []error{err}
	}
	pool := newDropPool(cfg, engineconn.Connect)
	defer pool.close()
	for _, c := range cands {
		if err := pool.drop(ctx, c); err != nil {
			errs = append(errs, fmt.Errorf("drop %s (%s): %w", c.TemplateName, c.Engine, err))
			continue
		}
		if err := st.DeleteSnapshot(ctx, c.Fingerprint); err != nil {
			errs = append(errs, fmt.Errorf("delete row %s: %w", c.Fingerprint, err))
			continue
		}
		dropped++
		_ = st.WriteEvent(ctx, store.LevelInfo, store.EvtSnapshotsPurge,
			fmt.Sprintf("purged %s (%s)", c.TemplateName, c.Engine),
			repoID, 0, "", 0, map[string]string{
				"engine":      c.Engine,
				"template":    c.TemplateName,
				"source_db":   c.SourceDB,
				"fingerprint": c.Fingerprint,
			})
	}
	return dropped, errs
}

// SweepBySource evicts cached templates that exceed
// `cfg.Snapshots.KeepPerSource` per source, where a "source" is the
// migration-content key (`migrations_hash`). Within each source the N
// most-recently-used templates are kept; older ones are dropped. Bounds
// the per-source template fan-out that accumulates as a project's
// dump/lockfile/engine-version churn while migration content holds
// steady. Runs as part of the daemon's periodic GC tick.
func SweepBySource(ctx context.Context, cfg *config.Config, st *store.Store) {
	keep := cfg.Snapshots.KeepPerSource
	if keep == 0 {
		return
	}
	cands, err := st.ListSnapshotsBeyondPerSource(ctx, keep)
	if err != nil {
		slog.Warn("snapshot source sweep query", "err", err)
		return
	}
	evictCandidates(ctx, cfg, st, cands, 0, store.EvtSnapshotsEvictSource, "snapshot source sweep",
		func(c store.SnapshotEvictionCandidate) string {
			return fmt.Sprintf("evicted %s (over keep_per_source=%d)", c.TemplateName, keep)
		})
}

// dropPool dials each (engine family, connection) at most once per
// eviction batch. `connect` is injectable so the reuse contract is
// assertable without a live engine (#55). Named connections dial
// separately from the singular block (#44).
type dropPool struct {
	cfg     *config.Config
	connect func(context.Context, *config.Config, engine.Family, string) (engineconn.Conn, bool, error)
	conns   map[connKey]engineconn.Conn
}

type connKey struct {
	fam  engine.Family
	name string
}

func newDropPool(
	cfg *config.Config,
	connect func(context.Context, *config.Config, engine.Family, string) (engineconn.Conn, bool, error),
) *dropPool {
	return &dropPool{cfg: cfg, connect: connect, conns: map[connKey]engineconn.Conn{}}
}

func (p *dropPool) get(ctx context.Context, fam engine.Family, name string) (engineconn.Conn, error) {
	key := connKey{fam, name}
	if c, ok := p.conns[key]; ok {
		return c, nil
	}
	conn, configured, err := p.connect(ctx, p.cfg, fam, name)
	if !configured {
		return nil, fmt.Errorf("connections.%s not configured", fam)
	}
	if err != nil {
		return nil, err
	}
	p.conns[key] = conn
	return conn, nil
}

func (p *dropPool) close() {
	for _, c := range p.conns {
		_ = c.Close()
	}
}

// drop removes the candidate's template + its pre-warmed spare family.
func (p *dropPool) drop(ctx context.Context, c store.SnapshotEvictionCandidate) error {
	fam, ok := engine.Canonical(c.Engine)
	if !ok {
		return fmt.Errorf("eviction: unsupported engine %q", c.Engine)
	}
	conn, err := p.get(ctx, fam, c.Connection)
	if err != nil {
		return err
	}
	if err := conn.DropSnapshot(ctx, c.TemplateName); err != nil {
		return err
	}
	// Reap the template's pre-warmed spare family too — spares are
	// anonymous engine-side copies with no SQLite row of their own, so
	// nothing else would ever collect them once the template is gone.
	// Prefix-reap is a no-op for engines/templates without spares.
	if _, err := conn.DropMatching(ctx, c.TemplateName+PrewarmSuffix); err != nil {
		return fmt.Errorf("drop spare family %s%s*: %w", c.TemplateName, PrewarmSuffix, err)
	}
	return nil
}

// dropTemplate is the single-candidate path: one batch of one.
func dropTemplate(ctx context.Context, cfg *config.Config, c store.SnapshotEvictionCandidate) error {
	pool := newDropPool(cfg, engineconn.Connect)
	defer pool.close()
	return pool.drop(ctx, c)
}

// SweepByAge drops every cached template whose `last_used_at` is
// older than `cfg.Snapshots.MaxAgeDays` days. Runs as
// part of the daemon's periodic GC tick. Cheap on small tables;
// keep an eye on it if the snapshots table grows past a few thousand
// rows.
func SweepByAge(ctx context.Context, cfg *config.Config, st *store.Store) {
	days := cfg.Snapshots.MaxAgeDays
	if days == 0 {
		return
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
	cands, err := st.ListSnapshotsOlderThan(ctx, cutoff)
	if err != nil {
		slog.Warn("snapshot age sweep query", "err", err)
		return
	}
	evictCandidates(ctx, cfg, st, cands, 0, store.EvtSnapshotsEvictAge, "snapshot age sweep",
		func(c store.SnapshotEvictionCandidate) string {
			return fmt.Sprintf("evicted %s (older than %dd)", c.TemplateName, days)
		})
}

// SweepBySize evicts the largest cached templates until total
// `size_bytes` falls below `cfg.Snapshots.MaxTotalGb`.
// Snapshots with size_bytes = NULL (never recorded) are evicted
// last — they're treated as size 0 by the ORDER BY in the store
// query.
func SweepBySize(ctx context.Context, cfg *config.Config, st *store.Store) {
	gb := cfg.Snapshots.MaxTotalGb
	if gb == 0 {
		return
	}
	capBytes := int64(gb) * 1024 * 1024 * 1024
	total, err := st.SumSnapshotBytes(ctx)
	if err != nil {
		slog.Warn("snapshot size sweep sum", "err", err)
		return
	}
	if total <= capBytes {
		return
	}
	cands, sizes, err := st.ListSnapshotsLargestLRU(ctx)
	if err != nil {
		slog.Warn("snapshot size sweep query", "err", err)
		return
	}
	for i, c := range cands {
		if total <= capBytes {
			break
		}
		if IsPinned(c.Fingerprint) {
			continue
		}
		if err := dropTemplate(ctx, cfg, c); err != nil {
			slog.Warn("snapshot size sweep drop", "template", c.TemplateName, "err", err)
			continue
		}
		if err := st.DeleteSnapshot(ctx, c.Fingerprint); err != nil {
			slog.Warn("snapshot size sweep delete row",
				"fp", c.Fingerprint, "template", c.TemplateName, "err", err)
		}
		total -= sizes[i]
		_ = st.WriteEvent(ctx, store.LevelInfo, store.EvtSnapshotsEvictSize,
			fmt.Sprintf("evicted %s (size=%d)", c.TemplateName, sizes[i]),
			0, 0, "", 0, map[string]string{
				"engine":      c.Engine,
				"template":    c.TemplateName,
				"fingerprint": c.Fingerprint,
			})
	}
}

// PruneResult reports one PruneOrphans outcome per orphan row.
type PruneResult struct {
	Engine      string `json:"engine"`
	Template    string `json:"template"`
	Fingerprint string `json:"fingerprint"`
}

// FindRowOrphans returns the SQLite snapshot rows whose engine-side
// template no longer exists — the leftovers of templates removed
// out-of-band (manual index/database deletion, engine wipe, a died
// capture). An engine that is unconfigured or unreachable contributes
// nothing (probe errors mean "unknown", not "orphan"), and pinned
// fingerprints (in-flight prepare) are skipped.
func FindRowOrphans(ctx context.Context, cfg *config.Config, st *store.Store, repoID int64) ([]PruneResult, []error) {
	cands, err := st.ListSnapshotsForRepo(ctx, repoID)
	if err != nil {
		return nil, []error{fmt.Errorf("list snapshots: %w", err)}
	}
	var orphans []PruneResult
	conns := map[connKey]engineconn.Conn{}
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for _, c := range cands {
		if IsPinned(c.Fingerprint) {
			continue
		}
		fam, ok := engine.Canonical(c.Engine)
		if !ok {
			continue
		}
		key := connKey{fam, c.Connection}
		conn, cached := conns[key]
		if !cached {
			cn, configured, cerr := engineconn.Connect(ctx, cfg, fam, c.Connection)
			if !configured || cerr != nil {
				// Unknown state — keep the rows rather than guess.
				continue
			}
			conns[key] = cn
			conn = cn
		}
		exists, perr := conn.Exists(ctx, c.TemplateName)
		if perr != nil || exists {
			continue
		}
		orphans = append(orphans, PruneResult{Engine: c.Engine, Template: c.TemplateName, Fingerprint: c.Fingerprint})
	}
	return orphans, nil
}

// PruneRowOrphans deletes the rows FindRowOrphans reports. The
// inverse cleanup of DropOrphans (engine template with no row): here
// the row exists and the template is gone. Live templates
// are never touched, so this is safe to run any time, unlike
// PurgeRepo. Returns the pruned rows.
func PruneRowOrphans(ctx context.Context, cfg *config.Config, st *store.Store, repoID int64) ([]PruneResult, []error) {
	orphans, errs := FindRowOrphans(ctx, cfg, st, repoID)
	pruned := make([]PruneResult, 0, len(orphans))
	for _, o := range orphans {
		if derr := st.DeleteSnapshot(ctx, o.Fingerprint); derr != nil {
			errs = append(errs, fmt.Errorf("delete row %s: %w", o.Fingerprint, derr))
			continue
		}
		pruned = append(pruned, o)
		_ = st.WriteEvent(ctx, store.LevelInfo, store.EvtSnapshotsPruneOrphan,
			fmt.Sprintf("pruned orphan snapshot row %s (%s: template gone)", o.Template, o.Engine),
			repoID, 0, "", 0, map[string]string{
				"engine":      o.Engine,
				"template":    o.Template,
				"fingerprint": o.Fingerprint,
			})
	}
	return pruned, errs
}
