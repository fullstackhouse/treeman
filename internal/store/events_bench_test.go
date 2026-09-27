package store

import (
	"context"
	"path/filepath"
	"testing"
)

// benchBatchedStore opens a throwaway store with the event batcher
// running (the daemon's steady-state shape) and one event hook
// registered (the streaming-subscriber shape), so the benchmark
// measures the exact path a live treemand pays per event.
func benchBatchedStore(b *testing.B) *Store {
	b.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := Open(ctx, filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		cancel()
		b.Fatal(err)
	}
	b.Cleanup(func() {
		cancel()
		_ = s.Close()
	})
	s.StartEventBatcher(ctx)
	s.RegisterEventHook("bench", func(Event) {})
	return s
}

// BenchmarkWriteEventNilPayload measures the batched enqueue path for
// the most common daemon event shape: no structured payload.
func BenchmarkWriteEventNilPayload(b *testing.B) {
	s := benchBatchedStore(b)
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		_ = s.WriteEvent(ctx, LevelInfo, "bench_event", "hot path probe", 1, 2, "", 0, nil)
	}
}

// BenchmarkWriteEventPayload measures the batched enqueue path with a
// small string-map payload — the finalize/plan event shape.
func BenchmarkWriteEventPayload(b *testing.B) {
	s := benchBatchedStore(b)
	ctx := context.Background()
	payload := map[string]string{"path": "/repo/.worktrees/dev", "wt": "dev"}
	b.ReportAllocs()
	for b.Loop() {
		_ = s.WriteEvent(ctx, LevelInfo, "bench_event", "hot path probe", 1, 2, "prepare", 42, payload)
	}
}

// BenchmarkFireEventHooks isolates the hook fan-out cost — the part
// every registered subscriber adds to every event.
func BenchmarkFireEventHooks(b *testing.B) {
	s := benchBatchedStore(b)
	ev := Event{ID: 7, Ts: 1, Level: LevelInfo, EventType: "bench_event", Message: "m"}
	b.ReportAllocs()
	for b.Loop() {
		s.fireEventHooks(ev)
	}
}
