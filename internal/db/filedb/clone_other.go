//go:build !linux

package filedb

import (
	"errors"
	"os"
)

// errReflinkUnsupported stands in for a reflink primitive off Linux;
// the caller's io.Copy fallback always runs.
var errReflinkUnsupported = errors.New("reflink unsupported on this platform")

// reflink is unsupported off Linux; the caller's io.Copy fallback
// always runs.
func reflink(_, _ *os.File) error {
	return errReflinkUnsupported
}
