package wt

import (
	"context"
	"fmt"
	"time"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/hooks"
	"github.com/stubbedev/treeman/internal/prepare"
	"github.com/stubbedev/treeman/internal/slug"
	"github.com/stubbedev/treeman/internal/store"
	"github.com/stubbedev/treeman/internal/template"
	"github.com/stubbedev/treeman/internal/wtlock"
)

// RunLocalFinalize executes the setup + prepare tail in the calling
// process. Used by `wt finalize --local` (the manual retry path when
// the daemon didn't run the create tail).
//
// The daemon has its own canonical FinalizeWorktree; this is the
// "daemon-less" mirror that runs when the daemon is unreachable.
func RunLocalFinalize(
	ctx context.Context,
	cfg *config.Config,
	repoRoot, wtPath string,
	sl slug.Slug,
	isMain bool,
	st *store.Store,
	repoID, wtID int64,
	env map[string]string,
	skipPrepare bool,
	sink Sink,
) error {
	if sink == nil {
		sink = NoopSink{}
	}
	// Queue behind a finalize already running for this worktree — the
	// daemon's detached create tail, or another `--local` run. Running
	// alongside it rebuilds the same databases under its feet (#123).
	unlock, err := wtlock.Acquire(ctx, wtlock.Finalize, wtPath, func() {
		sink.Info("another finalize of %s is still running; waiting for it to finish", wtPath)
	})
	if err != nil {
		return fmt.Errorf("wait for running finalize: %w", err)
	}
	defer unlock()
	runTrigger := func(trigger string, actions []config.Action) error {
		if len(actions) == 0 {
			return nil
		}
		started := hooks.EmitHookStart(ctx, st, repoID, wtID, trigger, len(actions))
		out, err := hooks.RunHooks(ctx, trigger, actions, repoRoot, wtPath, sl.Value, isMain, env, true)
		hooks.PersistOutcome(ctx, st, repoID, wtID, trigger, started, time.Now().UnixMilli(), out)
		if err != nil {
			return err
		}
		sink.Info("%s: %d action(s) complete (logs in %s/.treeman-hooks/)",
			trigger, len(actions), wtPath)
		return nil
	}
	// Materialize links/copies + render patches before hooks fire (a
	// before-engines hook may read the copied/patched `.env`). The daemon
	// path does this in FinalizeWorktree; this mirror covers the detached
	// child / `wt finalize --local` fallback. Skipped for the main
	// worktree (src == dst), matching the daemon.
	if !isMain {
		if err := BringInFiles(ctx, repoRoot, wtPath, cfg.Worktrees.Links, "link", sink); err != nil {
			return err
		}
		if err := BringInFiles(ctx, repoRoot, wtPath, cfg.Worktrees.Copies, "copy", sink); err != nil {
			return err
		}
		portMap, _ := st.LoadWorktreePorts(ctx, wtID)
		tplCtx := template.FromSlug(sl).WithPorts(portMap)
		if err := applyPatches(ctx, cfg.Patches, wtPath, tplCtx, sink); err != nil {
			return err
		}
	}
	if err := runTrigger("create-before-engines", cfg.Hooks.OnCreateBeforeEngines); err != nil {
		return err
	}
	if skipPrepare || len(cfg.Databases) == 0 {
		return runTrigger("create-after-engines", cfg.Hooks.OnCreateAfterEngines)
	}
	outs, err := prepare.Run(ctx, cfg, wtPath, sl, st, repoID, wtID, env)
	for _, o := range outs {
		sink.Info("prepare[%s] %s template=%s clones=%d",
			o.Engine, o.SourceDB, o.TemplateName, len(o.Clones))
	}
	if err != nil {
		// Fail the run, as the daemon's finalize does: create-after-engines
		// hooks assume ready databases, and exiting 0 here reported success
		// over a stale or half-built database (#123).
		return fmt.Errorf("prepare: %w", err)
	}
	return runTrigger("create-after-engines", cfg.Hooks.OnCreateAfterEngines)
}
