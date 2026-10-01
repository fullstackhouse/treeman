package prepare

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/gitcmd"
	"github.com/stubbedev/treeman/internal/store"
)

// ReapBranchDurables drops every branch_scoped durable copy belonging to
// `branch` across all of the repo's worktrees. The daemon calls it right
// after it prunes a now-deleted local branch (upstream gone + provably
// merged), so a branch's preserved per-branch databases don't outlive the
// branch.
//
// Because the branch name is known at call time, the durable name is computed
// FORWARD via durable(active, branch) — no reverse lookup of the one-way hash,
// no bookkeeping table. Each (worktree, branch_scoped db) pair yields one
// candidate durable; the existence gate means worktrees that never held the
// branch are silently skipped.
//
// Only the durable copy is ever touched, never the active namespace a live
// worktree connects to. So a mis-rendered active namespace can at worst leave
// a durable un-reaped (a leak) — it can never drop live data.
func ReapBranchDurables(ctx context.Context, cfg *config.Config, st *store.Store, repoID int64, branch string) {
	if branch == "" {
		return
	}
	ReapBranchDurablesMany(ctx, cfg, st, repoID, []string{branch})
}

// ReapBranchDurablesMany is the batched form of ReapBranchDurables:
// a mass prune of P branches costs ONE worktree listing, ONE
// main-worktree overlay, and ONE engine connect per branch_scoped
// database — the per-branch shape paid P listings and P connects, and
// PxDxW serial catalog probes either way. Drops, row deletions and
// branch_reap events are identical to calling the single-branch
// reaper P times (#72).
func ReapBranchDurablesMany(ctx context.Context, cfg *config.Config, st *store.Store, repoID int64, branches []string) {
	filtered := make([]string, 0, len(branches))
	for _, b := range branches {
		if b != "" {
			filtered = append(filtered, b)
		}
	}
	if len(filtered) == 0 {
		return
	}
	worktrees, err := st.ListWorktreesForRepo(ctx, repoID)
	if err != nil {
		slog.Warn("reap durables: list worktrees", "repo_id", repoID, "err", err)
		return
	}
	if len(worktrees) == 0 {
		return
	}

	// The main worktree renders its active namespace from the main_worktree
	// overlay (bare, slug-free names); linked worktrees use the base
	// templates. Build the overlaid view once on a shallow copy so the
	// caller's cfg.Databases is left untouched (the overlay reslices into a
	// fresh backing array).
	mainCfg := *cfg
	config.ApplyMainWorktreeOverlay(&mainCfg)

	for i, d := range cfg.Databases {
		if !d.BranchScoped {
			continue
		}
		scope, _, ok := branchScopeFor(d.Engine)
		if !ok {
			continue
		}
		// Reap only touches hash-derived durable namespaces, which never
		// collide with a sibling's prefix — no sibling filter needed.
		eng, closeEng, err := connectBranchEngine(ctx, cfg, d.Engine, d.Connection, nil, d.PhysicalCloneMinBytes)
		if err != nil {
			slog.Warn("reap durables: connect engine", "engine", d.Engine, "err", err)
			closeEng()
			continue
		}
		if eng == nil {
			closeEng()
			continue
		}
		targets := make([]reapTarget, 0, len(worktrees))
		for _, wt := range worktrees {
			dbForWt := d
			if wt.IsMain && i < len(mainCfg.Databases) {
				dbForWt = mainCfg.Databases[i]
			}
			active, err := activeNamespace(dbForWt, scope, wt.Path)
			if err != nil {
				continue
			}
			targets = append(targets, reapTarget{wtID: wt.ID, active: active})
		}
		reapViaEngine(ctx, eng, st, repoID, targets, filtered)
		closeEng()
	}
}

// reapTarget pairs a worktree row id with the active namespace name
// rendered for it under one branch_scoped database.
type reapTarget struct {
	wtID   int64
	active string
}

// reapViaEngine probes and drops every (target x branch) durable pair
// through one already-connected engine, writing the row deletion + the
// branch_reap event per drop. Shared by the single-branch and batched
// reapers so their observable behavior cannot drift.
func reapViaEngine(
	ctx context.Context,
	eng *branchEngine,
	st *store.Store,
	repoID int64,
	targets []reapTarget,
	branches []string,
) {
	for _, tgt := range targets {
		for _, branch := range branches {
			dur := eng.durable(tgt.active, branch)
			exists, err := eng.drv.Exists(ctx, dur)
			if err != nil {
				slog.Warn("reap durables: probe", "engine", eng.engine, "durable", dur, "err", err)
				continue
			}
			if !exists {
				continue
			}
			if err := eng.drv.DropDurable(ctx, dur); err != nil {
				slog.Warn("reap durables: drop", "engine", eng.engine,
					"durable", dur, "branch", branch, "err", err)
				continue
			}
			_ = st.DeleteBranchDurable(ctx, repoID, dur)
			_ = st.WriteEvent(ctx, store.LevelInfo, store.EvtBranchReap,
				fmt.Sprintf("%s: dropped durable for deleted branch %q (active=%s)", eng.engine, branch, tgt.active),
				repoID, tgt.wtID, "", 0, map[string]string{
					"engine":  eng.engine,
					"branch":  branch,
					"durable": dur,
					"active":  tgt.active,
				})
		}
	}
}

// ReapOrphanDurables drops every TRACKED branch_scoped durable whose branch no
// longer exists as a local git ref, and removes its tracking row. It is the
// catch-all that ReapBranchDurables structurally can't be: that reaper
// forward-computes one just-deleted branch's durable name across LIVE
// worktrees, so it misses durables left by a removed worktree or a branch
// deleted out-of-band (the source of the 511-orphan-index leak). This sweep
// enumerates the recorded pool instead and drops by stored NAME — no hash
// re-derivation, no dependence on the worktree still existing.
//
// `repoRoot` is the repo's main checkout, used to list local branches. On a
// git error (or a repo with no listable branches) it declines to drop
// anything rather than risk wiping live durables.
func ReapOrphanDurables(ctx context.Context, cfg *config.Config, st *store.Store, repoID int64, repoRoot string) {
	durables, err := st.ListBranchDurables(ctx, repoID)
	if err != nil {
		slog.Warn("reap orphan durables: list", "repo_id", repoID, "err", err)
		return
	}
	if len(durables) == 0 {
		return
	}
	live, ok := localBranchSet(ctx, repoRoot)
	if !ok || len(live) == 0 {
		// Couldn't enumerate branches (transient git error, or a worktree
		// with no refs) — declining to drop is the safe default; the next
		// tick retries once git is readable.
		return
	}

	// Group orphans by engine so each engine connects at most once.
	byEngine := map[string][]store.BranchDurableRow{}
	for _, d := range durables {
		if _, alive := live[d.Branch]; alive {
			continue
		}
		byEngine[d.Engine] = append(byEngine[d.Engine], d)
	}
	for eng, rows := range byEngine {
		// Reap only touches hash-derived durable namespaces by exact name —
		// no sibling filter needed.
		be, closeEng, cerr := connectBranchEngine(ctx, cfg, eng, "", nil, nil)
		if cerr != nil {
			slog.Warn("reap orphan durables: connect engine", "engine", eng, "err", cerr)
			closeEng()
			continue
		}
		if be == nil {
			closeEng()
			continue
		}
		for _, d := range rows {
			if err := be.drv.DropDurable(ctx, d.DurableName); err != nil {
				slog.Warn("reap orphan durables: drop", "engine", eng,
					"durable", d.DurableName, "branch", d.Branch, "err", err)
				continue
			}
			_ = st.DeleteBranchDurable(ctx, repoID, d.DurableName)
			_ = st.WriteEvent(ctx, store.LevelInfo, store.EvtBranchReap,
				fmt.Sprintf("%s: dropped orphan durable for absent branch %q (active=%s)", eng, d.Branch, d.DBKey),
				repoID, d.WorktreeID, "", 0, map[string]string{
					"engine":  eng,
					"branch":  d.Branch,
					"durable": d.DurableName,
					"active":  d.DBKey,
					"reason":  "orphan_branch_gone",
				})
		}
		closeEng()
	}
}

// esDurableMarker is the index-name prefix branchEngine.durable emits for the
// elasticsearch scope ("tmbs_<16hex>_"). Disjoint from the snapshot-cache
// marker ("tm_"), the active clone prefix ("kho_"), and base data
// ("client_*"/"dev_*"), so a prefix scan for it can only ever match treeman ES
// branch durables.
const esDurableMarker = "tmbs_"

// esDurablePrefix extracts the durable family prefix "tmbs_<16hex>_" from an ES
// index name, mirroring branchEngine.durable for the prefix/elasticsearch
// scope. ok=false for any name that isn't a treeman ES durable — snapshot-cache
// (`tm_`), active clones (`kho_`), and base data (`client_*`/`dev_*`) all fall
// through untouched, so the reconcile can never reach live or cached data.
func esDurablePrefix(index string) (string, bool) {
	rest, ok := strings.CutPrefix(index, esDurableMarker)
	if !ok {
		return "", false
	}
	// bsHash emits exactly 16 lowercase-hex chars, followed by the trailing '_'.
	if len(rest) < 17 || rest[16] != '_' || !isBSHash(rest[:16]) {
		return "", false
	}
	return esDurableMarker + rest[:16] + "_", true
}

// isBSHash reports whether s is exactly a bsHash: 16 lowercase-hex chars.
func isBSHash(s string) bool {
	if len(s) != 16 {
		return false
	}
	for i := range 16 {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// untrackedMarker is the name prefix branchEngine.durable emits for each
// engine the untracked-durable reconcile covers. Redis is absent: its
// durables are key prefixes inside one keyspace, and enumerating them means
// a full SCAN of the live keyspace every sweep.
var untrackedMarker = map[string]string{
	"mysql":         "_tmbs_",
	"postgres":      "_tmbs_",
	"mongodb":       "_tmbs_",
	"s3":            "tmbs-",
	"elasticsearch": esDurableMarker,
}

// durableFamily maps an engine-side name to the durable it belongs to, as
// branchEngine.durable spells it (and as branch_durables stores it). The
// name-scoped engines and S3 hold one durable per database/bucket, so the
// name must be EXACTLY marker+bsHash; ES durables are index families
// sharing a "tmbs_<hash>_" prefix. ok=false for anything else, so a user
// database that merely starts with the marker is never touched.
func durableFamily(eng, name string) (string, bool) {
	if eng == "elasticsearch" {
		return esDurablePrefix(name)
	}
	marker, ok := untrackedMarker[eng]
	if !ok {
		return "", false
	}
	rest, ok := strings.CutPrefix(name, marker)
	if !ok || !isBSHash(rest) {
		return "", false
	}
	return name, true
}

// listDurableCandidates lists the engine-side names starting with the
// engine's durable marker. ok=false for an adapter with no listing.
func listDurableCandidates(ctx context.Context, drv nsDriver, marker string) ([]string, bool, error) {
	var names []string
	var err error
	switch a := drv.(type) {
	case mysqlNS:
		names, err = a.d.ListMatching(ctx, marker)
	case postgresNS:
		names, err = a.d.ListMatching(ctx, marker)
	case mongoNS:
		names, err = a.d.ListMatching(ctx, marker)
	case s3NS:
		names, err = a.d.ListMatching(ctx, marker)
	case esNS:
		names, err = a.d.ListMatching(ctx, marker)
	default:
		return nil, false, nil
	}
	return names, true, err
}

// UntrackedGraceDefault is how long a durable must have been seen untracked
// before the reconcile drops it.
const UntrackedGraceDefault = 10 * time.Minute

// UntrackedSeen remembers when each untracked durable was first observed,
// so ReapUntrackedDurables only drops one that stayed untracked across
// sweeps at least `Grace` apart. That closes the window between a Capture
// writing a durable and RecordBranchDurable tracking it — captures run
// outside a finalize too (in-worktree checkout, teardown, `db save`), so
// the in-flight-finalize gate alone can't cover them. The zero value is
// usable; Grace 0 means UntrackedGraceDefault.
type UntrackedSeen struct {
	Grace time.Duration
	// Now is the clock; nil means time.Now. Tests pin it.
	Now func() time.Time

	mu    sync.Mutex
	first map[string]time.Time
}

// observe records the durables seen untracked for one engine this sweep
// and returns those first seen at least Grace ago. A durable no longer in
// the untracked set (tracked since, or gone) is forgotten, so its clock
// restarts if it ever turns up untracked again.
func (u *UntrackedSeen) observe(eng string, untracked map[string]struct{}) []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := time.Now()
	if u.Now != nil {
		now = u.Now()
	}
	grace := u.Grace
	if grace <= 0 {
		grace = UntrackedGraceDefault
	}
	if u.first == nil {
		u.first = map[string]time.Time{}
	}
	keyPrefix := eng + "\x00"
	for k := range u.first {
		if name, ok := strings.CutPrefix(k, keyPrefix); ok {
			if _, still := untracked[name]; !still {
				delete(u.first, k)
			}
		}
	}
	var due []string
	for name := range untracked {
		k := keyPrefix + name
		first, seen := u.first[k]
		if !seen {
			u.first[k] = now
			continue
		}
		if now.Sub(first) >= grace {
			due = append(due, name)
		}
	}
	sort.Strings(due)
	return due
}

// ReapUntrackedDurables drops branch_scoped durables that NO repo's
// branch_durables row references, for every engine the repo configures
// branch_scoped (mysql, postgres, mongodb, s3, elasticsearch). It is the
// catch-all the two registry-driven reapers structurally can't be: both
// ReapBranchDurables and ReapOrphanDurables key off the branch_durables
// table, so a durable with no row at all is invisible to them. Untracked
// durables come from captures before the branch_durables table existed
// (migration 0012), a Capture that wrote the durable but died before
// RecordBranchDurable, or a DropDurable that failed after the row was
// deleted. Left unbounded they accumulate forever — on ES as shards until
// the single-node dev cluster can't recover on restart (the 2625-index
// meltdown).
//
// Safe by construction: only names that are exactly a durable spelling
// (marker + 16-hex hash) are candidates, and the namespace a live
// worktree connects to never has that shape; the keep-set is built across
// ALL repos so a shared server never has one repo's sweep drop another's
// durable; a durable must stay untracked for `seen.Grace` across sweeps
// before it is dropped (see UntrackedSeen); and on any registry- or
// list-read error the engine is skipped rather than risk dropping on an
// incomplete keep-set.
func ReapUntrackedDurables(ctx context.Context, cfg *config.Config, st *store.Store, repoID int64, seen *UntrackedSeen) {
	type target struct{ engine, connection string }
	var targets []target
	done := map[target]bool{}
	for _, d := range cfg.Databases {
		if !d.BranchScoped {
			continue
		}
		_, eng, ok := branchScopeFor(d.Engine)
		if !ok {
			continue
		}
		if _, covered := untrackedMarker[eng]; !covered {
			continue
		}
		t := target{eng, d.Connection}
		if !done[t] {
			done[t] = true
			targets = append(targets, t)
		}
	}
	for _, t := range targets {
		reapUntrackedEngine(ctx, cfg, st, repoID, seen, t.engine, t.connection)
	}
}

func reapUntrackedEngine(
	ctx context.Context,
	cfg *config.Config,
	st *store.Store,
	repoID int64,
	seen *UntrackedSeen,
	eng, connection string,
) {
	be, closeEng, err := connectBranchEngine(ctx, cfg, eng, connection, nil, nil)
	if err != nil {
		slog.Warn("reap untracked durables: connect engine", "engine", eng, "err", err)
		closeEng()
		return
	}
	defer closeEng()
	if be == nil {
		return
	}

	keep, err := st.ListAllDurableNamesByEngine(ctx, eng)
	if err != nil {
		// Couldn't read the registry — declining to drop is the safe default;
		// the next sweep retries. Dropping on an unreadable keep-set would risk
		// wiping every durable.
		slog.Warn("reap untracked durables: list registry", "engine", eng, "err", err)
		return
	}
	names, ok, err := listDurableCandidates(ctx, be.drv, untrackedMarker[eng])
	if !ok {
		return
	}
	if err != nil {
		slog.Warn("reap untracked durables: list", "engine", eng, "err", err)
		return
	}

	untracked := map[string]struct{}{}
	for _, n := range names {
		fam, ok := durableFamily(eng, n)
		if !ok {
			continue
		}
		if _, referenced := keep[fam]; referenced {
			continue
		}
		untracked[fam] = struct{}{}
	}
	for _, fam := range seen.observe(eng, untracked) {
		dropped := "1"
		if esa, isES := be.drv.(esNS); isES {
			idx, derr := esa.d.DropMatching(ctx, fam)
			if derr != nil {
				slog.Warn("reap untracked durables: drop", "engine", eng, "durable", fam, "err", derr)
				continue
			}
			if len(idx) == 0 {
				continue
			}
			dropped = strconv.Itoa(len(idx))
		} else if derr := be.drv.DropDurable(ctx, fam); derr != nil {
			slog.Warn("reap untracked durables: drop", "engine", eng, "durable", fam, "err", derr)
			continue
		}
		_ = st.WriteEvent(ctx, store.LevelInfo, store.EvtBranchReap,
			fmt.Sprintf("%s: dropped untracked durable %q (no branch_durables row in any repo)", eng, fam),
			repoID, 0, "", 0, map[string]string{
				"engine":  eng,
				"durable": fam,
				"dropped": dropped,
				"reason":  "orphan_untracked",
			})
	}
}

// localBranchSet returns the repo's local branch names as a set. ok=false on a
// git error (not a repo / git missing) so the orphan sweep can decline to act
// rather than treat "couldn't list" as "every branch is gone".
func localBranchSet(ctx context.Context, repoRoot string) (map[string]struct{}, bool) {
	out, err := gitcmd.Output(ctx, repoRoot, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return nil, false
	}
	set := map[string]struct{}{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if b := strings.TrimSpace(line); b != "" {
			set[b] = struct{}{}
		}
	}
	return set, true
}
