package gitcmd

import (
	"context"
	"strings"
)

// branchBaseKey is the per-branch git config key recording the local base
// branch a treeman-created branch was forked off ("develop"). Stored as
// `branch.<name>.treemanBase` so git drops it together with the rest of the
// branch's config section on `git branch -D`.
//
// Why not the upstream: treeman creates branches with --no-track. Tracking
// the base (`origin/develop`) as the upstream meant a branch pushed without
// `-u` never showed `[gone]` once its remote branch was merged and deleted,
// so the merged-branch prune — and the durable reap riding on it — skipped
// it forever.
const branchBaseKey = "treemanBase"

// RecordBranchBase stores `base` as the base branch of `branch`, with any
// leading "origin/" stripped. Best-effort: a failure only costs the base
// lookup its first tier.
func RecordBranchBase(ctx context.Context, dir, branch, base string) {
	base = strings.TrimPrefix(base, "origin/")
	if branch == "" || base == "" || base == branch {
		return
	}
	_ = RunOptional(ctx, dir, "config", "branch."+branch+"."+branchBaseKey, base)
}

// BranchBase returns the base branch recorded by RecordBranchBase, or ""
// when none was recorded.
func BranchBase(ctx context.Context, dir, branch string) string {
	if branch == "" {
		return ""
	}
	out, err := String(ctx, dir, "config", "--get", "branch."+branch+"."+branchBaseKey)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
