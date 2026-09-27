//go:build windows

package wt

import (
	"fmt"
	"os"
	"time"
)

// lockExcludeFile is the windows variant of the shared info/exclude
// read-modify-write lock (#89): an O_CREATE|O_EXCL sentinel file,
// retried briefly; a sentinel older than 10s (a crashed writer) is
// treated as stale and stolen. Cruder than flock, but dependency-free
// and sufficient for the tiny exclude-edit race window.
func lockExcludeFile(path string) (func(), error) {
	lockPath := path + ".lock"
	deadline := time.Now().Add(5 * time.Second)
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if fi, statErr := os.Stat(lockPath); statErr == nil && time.Since(fi.ModTime()) > 10*time.Second {
			_ = os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("lock %s: %w", lockPath, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// appendFile appends at the end of the locked file.
func appendFile(path, s string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.WriteString(s)
	return err
}
