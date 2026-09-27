package wt

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCopyRegularFileFallback pins the contract the clone seams lean
// on (#96): on a filesystem without reflink/clonefile support both
// clone attempts fail and copyRegularFile must still produce a
// byte-exact copy with the requested permission bits — the
// always-false non-darwin tryPathClone stub plus the FICLONE
// EOPNOTSUPP path funnel into the same io.Copy fallback.
func TestCopyRegularFileFallback(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "seed.sql")
	body := "INSERT INTO products VALUES (1);\n-- padding to defeat any same-page shortcuts\n" +
		string(make([]byte, 4096))
	if err := os.WriteFile(src, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "sub", "seed.sql")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	n, err := copyRegularFile(src, dst, 0o755)
	if err != nil {
		t.Fatalf("copyRegularFile: %v", err)
	}
	if n != int64(len(body)) {
		t.Errorf("copied %d bytes, want %d", n, len(body))
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Error("copy is not byte-exact")
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("perm = %v, want 0755", fi.Mode().Perm())
	}

	// Second copy over an existing destination (the io.Copy
	// O_TRUNC path) stays byte-exact too.
	if err := os.WriteFile(dst, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := copyRegularFile(src, dst, 0o644); err != nil {
		t.Fatalf("re-copy: %v", err)
	}
	got, err = os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Error("re-copy is not byte-exact")
	}
	_, statErr := os.Lstat(dst)
	if statErr != nil {
		t.Fatal(statErr)
	}
}
