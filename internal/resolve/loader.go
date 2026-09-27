package resolve

import (
	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/slug"
)

// LoadResolved is the canonical "load + fill-in-credentials"
// helper. Reads the layered YAML config rooted at `repoRoot`, then
// applies env-file resolution so `cfg.Connections.*` is fully
// populated from `.env` / `.env.testing` / `.env.local` etc. even
// when the YAML omits the corresponding block.
//
// Why this exists: every consumer of `config.LoadLayered` was
// supposed to call `ApplyEnvCredentials` afterwards. Easy to miss,
// and a missed call surfaced as confusing "connections.mysql not
// configured" errors when the .env had the creds. Wrapping the two
// in one function makes the expected use-site the same length as
// the old one.
//
// Call this instead of `config.LoadLayered` outside of debug /
// inspection contexts (e.g. `treeman config show`).
func LoadResolved(repoRoot string) (config.Config, error) {
	return loadResolvedCached(repoRoot, "", func() (config.Config, error) {
		cfg, err := config.LoadLayered(repoRoot)
		if err != nil {
			return cfg, err
		}
		ApplyEnvCredentials(&cfg, repoRoot)
		applyRedisDBRange(&cfg)
		return cfg, nil
	})
}

// LoadResolvedForWorktree mirrors LoadResolved but uses the
// worktree's checkout for env-file resolution. Lets per-worktree
// `.env.testing` (post env-scoping patches) override the main
// repo's defaults.
func LoadResolvedForWorktree(mainRoot, wtRoot string) (config.Config, error) {
	return loadResolvedCached(mainRoot, wtRoot, func() (config.Config, error) {
		cfg, err := config.LoadLayeredForWorktree(mainRoot, wtRoot)
		if err != nil {
			return cfg, err
		}
		if wtRoot != "" && wtRoot != mainRoot {
			// Main root as base layer, worktree overrides: a fresh
			// worktree's env copies land only mid-finalize
			// (worktrees.copies), so its files may not exist yet —
			// the main checkout's fill any gap.
			ApplyEnvCredentials(&cfg, mainRoot, wtRoot)
		} else {
			ApplyEnvCredentials(&cfg, mainRoot)
		}
		applyRedisDBRange(&cfg)
		return cfg, nil
	})
}

// applyRedisDBRange pins the slug → redis-index window from
// connections.redis db_min/db_max (hosted tiers exposing <16 logical
// DBs). Config validation bounds the values; the default 6..15 stays
// untouched when redis isn't configured or the knobs are omitted.
func applyRedisDBRange(cfg *config.Config) {
	if r := cfg.Connections.Redis; r != nil {
		lo, hi := r.EffectiveDBRange()
		_ = slug.SetRedisDBRange(lo, hi)
	}
}
