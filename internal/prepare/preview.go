package prepare

import (
	"context"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/gitenv"
	"github.com/stubbedev/treeman/internal/slug"
	"github.com/stubbedev/treeman/internal/template"
)

// PlanEntry is one databases[] entry's teardown/reset preview: the
// namespaces that would be removed, rendered WITHOUT touching any
// engine (#60). Drives `wt delete --dry-run` and `db reset --dry-run`.
type PlanEntry struct {
	Engine string   `json:"engine"`
	Kind   string   `json:"kind"` // template | branch_scoped | prefix
	Names  []string `json:"names"`
}

// PreviewTeardown renders every cfg.Databases entry's drop targets for
// `worktreePath` the way TeardownDatabases / ResetBranchScoped would
// compute them — pure name math, no engine connections. `slugValue` is
// the worktree's registered slug ({slug} in templates); pass "" to
// derive it from the path. `repoRoot` feeds the auto test-clone count
// probe; `currentBranch` (detected by the caller via git) names the
// durable copy a reset would drop ("" to skip it).
func PreviewTeardown(cfg *config.Config, repoRoot, worktreePath, slugValue, currentBranch string) ([]PlanEntry, error) {
	if slugValue == "" {
		slugValue = slug.For(worktreePath, "").Value
	}
	tplCtx := template.FromSlug(slug.Slug{Value: slugValue, Source: slug.SourceTicket})
	out := make([]PlanEntry, 0, len(cfg.Databases))
	for _, d := range cfg.Databases {
		if d.BranchScoped {
			scope, eng, ok := branchScopeFor(d.Engine)
			if !ok {
				continue
			}
			active, err := activeNamespace(d, scope, worktreePath)
			if err != nil {
				return nil, err
			}
			names := []string{active}
			if currentBranch != "" {
				// durable() only hashes names — the engine adapter is
				// built from scope+engine strings, never connected.
				be := &branchEngine{scope: scope, engine: eng}
				names = append(names, be.durable(active, currentBranch))
			}
			out = append(out, PlanEntry{Engine: d.Engine, Kind: "branch_scoped", Names: names})
			continue
		}
		if d.NameTemplate != "" {
			source, err := template.Render(d.NameTemplate, tplCtx)
			if err != nil {
				return nil, err
			}
			clones, err := resolveCloneNames(d.TestClones, tplCtx, repoRoot)
			if err != nil {
				return nil, err
			}
			names := make([]string, 0, 1+len(clones))
			names = append(names, source)
			names = append(names, clones...)
			out = append(out, PlanEntry{Engine: d.Engine, Kind: "template", Names: names})
			continue
		}
		if d.KeyPrefix != "" {
			prefix, err := template.Render(d.KeyPrefix, tplCtx)
			if err != nil {
				return nil, err
			}
			out = append(out, PlanEntry{Engine: d.Engine, Kind: "prefix", Names: []string{prefix}})
		}
	}
	return out, nil
}

// PreviewBranchReset is PreviewTeardown filtered to the branch_scoped
// databases `db reset` would drop and re-seed.
func PreviewBranchReset(cfg *config.Config, repoRoot, worktreePath, slugValue, currentBranch string) ([]PlanEntry, error) {
	all, err := PreviewTeardown(cfg, repoRoot, worktreePath, slugValue, currentBranch)
	if err != nil {
		return nil, err
	}
	out := make([]PlanEntry, 0, len(all))
	for _, e := range all {
		if e.Kind == "branch_scoped" {
			out = append(out, e)
		}
	}
	return out, nil
}

// CurrentBranchOf is a convenience for callers that only have a
// worktree path: the worktree's checked-out branch, or "" when it
// can't be detected (detached/removed).
func CurrentBranchOf(ctx context.Context, worktreePath string) string {
	return gitenv.DetectBranch(ctx, worktreePath)
}
