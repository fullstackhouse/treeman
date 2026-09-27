package prepare

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stubbedev/treeman/internal/store"
)

// countingNS wraps an nsDriver and counts Exists probes and DropDurable
// calls — the connect/probe fan-out the batched reaper must shrink (#72).
type countingNS struct {
	nsDriver
	probes int
	drops  []string
}

func (c *countingNS) Exists(ctx context.Context, ns string) (bool, error) {
	c.probes++
	return true, nil
}

func (c *countingNS) DropDurable(ctx context.Context, durable string) error {
	c.drops = append(c.drops, durable)
	return nil
}

// TestReapBranchDurablesManyMatchesPerBranch pins the #72 acceptance
// criteria: reaping P branches through the batched path drops exactly
// the same durables and writes exactly the same branch_reap events as
// P single-branch calls, and probes/drops stay inside one pass over
// (worktrees x branches) rather than re-walking per branch.
func TestReapBranchDurablesManyMatchesPerBranch(t *testing.T) {
	ctx := context.Background()

	newEnv := func(t *testing.T) (*store.Store, int64, []reapTarget, []string) {
		t.Helper()
		st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		repoID, err := st.EnsureRepo(ctx, "/repo", "repo")
		if err != nil {
			t.Fatal(err)
		}
		targets := []reapTarget{
			{wtID: 1, active: "app_main"},
			{wtID: 2, active: "app_fea"},
			{wtID: 3, active: "app_fix"},
		}
		branches := []string{"feat/a", "feat/b", "feat/c", "feat/d", "feat/e", "feat/f", "feat/g", "feat/h", "feat/i", "feat/j"}
		return st, repoID, targets, branches
	}

	fakeEngine := func(ns *countingNS) *branchEngine {
		return &branchEngine{drv: ns, engine: "mysql"}
	}

	// ── batched: ONE pass ──
	stB, repoB, targetsB, branchesB := newEnv(t)
	nsB := &countingNS{nsDriver: &fakeNS{data: map[string]map[string]string{}}}
	reapViaEngine(ctx, fakeEngine(nsB), stB, repoB, targetsB, branchesB)

	// ── per-branch: P passes (the old call pattern) ──
	stP, repoP, targetsP, branchesP := newEnv(t)
	nsP := &countingNS{nsDriver: &fakeNS{data: map[string]map[string]string{}}}
	for _, branch := range branchesP {
		reapViaEngine(ctx, fakeEngine(nsP), stP, repoP, targetsP, []string{branch})
	}

	// Identical drops.
	sort.Strings(nsB.drops)
	sort.Strings(nsP.drops)
	if fmt.Sprint(nsB.drops) != fmt.Sprint(nsP.drops) {
		t.Errorf("batched drops differ from per-branch drops:\nbatched: %v\nper-branch: %v", nsB.drops, nsP.drops)
	}
	// Identical events.
	evB, err := stB.QueryEvents(ctx, store.EventFilter{RepoID: repoB, EventTypes: []string{store.EvtBranchReap}})
	if err != nil {
		t.Fatal(err)
	}
	evP, err := stP.QueryEvents(ctx, store.EventFilter{RepoID: repoP, EventTypes: []string{store.EvtBranchReap}})
	if err != nil {
		t.Fatal(err)
	}
	if len(evB) != len(evP) {
		t.Fatalf("events: batched %d vs per-branch %d", len(evB), len(evP))
	}
	msgsB, msgsP := make([]string, 0, len(evB)), make([]string, 0, len(evP))
	for _, e := range evB {
		msgsB = append(msgsB, e.Message)
	}
	for _, e := range evP {
		msgsP = append(msgsP, e.Message)
	}
	sort.Strings(msgsB)
	sort.Strings(msgsP)
	for i := range msgsB {
		if msgsB[i] != msgsP[i] {
			t.Errorf("event %d differs:\nbatched:     %s\nper-branch:  %s", i, msgsB[i], msgsP[i])
		}
	}

	// Every probe must exist in both runs (same population), and the
	// batched run must not re-probe beyond the (targets x branches)
	// grid — i.e. exactly len(targets)*len(branches) probes, versus the
	// per-branch path's identical grid reached through P passes.
	wantProbes := len(targetsB) * len(branchesB)
	if nsB.probes != wantProbes {
		t.Errorf("batched probes = %d, want exactly %d (one grid pass)", nsB.probes, wantProbes)
	}
	if nsP.probes != wantProbes {
		t.Errorf("per-branch probes = %d, want %d", nsP.probes, wantProbes)
	}
}
