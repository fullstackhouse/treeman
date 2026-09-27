package daemon

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"testing"

	"github.com/stubbedev/treeman/internal/store"
	"github.com/stubbedev/treeman/pkg/rpc"
)

func benchEnvelopes(n int) chan rpc.EventEnvelope {
	ch := make(chan rpc.EventEnvelope, n)
	for range n {
		ch <- rpc.EventEnvelope{
			ID: 42, Ts: 1730000000000, Level: "info", EventType: "db:prepare:end",
			Phase: "prepare", Message: "mysql prepared in 1.2s (strategy=clone)",
			PayloadJSON: `{"engine":"mysql","strategy":"clone"}`,
			RepoID:      1, WorktreeID: 7, DurationMs: 1234,
		}
	}
	return ch
}

// BenchmarkStreamEventEncode measures the per-event cost of the
// daemon-side streaming write: one KindEvent envelope framed onto the
// connection. The baseline shape encodes straight to the connection
// with a json.Encoder; the batched variant coalesces queued envelopes
// into one reusable buffer.
func BenchmarkStreamEventEncode(b *testing.B) {
	enc := json.NewEncoder(io.Discard)
	b.ReportAllocs()
	for b.Loop() {
		ev := rpc.EventEnvelope{
			ID: 42, Ts: 1730000000000, Level: "info", EventType: "db:prepare:end",
			Phase: "prepare", Message: "mysql prepared in 1.2s (strategy=clone)",
			PayloadJSON: `{"engine":"mysql","strategy":"clone"}`,
			RepoID:      1, WorktreeID: 7, DurationMs: 1234,
		}
		resp := rpc.Response{Kind: rpc.KindEvent, Event: &ev}
		if err := enc.Encode(&resp); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkStreamEventEncodeBatched measures the coalesced shape:
// encodeEventBatch drains every already-queued envelope into a reused
// buffer, so per-event cost drops to one buffered encode plus one
// connection write per batch.
func BenchmarkStreamEventEncodeBatched(b *testing.B) {
	const batch = 256
	buf := new(bytes.Buffer)
	enc := json.NewEncoder(buf)
	b.ReportAllocs()
	for b.Loop() {
		buf.Reset()
		ch := benchEnvelopes(batch)
		first := <-ch
		encodeEventBatch(enc, first, ch)
		if _, err := io.Copy(io.Discard, bytes.NewReader(buf.Bytes())); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkToEnvelope isolates the store.Event → wire envelope copy.
func BenchmarkToEnvelope(b *testing.B) {
	ev := store.Event{
		ID: 42, Ts: 1730000000000, Level: "info", EventType: "db:prepare:end",
		Phase: "prepare", Message: "mysql prepared (strategy=clone)",
		PayloadJSON: `{"engine":"mysql"}`,
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = toEnvelope(ev)
	}
}

// BenchmarkBuildSubscribeFilter exercises the per-event predicate of a
// fully-populated subscription (all filter fields set, so every clause
// runs).
func BenchmarkBuildSubscribeFilter(b *testing.B) {
	args := rpc.EventSubscribeArgs{
		RepoPath:    "/home/dev/app",
		Levels:      []string{"info", "warn", "error"},
		EventTypes:  []string{"db:prepare:end", "wt:create:end"},
		Phases:      []string{"prepare", "finalize"},
		PayloadLike: "%clone%",
	}
	filter := buildSubscribeFilter(args, 1, 7)
	ev := store.Event{
		ID: 42, Ts: 1730000000000, Level: "info", EventType: "db:prepare:end",
		Phase: "prepare", Message: "mysql prepared",
		PayloadJSON: `{"engine":"mysql","strategy":"clone"}`,
		RepoID:      sql.NullInt64{Int64: 1, Valid: true},
		WorktreeID:  sql.NullInt64{Int64: 7, Valid: true},
	}
	b.ReportAllocs()
	for b.Loop() {
		if !filter(ev) {
			b.Fatal("event should match")
		}
	}
}
