//go:build windows

package daemon

import (
	"errors"
	"net"
)

// CheckPeerUID — windows stub. The peer check relies on unix-domain
// socket file ownership, and the daemon itself depends on POSIX
// process semantics (setsid hooks, flock) that windows doesn't offer.
// Fail loudly and specifically instead of silently weakening the
// trust boundary (#89).
func CheckPeerUID(c net.Conn) error {
	return errors.New("peer uid check: unsupported platform (treeman's daemon runs on linux, macOS, and the BSDs)")
}
