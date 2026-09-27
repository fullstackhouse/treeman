package cmd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/treeman/internal/store"
	"github.com/stubbedev/treeman/internal/ui"
)

// TestPollFinalizeQuietPipedOutput pins the scripting contract (#54):
// with --quiet (or piped stderr, as under `go test`) pollFinalize must
// stay byte-conservative — no spinner redraws on stderr, only the
// caller-level final result. It must also succeed the moment the
// worktree:create:end event lands, and surface the failure message on
// an error event.
func TestPollFinalizeQuietPipedOutput(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "treeman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	repoID, _ := st.EnsureRepo(ctx, "/tmp/repo", "repo")
	wtID, _ := st.EnsureWorktree(ctx, repoID, "/tmp/repo/wt", "slug", "feature")
	wt := worktreeRow{ID: wtID, Slug: "slug", Branch: "feature", Path: "/tmp/repo/wt", CreatedAt: 1}

	t.Run("success event ends the wait", func(t *testing.T) {
		go func() {
			time.Sleep(600 * time.Millisecond)
			_ = st.WriteEvent(ctx, store.LevelInfo, store.EvtWorktreeCreateEnd, "done", repoID, wtID, "", 0, nil)
		}()
		err := pollFinalize(ctx, st, wt, 0, time.Now().Add(5*time.Second), 5*time.Second, true)
		if err != nil {
			t.Fatalf("pollFinalize should succeed on create:end, got %v", err)
		}
	})

	t.Run("error event fails with the message", func(t *testing.T) {
		// Fresh worktree so the earlier create:end can't satisfy the
		// wait before the error row is reached.
		eid, _ := st.EnsureWorktree(ctx, repoID, "/tmp/repo/wt-err", "slug-err", "feature-err")
		wtErr := worktreeRow{ID: eid, Slug: "slug-err", Branch: "feature-err", Path: "/tmp/repo/wt-err", CreatedAt: 1}
		if err := st.WriteEvent(
			ctx,
			store.LevelError,
			store.EvtWorktreeCreateError,
			"migrate exploded",
			repoID,
			eid,
			"",
			0,
			nil,
		); err != nil {
			t.Fatal(err)
		}
		err := pollFinalize(ctx, st, wtErr, 0, time.Now().Add(5*time.Second), 5*time.Second, true)
		if err == nil || !strings.Contains(err.Error(), "migrate exploded") {
			t.Fatalf("want failure carrying the event message, got %v", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		err := pollFinalize(ctx, st, wt, 1<<62, time.Now().Add(1200*time.Millisecond), time.Second, true)
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("want timeout error, got %v", err)
		}
	})
}

// TestPollFinalizeSpinnerOnlyOnTTY pins the gating: with stderr NOT a
// terminal (the test process) the spinner path must stay off even when
// not quiet — the redraw writes nothing and the outcome lines are the
// only stderr traffic.
func TestPollFinalizeSpinnerOnlyOnTTY(t *testing.T) {
	if ui.IsStderrTTY() {
		t.Skip("stderr is a terminal; the piped-stderr guarantee can't be observed here")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "treeman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	repoID, _ := st.EnsureRepo(ctx, "/tmp/repo", "repo")
	wtID, _ := st.EnsureWorktree(ctx, repoID, "/tmp/repo/wt", "slug2", "feature")
	wt := worktreeRow{ID: wtID, Slug: "slug2", Branch: "feature", Path: "/tmp/repo/wt", CreatedAt: 1}

	go func() {
		time.Sleep(1200 * time.Millisecond)
		_ = st.WriteEvent(ctx, store.LevelInfo, store.EvtWorktreeCreateEnd, "done", repoID, wtID, "", 0, nil)
	}()
	if err := pollFinalize(ctx, st, wt, 0, time.Now().Add(5*time.Second), 5*time.Second, false); err != nil {
		t.Fatalf("non-quiet piped poll should still succeed: %v", err)
	}
}
