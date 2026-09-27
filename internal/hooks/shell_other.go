//go:build !unix

package hooks

import (
	"fmt"
	"os/exec"
	"runtime"
)

// shellCommand is the non-unix stub: treeman hooks are `/bin/sh -c`
// commands detached into their own session, which has no equivalent on
// platforms without a POSIX shell and Setsid. Fail with the platform
// statement up front instead of scattering syscall compile errors or,
// worse, silently running hooks differently (#89).
func shellCommand(cmdStr string) (*exec.Cmd, error) {
	return nil, fmt.Errorf(
		"hook %q cannot run: treeman hooks are unsupported on %s (they require /bin/sh and setsid); supported platforms are linux, macOS, and the BSDs",
		cmdStr, runtime.GOOS)
}
