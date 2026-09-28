//go:build e2e

package cli_surface_e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stubbedev/treeman/pkg/rpc"
)

// TestPkgRPCClientIntegration exercises pkg/rpc the way an EXTERNAL
// Go tool would (#100): dial $TREEMAN_SOCKET with only the promoted
// package, complete `ping` (typed status response), and drive a real
// `run_plan` (worktree_register) through to per-task results.
func TestPkgRPCClientIntegration(t *testing.T) {
	binDir := sharedDaemonBinDir(t)

	sockDir, err := os.MkdirTemp("", "tmd-pkg-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "treeman.sock")

	cmd := exec.Command(filepath.Join(binDir, "treemand"))
	cmd.Env = append(os.Environ(), "TREEMAN_SOCKET="+sock)
	// rpc.Call below resolves the socket from THIS process's env, so
	// pin it here too — otherwise it dials the default path and talks
	// to whatever treemand the host happens to run (or nothing, on CI).
	t.Setenv("TREEMAN_SOCKET", sock)
	if err := cmd.Start(); err != nil {
		t.Skipf("treemand start: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	// The repo the plan will register — a real git tree so
	// worktree_register's EnsureRepo path has something to probe.
	repo := newGitRepo(t)

	// ping first: poll until the daemon has bound the socket.
	var status *rpc.Response
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		resp, err := rpc.Call(ctx, rpc.Request{Method: rpc.MethodPing})
		if err == nil {
			status = &resp
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if status == nil {
		t.Fatal("pkg/rpc ping never succeeded against the live daemon")
	}
	if status.Kind != rpc.KindPong {
		t.Errorf("ping kind = %q, want %q", status.Kind, rpc.KindPong)
	}
	if status.ProtocolVersion != rpc.ProtocolVersion {
		t.Errorf("daemon protocol = %d, client = %d", status.ProtocolVersion, rpc.ProtocolVersion)
	}
	if status.DaemonVersion == "" {
		t.Error("ping response should carry the daemon version")
	}

	// run_plan with Wait=true: one worktree_register lane, asserted
	// through the typed TaskResult.
	plan := rpc.Plan(true, rpc.One(rpc.Task{
		Type:         rpc.TaskWorktreeRegister,
		RepoPath:     repo,
		WorktreePath: repo,
		Params:       map[string]string{rpc.ParamBranch: "master"},
	}))
	result, err := rpc.Call(ctx, plan)
	if err != nil {
		t.Fatalf("run_plan: %v", err)
	}
	if result.Kind != rpc.KindPlanResult {
		t.Fatalf("run_plan kind = %q, want %q (payload: %+v)", result.Kind, rpc.KindPlanResult, result)
	}
	if len(result.TaskResults) != 1 || !result.TaskResults[0].OK {
		t.Fatalf("plan results = %+v, want one ok task", result.TaskResults)
	}
	if result.TaskResults[0].Type != rpc.TaskWorktreeRegister {
		t.Errorf("task type = %q, want %q", result.TaskResults[0].Type, rpc.TaskWorktreeRegister)
	}
}
