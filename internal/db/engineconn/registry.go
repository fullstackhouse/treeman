// Driver registry — the table that removes the per-family switches
// from engineconn.Connect/Configured (#47). Each engine family
// registers one DriverFactory; adding an engine means adding a
// registry entry (plus its driver package), not editing switch arms.
package engineconn

import (
	"context"
	"sync"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/engine"
)

// DriverFactory is one engine family's wiring into treeman: how to
// check for a connection block and how to dial it into a uniform Conn.
type DriverFactory struct {
	// Configured reports whether cfg carries a usable connection for
	// (family, name) — the cheap "is it wired up" check callers use to
	// distinguish "not configured" from "configured but unreachable".
	// An empty name selects the singular block (the historical shape).
	Configured func(cfg *config.Config, name string) bool
	// Connect dials (family, name), returning (nil, false, nil) when no
	// connection block is present, (nil, true, err) when dialing a
	// configured engine failed, and (conn, true, nil) on success. The
	// caller owns conn.Close.
	Connect func(ctx context.Context, cfg *config.Config, name string) (Conn, bool, error)
}

var (
	regMu   sync.RWMutex
	drivers = map[engine.Family]DriverFactory{}
)

// Register installs (or, in tests, replaces) the factory for `fam`.
// Production registration happens in the explicit populate list in
// registry_drivers.go; tests may register fakes to prove new surfaces
// are registry-driven.
func Register(fam engine.Family, f DriverFactory) {
	regMu.Lock()
	defer regMu.Unlock()
	drivers[fam] = f
}

// Factory returns the registered factory for `fam` and whether one
// exists at all.
func Factory(fam engine.Family) (DriverFactory, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	f, ok := drivers[fam]
	return f, ok
}
