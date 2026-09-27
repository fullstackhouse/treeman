package wt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/stubbedev/treeman/internal/daemonctl"
	"github.com/stubbedev/treeman/pkg/rpc"
)

// EnsureDaemon tries to reach the daemon. If the probe fails it
// invokes `daemonctl.Start` inline (systemd / launchd when installed,
// else a detached binary), then polls the socket for up to ~2s.
// Returns nil when the daemon is reachable and speaks the expected
// protocol. A ProtocolMismatchError is returned as-is — restarting
// the daemon behind the user's command would paper over the stale
// binary instead of telling them to fix it.
func EnsureDaemon(ctx context.Context) error {
	err := probeDaemon(ctx)
	var pme *rpc.ProtocolMismatchError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &pme):
		return err
	}
	if _, err := daemonctl.Start(ctx); err != nil {
		return fmt.Errorf("daemon start: %w", err)
	}
	sock, _ := rpc.SocketPath()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			if probeDaemon(ctx) == nil {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not respond within 2s — %s", daemonDebugHint())
}

// probeDaemon reports whether the daemon answers status with the
// protocol version this CLI speaks. MethodStatus rather than ping:
// status has always carried ProtocolVersion/DaemonVersion, so even a
// binary from before response stamping identifies itself here.
func probeDaemon(ctx context.Context) error {
	_, err := rpc.Call(ctx, rpc.Request{Method: rpc.MethodStatus})
	return err
}

// daemonDebugHint returns a one-liner pointing at the right log
// surface for the current platform.
func daemonDebugHint() string {
	switch runtime.GOOS {
	case "darwin":
		return "check `log show --predicate 'process == \"treemand\"' --last 5m` and run `treeman doctor`"
	default:
		return "check `journalctl --user -u treemand -n 50` and run `treeman doctor`"
	}
}

// Note: teardown/finalize are dispatched to the daemon over the RPC
// socket (see dispatch.go + CallWithStart). There is deliberately no
// "detach a CLI child to do the work" fallback — when the daemon can't
// be reached even after an autostart attempt, delete runs the teardown
// in-process and create surfaces a hard error; nothing forks a
// `treeman worktree …` subprocess.
