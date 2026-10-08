package wtlock

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// isolate points the lock directory at a temp state dir.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("TREEMAN_DB_PATH", filepath.Join(t.TempDir(), "treeman.db"))
}

func acquireWithin(t *testing.T, kind, wtPath string, d time.Duration, onWait func()) (func(), error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return Acquire(ctx, kind, wtPath, onWait)
}

func TestAcquireExcludesUntilReleased(t *testing.T) {
	isolate(t)
	wt := t.TempDir()
	release, err := Acquire(context.Background(), Finalize, wt, nil)
	if err != nil {
		t.Fatal(err)
	}
	waited := 0
	if _, err := acquireWithin(t, Finalize, wt, 300*time.Millisecond, func() { waited++ }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Acquire while held: err = %v, want DeadlineExceeded", err)
	}
	if waited != 1 {
		t.Fatalf("onWait ran %d times, want once", waited)
	}

	got := make(chan error, 1)
	go func() {
		rel, err := acquireWithin(t, Finalize, wt, 5*time.Second, nil)
		if err == nil {
			rel()
		}
		got <- err
	}()
	time.Sleep(100 * time.Millisecond)
	release()
	if err := <-got; err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
}

func TestAcquireIsPerWorktreeAndKind(t *testing.T) {
	isolate(t)
	a, b := t.TempDir(), t.TempDir()
	release, err := Acquire(context.Background(), Finalize, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, c := range []struct{ kind, wt string }{{Finalize, b}, {Prepare, a}} {
		rel, err := acquireWithin(t, c.kind, c.wt, time.Second, func() { t.Errorf("%s %s waited on an unrelated lock", c.kind, c.wt) })
		if err != nil {
			t.Fatalf("%s %s: %v", c.kind, c.wt, err)
		}
		rel()
	}
}

// TestAcquireExcludesOtherProcess is the #123 case: the holder is a
// different process (the daemon), not a goroutine of this one.
func TestAcquireExcludesOtherProcess(t *testing.T) {
	if os.Getenv("WTLOCK_HOLDER") != "" {
		holdForParent()
		return
	}
	isolate(t)
	wt := t.TempDir()

	cmd := exec.Command(os.Args[0], "-test.run=^TestAcquireExcludesOtherProcess$")
	cmd.Env = append(os.Environ(), "WTLOCK_HOLDER="+wt)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("holder process: %q, %v", line, err)
	}

	if _, err := acquireWithin(t, Finalize, wt, 300*time.Millisecond, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire while another process holds the lock: err = %v, want DeadlineExceeded", err)
	}
	_ = stdin.Close() // holder releases and exits
	rel, err := acquireWithin(t, Finalize, wt, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("Acquire after the holder exited: %v", err)
	}
	rel()
}

// holdForParent takes the lock, reports it, and holds it until the
// parent closes stdin.
func holdForParent() {
	release, err := Acquire(context.Background(), Finalize, os.Getenv("WTLOCK_HOLDER"), nil)
	if err != nil {
		os.Exit(2)
	}
	_, _ = os.Stdout.WriteString("locked\n")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	release()
	os.Exit(0)
}
