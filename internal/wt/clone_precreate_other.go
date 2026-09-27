//go:build !darwin

package wt

import "os"

// tryPathClone is the non-darwin stub of the pre-create clone seam:
// only APFS needs a destination that doesn't exist yet, and Linux's
// FICLONE path clones into an already-opened fd instead (see
// cloneFile). Always false — copyRegularFile proceeds to io.Copy.
func tryPathClone(_, _ string, _ os.FileMode) (int64, bool) {
	return 0, false
}
