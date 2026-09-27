package cmd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/treeman/internal/store"
)

// TestFollowLoopBannerAndGapMarker pins the #101 affordances: the
// watching banner lands on STDERR in human mode only (stdout stays
// pipe-clean for --json), and a batch arriving after a >2-minute gap
// gets a timestamp separator before it.
func TestFollowLoopBannerAndGapMarker(t *testing.T) {
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	t.Run("banner on stderr in human mode, absent in json", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
		defer cancel()
		sout, serr := captureOutErr(t, func() {
			_ = followLoop(ctx, st, store.EventFilter{}, 0, eventStyle{})
		})
		if !strings.Contains(serr, "following") {
			t.Errorf("human mode should print the watching banner to stderr, got: %q", serr)
		}
		if sout != "" {
			t.Errorf("human-mode stdout should stay clean, got: %q", sout)
		}
	})

	t.Run("json mode stays byte-clean", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
		defer cancel()
		sout, serr := captureOutErr(t, func() {
			_ = followLoop(ctx, st, store.EventFilter{}, 0, eventStyle{asJSON: true})
		})
		if strings.Contains(sout, "following") || strings.Contains(serr, "following") {
			t.Errorf("json mode must not print the banner:\nstdout:%s\nstderr:%s", sout, serr)
		}
	})

	t.Run("gap separator before an idle-gapped batch", func(t *testing.T) {
		// Seed one event, then backdate its ts 3 minutes; the first
		// poll's batch arrives gapped from the loop's start, so the
		// separator must precede it. Setup uses a fresh ctx (the outer
		// one may already be cancelled by earlier subtests).
		if err := st.WriteEvent(context.Background(), store.LevelInfo, store.EvtPrepareStart, "gapped", 0, 0, "", 0, nil); err != nil {
			t.Fatalf("WriteEvent: %v", err)
		}
		old := time.Now().Add(-3 * time.Minute).UnixMilli()
		if _, err := st.DB.ExecContext(context.Background(), `UPDATE events SET ts = ?`, old); err != nil {
			t.Fatalf("backdate: %v", err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		sout, serr := captureOutErr(t, func() {
			_ = followLoop(ctx, st, store.EventFilter{}, 0, eventStyle{})
		})
		if !strings.Contains(serr, "──── ") {
			t.Errorf("gap separator expected on stderr:\nstderr:%s\nstdout:%s", serr, sout)
		}
	})
}
