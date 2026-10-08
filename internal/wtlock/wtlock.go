// Package wtlock serialises per-worktree work ACROSS processes.
//
// The daemon's per-worktree guards (MarkFinalizeInFlight,
// LockWorktreePrepare) are in-process mutexes, but prepare.Run is also
// reached from other processes: `treeman worktree finalize --local`
// and the MCP server both run it inline. Issue #123: a `finalize
// --local` issued while the daemon's detached create tail was still
// building the same worktree dropped and dump-loaded the testing DB
// under the daemon's feet; the daemon's migrate died half-way and the
// test DB was left on a stale schema.
//
// Locks are advisory files under `<state dir>/locks/`, keyed by kind +
// a hash of the worktree path, so two worktrees never contend.
package wtlock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/stubbedev/treeman/internal/store"
)

// Lock kinds. A caller holding Finalize may take Prepare inside it;
// never the other way round.
const (
	// Finalize covers a whole setup + prepare tail (hooks included).
	Finalize = "finalize"
	// Prepare covers one prepare.Run / RunFiltered call.
	Prepare = "prepare"
)

// pollInterval is how often a blocked Acquire retries.
var pollInterval = 200 * time.Millisecond

// errBusy is what the platform tryLock returns when another holder has
// the lock.
var errBusy = errors.New("lock busy")

// Path returns the lock file for (kind, wtPath).
func Path(kind, wtPath string) (string, error) {
	dbPath, err := store.DefaultDBPath()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(wtPath)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(filepath.Clean(abs)))
	return filepath.Join(filepath.Dir(dbPath), "locks",
		kind+"-"+hex.EncodeToString(sum[:8])+".lock"), nil
}

// Acquire takes the exclusive (kind, wtPath) lock, blocking until it is
// free or ctx is done. onWait (optional) runs once, the first time the
// lock is found held, so callers can tell the user why they are paused.
// The returned func releases the lock.
//
// The lock is tied to an open file, so a holder that crashes releases
// it with its file descriptors — no stale-lock recovery is needed.
func Acquire(ctx context.Context, kind, wtPath string, onWait func()) (func(), error) {
	p, err := Path(kind, wtPath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	waited := false
	for {
		err := tryLock(f)
		if err == nil {
			return func() {
				_ = unlock(f)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, errBusy) {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: %w", p, err)
		}
		if !waited && onWait != nil {
			onWait()
		}
		waited = true
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}
