package engineconn

import (
	"testing"

	"github.com/stubbedev/treeman/internal/config"
	dbmysql "github.com/stubbedev/treeman/internal/db/mysql"
	dbpostgres "github.com/stubbedev/treeman/internal/db/postgres"
	"github.com/stubbedev/treeman/internal/engine"
)

// TestPrewarmCapableEnginesMatchCapabilities pins the #53 contract the
// other way round: every family config.Validate admits for `prewarm`
// must have a conn that implements BOTH spare capabilities
// (SnapshotCreator + SpareClaimer), and no other family does. Without
// this cross-check, config's static allowlist and the drivers' actual
// interfaces could drift apart silently.
func TestPrewarmCapableEnginesMatchCapabilities(t *testing.T) {
	conns := map[string]Conn{
		"mysql":    NewMySQLConn(&dbmysql.Driver{}),
		"postgres": NewPostgresConn(&dbpostgres.Driver{}),
	}
	for fam := range config.PrewarmCapableEngines {
		conn, ok := conns[fam]
		if !ok {
			t.Errorf("config admits prewarm for %q but engineconn has no conn adapter", fam)
			continue
		}
		if _, ok := conn.(SnapshotCreator); !ok {
			t.Errorf("family %q: conn lacks SnapshotCreator but config admits prewarm", fam)
		}
		if _, ok := conn.(SpareClaimer); !ok {
			t.Errorf("family %q: conn lacks SpareClaimer but config admits prewarm", fam)
		}
	}
	// And the converse: engines NOT in the allowlist must not sneak in.
	for _, eng := range engine.Known {
		if config.PrewarmCapableEngines[eng] {
			continue
		}
		fam, ok := engine.Canonical(eng)
		if !ok {
			continue
		}
		if fam == engine.FamilyMySQL || fam == engine.FamilyPostgres {
			continue
		}
		conn, ok := conns[string(fam)]
		if !ok {
			continue
		}
		if _, ok := conn.(SpareClaimer); ok {
			t.Errorf("family %q implements SpareClaimer but config rejects prewarm for it — extend PrewarmCapableEngines", fam)
		}
	}
}
