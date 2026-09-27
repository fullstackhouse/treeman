package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"testing"
)

// serveOnce answers exactly one request with the given response over
// a throwaway unix socket wired to $TREEMAN_SOCKET.
func serveOnce(t *testing.T, resp Response) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "treeman.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var req Request
		if derr := json.NewDecoder(conn).Decode(&req); derr != nil {
			return
		}
		e := json.NewEncoder(conn)
		if eerr := e.Encode(&resp); eerr != nil {
			_ = conn.Close()
		}
	}()
	t.Setenv(SocketEnv, sock)
}

func TestRequestRoundtripStatus(t *testing.T) {
	req := Request{Method: MethodStatus}
	b, err := json.Marshal(&req)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"method":"status"}` {
		t.Errorf("status payload: %s", string(b))
	}
	var got Request
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Method != MethodStatus {
		t.Errorf("method: %s", got.Method)
	}
}

func TestRequestRoundtripRunPlan(t *testing.T) {
	req := Request{
		Method: MethodRunPlan,
		RunPlan: &RunPlanArgs{
			RunID: "abcd1234",
			Wait:  true,
			Groups: [][]Task{
				{{
					Type:         TaskWorktreeFinalize,
					RepoPath:     "/repos/foo",
					WorktreePath: "/repos/foo/.worktrees/x",
					InheritedEnv: map[string]string{"PATH": "/usr/bin:/bin"},
				}},
				{
					{Type: TaskPrepare, WorktreePath: "/repos/foo/.worktrees/x"},
					{
						Type:         TaskWorktreeTeardown,
						WorktreePath: "/repos/foo/.worktrees/y",
						Params:       map[string]string{"force": "1"},
					},
				},
			},
		},
	}
	b, err := json.Marshal(&req)
	if err != nil {
		t.Fatal(err)
	}
	var got Request
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Method != MethodRunPlan || got.RunPlan == nil {
		t.Fatalf("method/args: %s %+v", got.Method, got.RunPlan)
	}
	if !got.RunPlan.Wait || got.RunPlan.RunID != "abcd1234" {
		t.Errorf("wait/run_id: %v %s", got.RunPlan.Wait, got.RunPlan.RunID)
	}
	if len(got.RunPlan.Groups) != 2 || len(got.RunPlan.Groups[1]) != 2 {
		t.Fatalf("groups shape: %+v", got.RunPlan.Groups)
	}
	g0 := got.RunPlan.Groups[0][0]
	if g0.Type != TaskWorktreeFinalize || g0.InheritedEnv["PATH"] != "/usr/bin:/bin" {
		t.Errorf("group0 task: %+v", g0)
	}
	if got.RunPlan.Groups[1][1].Params["force"] != "1" {
		t.Errorf("force param lost: %+v", got.RunPlan.Groups[1][1])
	}
}

// TestUnknownMethodDecodes confirms decode no longer rejects unknown
// methods — that validation moved to the daemon's Dispatch switch (which
// returns an "unknown method" error response). Decode just sets Method
// and leaves every args pointer nil.
// TestPlanConstructors checks rpc.Plan/One build the expected request and
// that it survives the nested-envelope round-trip.
func TestPlanConstructors(t *testing.T) {
	req := Plan(true, One(Task{Type: TaskPrepare, WorktreePath: "/wt"}))
	if req.Method != MethodRunPlan || req.RunPlan == nil {
		t.Fatalf("method/args: %s %+v", req.Method, req.RunPlan)
	}
	if !req.RunPlan.Wait || len(req.RunPlan.Groups) != 1 || len(req.RunPlan.Groups[0]) != 1 {
		t.Fatalf("shape: %+v", req.RunPlan)
	}
	b, err := json.Marshal(&req)
	if err != nil {
		t.Fatal(err)
	}
	var got Request
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.RunPlan == nil || got.RunPlan.Groups[0][0].Type != TaskPrepare {
		t.Fatalf("round-trip lost the task: %s", b)
	}
}

func TestUnknownMethodDecodes(t *testing.T) {
	var got Request
	if err := json.Unmarshal([]byte(`{"method":"nope"}`), &got); err != nil {
		t.Fatalf("decode should not error on unknown method: %v", err)
	}
	if got.Method != "nope" {
		t.Errorf("method: %q", got.Method)
	}
	if got.RunPlan != nil || got.RepoRegister != nil {
		t.Errorf("no args pointer should be set for an unknown method")
	}
}

// TestCallFlagsProtocolMismatch pins the stale-daemon gate: a response
// stamped with a foreign protocol version fails the call with the
// restart hint instead of surfacing later as a decode or
// unknown-method error, and the matching version passes through.
func TestCallFlagsProtocolMismatch(t *testing.T) {
	serveOnce(t, Response{Kind: KindOk, ProtocolVersion: ProtocolVersion - 1, DaemonVersion: "2.5.1"})
	_, err := Call(context.Background(), Request{Method: MethodStatus})
	var pme *ProtocolMismatchError
	if !errors.As(err, &pme) {
		t.Fatalf("expected *ProtocolMismatchError, got %v", err)
	}
	if pme.DaemonProtocol != ProtocolVersion-1 || pme.DaemonVersion != "2.5.1" {
		t.Errorf("mismatch fields = %+v", pme)
	}
	want := fmt.Sprintf("treemand v2.5.1 speaks protocol v%d but treeman expects v%d — run `treeman daemon restart`",
		ProtocolVersion-1, ProtocolVersion)
	if err.Error() != want {
		t.Errorf("mismatch text = %q, want %q", err.Error(), want)
	}

	serveOnce(t, Response{Kind: KindOk, ProtocolVersion: ProtocolVersion, DaemonVersion: "2.5.93"})
	if _, err := Call(context.Background(), Request{Method: MethodStatus}); err != nil {
		t.Errorf("matching protocol should pass: %v", err)
	}

	// An unstamped response (pre-stamping daemon, bare Pong) is not
	// flagged here — EnsureDaemon's MethodStatus probe owns that case.
	serveOnce(t, Response{Kind: KindPong})
	if _, err := Call(context.Background(), Request{Method: MethodPing}); err != nil {
		t.Errorf("unstamped response should pass: %v", err)
	}
}
