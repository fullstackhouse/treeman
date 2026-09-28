package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stubbedev/treeman/internal/runid"
	"github.com/stubbedev/treeman/internal/store"
	"github.com/stubbedev/treeman/pkg/rpc"
)

// shortSocketPath returns a unix-socket path short enough to bind on
// macOS (104-byte sun_path limit). t.TempDir() on macOS roots tests
// under /var/folders/<hash>/T/<TestName>/NNN/ which can exceed the
// limit; pinning the parent at os.TempDir() with a tiny basename
// keeps the path well inside the cap.
//
// CI hit `bind: invalid argument` on macos-latest before this helper
// landed. See: https://man7.org/linux/man-pages/man7/unix.7.html
// (Linux is 108 bytes, macOS is 104 — pick the lower bound).
var sockCounter atomic.Uint64

func shortSocketPath(t *testing.T) string {
	t.Helper()
	n := sockCounter.Add(1)
	p := filepath.Join(os.TempDir(), fmt.Sprintf("tm-%d-%d.sock", os.Getpid(), n))
	t.Cleanup(func() { _ = os.Remove(p) })
	return p
}

// startStreamServer binds a unix socket at a short path, points
// TREEMAN_SOCKET at it, and serves every accepted connection through
// DispatchStreaming — the streaming half of cmd/treemand's handleConn,
// inline to avoid importing the cmd package. Returns the daemon store
// events are written to.
func startStreamServer(ctx context.Context, t *testing.T) *store.Store {
	t.Helper()
	sockPath := shortSocketPath(t)
	t.Setenv("TREEMAN_SOCKET", sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	st := NewState(ctx, s)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				var req rpc.Request
				if err := json.NewDecoder(conn).Decode(&req); err != nil {
					return
				}
				if IsStreamingMethod(req.Method) {
					DispatchStreaming(ctx, st, conn, req)
				}
			}()
		}
	}()
	return s
}

// TestStreamingSubscribe_EndToEnd opens an rpc.SubscribeEvents stream,
// writes events to the daemon's store, and asserts that matching events
// arrive on the client channel. Covers the full hook-driven push path
// end-to-end — the foundation for logs_subscribe's "mode=push". No sleep
// between subscribe and write: SubscribeEvents returns only after the
// daemon acknowledged the live hook.
func TestStreamingSubscribe_EndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := startStreamServer(ctx, t)

	stream, stop, err := rpc.SubscribeEvents(ctx, rpc.EventSubscribeArgs{
		EventTypes: []string{"unit_test_match"},
	})
	if err != nil {
		t.Fatalf("SubscribeEvents: %v", err)
	}
	defer stop()

	// Write one matching + one non-matching event. Only the matching
	// one should arrive on the channel.
	_ = s.WriteEvent(ctx, store.LevelInfo, "unit_test_match", "yes", 0, 0, "", 0, nil)
	_ = s.WriteEvent(ctx, store.LevelInfo, "unit_test_skip", "no", 0, 0, "", 0, nil)

	select {
	case ev := <-stream:
		if ev.EventType != "unit_test_match" {
			t.Errorf("got event_type %q, want unit_test_match", ev.EventType)
		}
		if ev.Message != "yes" {
			t.Errorf("got message %q, want 'yes'", ev.Message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for matched event")
	}

	// Drain one more tick — the skipped event must NOT arrive.
	select {
	case ev := <-stream:
		t.Errorf("unexpected non-matching event delivered: %+v", ev)
	case <-time.After(200 * time.Millisecond):
		// OK
	}
}

// TestStreamingSubscribe_LevelFilter — confirms the daemon-side
// filter respects Levels (case-insensitive). Sends two events, only
// the matching level should come through.
func TestStreamingSubscribe_LevelFilter(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := startStreamServer(ctx, t)

	stream, stop, err := rpc.SubscribeEvents(ctx, rpc.EventSubscribeArgs{
		Levels: []string{"error"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	_ = s.WriteEvent(ctx, store.LevelInfo, "noise", "info-level", 0, 0, "", 0, nil)
	_ = s.WriteEvent(ctx, store.LevelError, "boom", "error-level", 0, 0, "", 0, nil)

	select {
	case ev := <-stream:
		if ev.Level != "error" {
			t.Errorf("got level %q, want error", ev.Level)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}
	select {
	case ev := <-stream:
		t.Errorf("unexpected extra event: %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestStreamingSubscribe_NoRegistrationRace pins #117: an event written
// the instant SubscribeEvents returns must be delivered. Before the
// KindSubscribed handshake the client returned before the daemon had
// registered its hook, so a plan that failed in milliseconds (a config
// parse error) emitted its terminal plan:error into the void and
// `worktree create --foreground` waited forever.
func TestStreamingSubscribe_NoRegistrationRace(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := startStreamServer(ctx, t)

	for i := range 50 {
		id := runid.New()
		msg := fmt.Sprintf("run-%d", i)
		stream, stop, err := rpc.SubscribeEvents(ctx, rpc.EventSubscribeArgs{RunID: id})
		if err != nil {
			t.Fatalf("SubscribeEvents #%d: %v", i, err)
		}
		_ = s.WriteEvent(runid.With(ctx, id), store.LevelError, store.EvtPlanError, msg, 0, 0, "", 0, nil)
		select {
		case ev := <-stream:
			if ev.Message != msg {
				t.Fatalf("#%d: got %q, want %q", i, ev.Message, msg)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("#%d: event written right after subscribe was never delivered", i)
		}
		stop()
	}
}

// TestSubscribeEvents_HandshakeRequired: a peer that accepts the
// subscription but never acknowledges it (a treemand predating the
// handshake) fails SubscribeEvents with a restart hint instead of
// handing back a stream that may already have missed events.
func TestSubscribeEvents_HandshakeRequired(t *testing.T) {
	sockPath := shortSocketPath(t)
	t.Setenv("TREEMAN_SOCKET", sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var req rpc.Request
		_ = json.NewDecoder(conn).Decode(&req)
		<-t.Context().Done() // hold the stream open, never ack
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	_, _, err = rpc.SubscribeEvents(ctx, rpc.EventSubscribeArgs{})
	if err == nil || !strings.Contains(err.Error(), "did not acknowledge") {
		t.Fatalf("SubscribeEvents against a non-acking peer = %v, want the acknowledge error", err)
	}
}
