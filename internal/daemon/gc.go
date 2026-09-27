package daemon

import (
	"context"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/stubbedev/treeman/internal/resolve"
	"github.com/stubbedev/treeman/internal/snapshot"
)

// SnapshotGCLoop runs the periodic snapshot eviction sweep until
// ctx is cancelled. Wakes every `snapshots.retention.gc_interval_
// minutes` of the GLOBAL config (`~/.config/treeman/config.yaml`),
// and per-repo it consults that repo's own config so cap policies
// can be per-project.
//
// Each tick:
//  1. Enumerate every repo from the SQLite registry.
//  2. Load the repo's layered config (so .treeman.yaml overrides
//     apply).
//  3. Run snapshot.EvictExcess against the repo's CapPerRepo.
//
// Errors are logged and the loop continues — one flaky repo
// shouldn't break GC for the others.
func SnapshotGCLoop(ctx context.Context, st *State) {
	// Pull the global interval from the layered config rooted at
	// the user's home dir (no repo). This is the steady-state knob.
	globalCfg, _ := resolve.LoadResolved("")
	interval := time.Duration(globalCfg.Snapshots.GcIntervalMinutes) * time.Minute
	if interval <= 0 {
		interval = time.Hour
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	slog.Info("snapshot_gc_loop started", "interval", interval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			runGCSweep(ctx, st)
		}
	}
}

func runGCSweep(ctx context.Context, st *State) {
	paths, err := st.Store.ListRepoPaths(ctx)
	if err != nil {
		slog.Warn("snapshot_gc enumerate repos", "err", err)
		return
	}
	// Per-repo cap eviction first — keeps the cache shape sane
	// regardless of size / age. Errors are logged and continued so
	// one bad repo doesn't block GC for the rest. Lookups stay
	// read-only (a GC tick must not be the thing that creates repo
	// rows) and repos evict in parallel — each repo drives its own
	// engines, and the drops inside are conn-cached + bounded (#55).
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(4)
	for _, p := range paths {
		repoID, err := st.Store.LookupRepoID(ctx, p)
		if err != nil || repoID == 0 {
			continue
		}
		cfg, err := resolve.LoadResolved(p)
		if err != nil {
			slog.Warn("snapshot_gc load cfg", "repo", p, "err", err)
			continue
		}
		g.Go(func() error {
			snapshot.EvictExcess(gctx, &cfg, st.Store, repoID)
			return nil
		})
	}
	_ = g.Wait()
	// Global age + size sweeps — driven by the global config so the
	// limits are user-wide, not per-repo.
	globalCfg, err := resolve.LoadResolved("")
	if err != nil {
		slog.Warn("snapshot_gc load global", "err", err)
		return
	}
	snapshot.SweepByAge(ctx, &globalCfg, st.Store)
	snapshot.SweepBySize(ctx, &globalCfg, st.Store)
	snapshot.SweepBySource(ctx, &globalCfg, st.Store)
}
