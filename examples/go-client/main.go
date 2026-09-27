// Command go-client is the sample external consumer of pkg/rpc
// (#100): it dials $TREEMAN_SOCKET (or the default socket path),
// pings the daemon, and — when a repo path is passed — runs a
// worktree_register plan. It imports ONLY the promoted package, the
// same way an out-of-tree module would.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/stubbedev/treeman/pkg/rpc"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "go-client:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	status, err := rpc.Call(ctx, rpc.Request{Method: rpc.MethodPing})
	if err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	fmt.Printf("daemon v%s (protocol %d, pid %d)\n",
		status.DaemonVersion, status.ProtocolVersion, status.Pid)

	if len(os.Args) < 2 {
		return nil
	}
	repo := os.Args[1]
	result, err := rpc.Call(ctx, rpc.Plan(true, rpc.One(rpc.Task{
		Type:         rpc.TaskWorktreeRegister,
		RepoPath:     repo,
		WorktreePath: repo,
	})))
	if err != nil {
		return fmt.Errorf("run_plan: %w", err)
	}
	if result.Kind != rpc.KindPlanResult {
		return fmt.Errorf("unexpected response kind %q", result.Kind)
	}
	for _, tr := range result.TaskResults {
		if !tr.OK {
			return fmt.Errorf("task %s failed: %s", tr.Type, tr.Message)
		}
	}
	fmt.Printf("registered %s\n", repo)
	return nil
}
