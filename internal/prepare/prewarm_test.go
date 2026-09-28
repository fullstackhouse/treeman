package prepare

import (
	"context"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/stubbedev/treeman/internal/db/engineconn"
	"github.com/stubbedev/treeman/internal/snapshot"
	"github.com/stubbedev/treeman/internal/store"
)

// copyClaimConn is a spare-capable Conn whose claims COPY the spare
// (MySQL semantics): a spare stays claimable after use, so only the
// restorer's slot allocator can keep claims distinct and bounded.
type copyClaimConn struct {
	engineconn.Conn // unused methods panic via the nil embed
	mu              sync.Mutex
	claimed         map[string]int // spare → times claimed
}

func (c *copyClaimConn) DropSnapshot(context.Context, string) error { return nil }

func (c *copyClaimConn) CreateSnapshot(context.Context, string, string) error { return nil }

func (c *copyClaimConn) ClaimSpare(_ context.Context, spare, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.claimed[spare]++
	return nil
}

// TestSpareClaimRestoreDistinctBoundedSlots pins the pool contract for
// one prepare's concurrent restores (source + fanout clones): each
// spare is claimed at most once and a pool of N covers at most N
// restores — the rest fall back to the plain restore — even when a
// claim leaves the spare in place.
func TestSpareClaimRestoreDistinctBoundedSlots(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	conn := &copyClaimConn{claimed: map[string]int{}}
	se := spareEngineFromConn("mysql", conn)
	var plainMu sync.Mutex
	plain := 0
	restore := spareClaimRestore(se, st, 0, 0, 2, func(context.Context, string, string) error {
		plainMu.Lock()
		plain++
		plainMu.Unlock()
		return nil
	})

	const restores = 5
	var wg sync.WaitGroup
	for i := range restores {
		wg.Go(func() {
			if err := restore(ctx, "_tm_tpl", "target_"+strconv.Itoa(i)); err != nil {
				t.Errorf("restore: %v", err)
			}
		})
	}
	wg.Wait()

	for slot := 1; slot <= 2; slot++ {
		if n := conn.claimed[snapshot.SpareName("_tm_tpl", slot)]; n != 1 {
			t.Errorf("spare slot %d claimed %d times, want exactly 1", slot, n)
		}
	}
	if len(conn.claimed) != 2 {
		t.Errorf("claimed spares = %v, want only the 2 pool slots", conn.claimed)
	}
	if plain != restores-2 {
		t.Errorf("plain restores = %d, want %d (restores beyond the pool fall back)", plain, restores-2)
	}
}
