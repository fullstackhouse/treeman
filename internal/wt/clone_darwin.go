//go:build darwin

package wt

import (
	"os"

	"golang.org/x/sys/unix"
)

// tryPathClone attempts an APFS copy-on-write clone of src into a
// not-yet-existing dst path via fclonefileat — a constant-time clone
// that makes `worktrees.copies` bring-in of large gitignored files
// (.env bundles, vendor dirs, seeded dumps) effectively free on macOS
// (#96). Returns the cloned size and true on success; (0, false) on
// any failure (non-APFS volume, cross-device, dst exists) so the
// caller falls back to io.Copy unchanged.
//
// fclonefileat refuses an existing destination, which is why this runs
// before copyRegularFile creates dst — the post-create FICLONE seam
// (cloneFile) is Linux-only and stays untouched.
func tryPathClone(srcPath, dstPath string, perm os.FileMode) (int64, bool) {
	sf, err := os.Open(srcPath)
	if err != nil {
		return 0, false
	}
	defer func() { _ = sf.Close() }()

	if err := unix.Fclonefileat(int(sf.Fd()), unix.AT_FDCWD, dstPath, 0); err != nil {
		return 0, false
	}
	// fclonefileat mirrors the source's mode; re-apply the requested
	// permission so gitignored executables stay executable.
	if err := os.Chmod(dstPath, perm); err != nil {
		_ = os.Remove(dstPath)
		return 0, false
	}
	st, err := sf.Stat()
	if err != nil {
		return 0, true
	}
	return st.Size(), true
}
