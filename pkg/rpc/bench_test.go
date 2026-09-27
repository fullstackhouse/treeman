package rpc

import (
	"encoding/json"
	"testing"
)

func benchEventResponse() *Response {
	return &Response{
		Kind: KindEvent,
		Event: &EventEnvelope{
			ID:          42,
			Ts:          1730000000000,
			Level:       "info",
			EventType:   "db:prepare:end",
			Phase:       "prepare",
			Message:     "mysql prepared in 1.2s (strategy=clone)",
			PayloadJSON: `{"engine":"mysql","strategy":"clone","db_idx":0}`,
			RepoID:      1,
			WorktreeID:  7,
			DurationMs:  1234,
		},
	}
}

// BenchmarkEncodeEventResponse measures the daemon-side cost of
// emitting one streamed event envelope — the per-event hot path of
// logs_subscribe / the TUI event feed.
func BenchmarkEncodeEventResponse(b *testing.B) {
	resp := benchEventResponse()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := json.Marshal(resp); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDecodePlanRequest measures the client→daemon request decode
// cost for a realistic run_plan payload.
func BenchmarkDecodePlanRequest(b *testing.B) {
	wire, err := json.Marshal(Plan(true,
		One(Task{
			Type:         "worktree_create",
			RepoPath:     "/home/dev/app",
			WorktreePath: "/home/dev/app/.worktrees/feature-x",
			Params:       map[string]string{"branch": "feature-x", "engine_filter": "mysql"},
			InheritedEnv: map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/home/dev"},
		}),
	))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		var req Request
		if err := json.Unmarshal(wire, &req); err != nil {
			b.Fatal(err)
		}
	}
}
