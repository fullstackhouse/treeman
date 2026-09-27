package prepare

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/db/engineconn"
	"github.com/stubbedev/treeman/internal/engine"
	"github.com/stubbedev/treeman/internal/snapshot"
	"github.com/stubbedev/treeman/internal/store"
	"github.com/stubbedev/treeman/pkg/safego"
)

// spareEngine bundles the spare-pool capabilities of one engine's
// already-connected driver (#53): replenishment clones via
// SnapshotCreator, cache-hit claims via SpareClaimer (Postgres rename,
// MySQL staged physical clone). Built from any engineconn.Conn whose
// driver implements the capabilities.
type spareEngine struct {
	engine  string
	conn    engineconn.Conn
	creator engineconn.SnapshotCreator
	claimer engineconn.SpareClaimer
}

// spareEngineFromConn resolves the capability set from `conn`; nil
// when it lacks the SnapshotCreator floor. The claimer is optional:
// without it the pool replenishes but restores can't claim (a future
// engine can ship create-only).
func spareEngineFromConn(engineName string, conn engineconn.Conn) *spareEngine {
	creator, ok := conn.(engineconn.SnapshotCreator)
	if !ok {
		return nil
	}
	claimer, _ := conn.(engineconn.SpareClaimer)
	return &spareEngine{engine: engineName, conn: conn, creator: creator, claimer: claimer}
}

// spareEngineFor dials `engineName` fresh and resolves its spare
// capabilities — the path used by the detached replenisher. (nil, err)
// when unconfigured or incapable.
func spareEngineFor(ctx context.Context, cfg *config.Config, engineName string) (*spareEngine, error) {
	fam, ok := engine.Canonical(engineName)
	if !ok {
		return nil, fmt.Errorf("prewarm: unsupported engine %q", engineName)
	}
	conn, configured, err := engineconn.Connect(ctx, cfg, fam)
	if !configured {
		return nil, fmt.Errorf("prewarm: connections.%s not configured", fam)
	}
	if err != nil {
		return nil, fmt.Errorf("prewarm: connect %s: %w", fam, err)
	}
	se := spareEngineFromConn(string(fam), conn)
	if se == nil {
		_ = conn.Close()
		return nil, fmt.Errorf("prewarm: engine %s cannot create spare snapshots", fam)
	}
	return se, nil
}

// spareClaimRestore wraps the plain restore with the spare-claim fast
// path: clear the target, then try to claim one of the template's
// pre-warmed spares. A claim is milliseconds (Postgres rename) or a
// file-copy import (MySQL physical clone) versus a full logical
// restore. Claims of the same slot resolve cleanly — Postgres rename
// is atomic; MySQL claims each use a DIFFERENT spare, so no shared
// export lock — and a dry pool falls back to the plain restore. The
// same restorer serves the cache-hit source AND its fanout clones, so
// a pool of N covers the first N restores of a worktree create.
func spareClaimRestore(
	se *spareEngine,
	st *store.Store,
	repoID, worktreeID int64,
	prewarm uint32,
	plainRestore func(ctx context.Context, template, target string) error,
) cloneRestorer {
	return func(ctx context.Context, template, target string) error {
		if se.claimer == nil {
			return plainRestore(ctx, template, target)
		}
		// The claim path can't overwrite, so clear the target first. A
		// failed drop (e.g. lingering connections) just means no fast
		// path — the plain restore re-attempts the drop with its own
		// semantics.
		if err := se.conn.DropSnapshot(ctx, target); err == nil {
			for slot := 1; slot <= int(prewarm); slot++ {
				spare := snapshot.SpareName(template, slot)
				if err := se.claimer.ClaimSpare(ctx, spare, target); err == nil {
					_ = st.WriteEvent(ctx, store.LevelInfo, store.EvtSnapshotsPrewarmClaim,
						fmt.Sprintf("claimed spare %s → %s", spare, target),
						repoID, worktreeID, "", 0, map[string]string{
							"engine":   se.engine,
							"template": template,
							"spare":    spare,
							"target":   target,
						})
					return nil
				}
			}
		}
		return plainRestore(ctx, template, target)
	}
}

// restoreFor picks the restore strategy for one database: plain
// SnapshotRestore, or the spare-claim wrapper when a pre-warm pool is
// configured and the engine has the capabilities. Works for any
// capability-bearing family — postgres and mysql today (#53).
func restoreFor(
	engineName string,
	conn engineconn.Conn,
	plainRestore func(ctx context.Context, template, target string) error,
	st *store.Store,
	repoID, worktreeID int64,
	d config.DatabaseConfig,
) cloneRestorer {
	if d.Prewarm == 0 {
		return plainRestore
	}
	se := spareEngineFromConn(engineName, conn)
	if se == nil {
		slog.Warn("prewarm configured but engine lacks spare capabilities",
			"engine", engineName, "template_pool", d.Prewarm)
		return plainRestore
	}
	return spareClaimRestore(se, st, repoID, worktreeID, d.Prewarm, plainRestore)
}

// maybeSpawnPrewarm is the prepare paths' deferred pool top-up: fires
// only after a successful exit that left templateName in place (cache
// hit, incremental/rollback/dump-only, or cold build — all set
// out.TemplateName; skip/branch-scoped outcomes don't).
func maybeSpawnPrewarm(
	cfg *config.Config,
	st *store.Store,
	repoID, worktreeID int64,
	d config.DatabaseConfig,
	fingerprint, templateName string,
	out Outcome,
	err error,
) {
	if err != nil || d.Prewarm == 0 || out.TemplateName != templateName {
		return
	}
	spawnPrewarm(cfg, st, repoID, worktreeID, d.Engine, fingerprint, templateName, d.Prewarm)
}

// prewarmInFlight dedups concurrent replenishers per fingerprint —
// two finalizes hitting the same template would otherwise both walk
// the slot list and double-restore the same spares.
var prewarmInFlight sync.Map

// spawnPrewarm tops the template's spare pool back up to n slots in a
// detached goroutine, mirroring spawnEvict's pattern: fresh background
// context (the prepare that triggered it must not block on, nor cancel,
// pool maintenance), hard 5-minute timeout so a stalled CREATE can't
// wedge the goroutine, and safego panic recovery. The fingerprint is
// pinned for the duration so a concurrent GC sweep can't drop the
// template out from under a spare mid-clone.
//
// Engine-generic (#53): the connection + spare capabilities resolve
// from the registry via spareEngineFor, so mysql pools replenish the
// same way postgres ones do.
//
// Slot-name idempotence makes replenish self-healing: only missing
// `_spare<i>` slots are created, and slots beyond n (config shrank)
// are reaped best-effort.
func spawnPrewarm(
	cfg *config.Config,
	st *store.Store,
	repoID, worktreeID int64,
	engineName string,
	fingerprint, templateName string,
	n uint32,
) {
	if n == 0 {
		return
	}
	if _, busy := prewarmInFlight.LoadOrStore(fingerprint, struct{}{}); busy {
		return
	}
	safego.Go("snapshot:prewarm", templateName, func() {
		defer prewarmInFlight.Delete(fingerprint)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		unpin := snapshot.Pin(fingerprint)
		defer unpin()

		se, err := spareEngineFor(ctx, cfg, engineName)
		if err != nil {
			slog.Warn("prewarm resolve engine", "engine", engineName, "template", templateName, "err", err)
			return
		}
		defer func() { _ = se.conn.Close() }()

		// The template can vanish between the triggering prepare and
		// this goroutine running (eviction raced the pin). Spares of a
		// dead template are unreachable, so just bail.
		if alive, _ := se.conn.Exists(ctx, templateName); !alive {
			return
		}

		created := atomic.Int64{}
		// Missing slots are restored concurrently: each restore is a full
		// template copy, so serial top-up after a burst made the tail of
		// the burst wait n x copy (the exact cost the pool exists to
		// avoid). Slot names are distinct so restores can't collide; the
		// cap keeps us from piling copy load onto the server beyond a
		// small multiple of what a create burst needs.
		restoreLimit := int(min(n, 4))
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(restoreLimit)
		for slot := 1; slot <= int(n); slot++ {
			name := snapshot.SpareName(templateName, slot)
			if exists, _ := se.conn.Exists(ctx, name); exists {
				continue
			}
			g.Go(func() error {
				if err := se.creator.CreateSnapshot(gctx, templateName, name); err != nil {
					slog.Warn("prewarm restore", "spare", name, "template", templateName, "err", err)
					return err
				}
				created.Add(1)
				return nil
			})
		}
		_ = g.Wait()
		createdN := int(created.Load())

		// Reap slots beyond n so shrinking `prewarm` in config actually
		// shrinks the pool instead of leaving zombie spares around until
		// template eviction.
		if names, err := se.conn.ListMatching(ctx, templateName+snapshot.PrewarmSuffix); err == nil {
			for _, name := range names {
				if slot, ok := snapshot.SpareSlot(name, templateName); ok && slot > int(n) {
					if err := se.conn.DropSnapshot(ctx, name); err != nil {
						slog.Warn("prewarm reap extra slot", "spare", name, "err", err)
					}
				}
			}
		}

		if createdN > 0 {
			_ = st.WriteEvent(ctx, store.LevelInfo, store.EvtSnapshotsPrewarm,
				fmt.Sprintf("pre-warmed %d spare(s) for %s", createdN, templateName),
				repoID, worktreeID, "", 0, map[string]string{
					"engine":   se.engine,
					"template": templateName,
					"created":  strconv.Itoa(createdN),
					"pool":     strconv.Itoa(int(n)),
				})
		}
	})
}
