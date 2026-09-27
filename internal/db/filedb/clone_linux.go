//go:build linux

package filedb

import (
	"os"

	"golang.org/x/sys/unix"
)

// reflink attempts a kernel-side copy-on-write clone (FICLONE) of
// src's data into dst — constant-time on btrfs, XFS with reflink=1,
// and bcachefs. Failure (EOPNOTSUPP on ext4, EXDEV across mounts) is
// expected and cheap: the caller falls back to a streamed io.Copy.
func reflink(dst, src *os.File) error {
	return unix.IoctlFileClone(int(dst.Fd()), int(src.Fd()))
}
