// Cross-platform coverage for the OS-native daemon control paths.
//
// daemonctl.Start/Stop shell out to systemctl (Linux) or launchctl
// (macOS). We never want a test to poke the real per-user service
// manager, and we want BOTH branches exercised regardless of which OS
// the test happens to run on. These tests therefore:
//
//   - override the `goos` selector so a Linux runner can drive the
//     darwin branch and vice-versa, and
//   - put recording fakes for systemctl / launchctl / treemand on PATH
//     so the real command-construction code runs but the recorded
//     argv is all that's asserted.
//
// Being un-tagged, they run under the plain `go test ./...` that CI
// executes on both ubuntu-latest and macos-latest — which is what
// actually proves the macOS control path behaves like the Linux one.
package daemonctl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeBins writes recording stubs for the named commands into a temp
// dir, prepends that dir to PATH for the duration of the test, and
// returns a reader that yields every recorded invocation line. Each
// stub appends "<name> <argv>" to a shared log and exits 0.
func fakeBins(t *testing.T, names ...string) (logLines func() []string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")

	for _, name := range names {
		script := "#!/bin/sh\n" +
			"printf '%s %s\\n' \"$(basename \"$0\")\" \"$*\" >> \"" + logPath + "\"\nexit 0\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return func() []string {
		b, err := os.ReadFile(logPath)
		if err != nil {
			return nil
		}
		var out []string
		for l := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
			if l != "" {
				out = append(out, l)
			}
		}
		return out
	}
}

// setGOOS overrides the package selector and restores it on cleanup.
func setGOOS(t *testing.T, v string) {
	t.Helper()
	prev := goos
	goos = v
	t.Cleanup(func() { goos = prev })
}

func joined(lines []string) string { return strings.Join(lines, "\n") }

func TestStartDarwinKickstartsLaunchd(t *testing.T) {
	setGOOS(t, "darwin")
	calls := fakeBins(t, "launchctl")

	pid, err := Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if pid != 0 {
		t.Errorf("launchctl succeeded → expected pid 0 (init handled it), got %d", pid)
	}
	got := joined(calls())
	if !strings.Contains(got, "launchctl kickstart -k gui/") || !strings.Contains(got, "/"+LaunchdLabel) {
		t.Errorf("expected launchctl kickstart for %s, got:\n%s", LaunchdLabel, got)
	}
}

func TestStopDarwinKillsLaunchd(t *testing.T) {
	setGOOS(t, "darwin")
	calls := fakeBins(t, "launchctl")

	if err := Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	got := joined(calls())
	if !strings.Contains(got, "launchctl kill TERM gui/") || !strings.Contains(got, "/"+LaunchdLabel) {
		t.Errorf("expected launchctl kill TERM for %s, got:\n%s", LaunchdLabel, got)
	}
}

func TestStartLinuxStartsSystemdUnit(t *testing.T) {
	setGOOS(t, "linux")
	calls := fakeBins(t, "systemctl")

	pid, err := Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if pid != 0 {
		t.Errorf("systemctl handled start → expected pid 0, got %d", pid)
	}
	got := joined(calls())
	if !strings.Contains(got, "systemctl --user is-enabled treemand") {
		t.Errorf("expected is-enabled probe, got:\n%s", got)
	}
	if !strings.Contains(got, "systemctl --user start treemand") {
		t.Errorf("expected start after enabled probe, got:\n%s", got)
	}
}

func TestStopLinuxStopsSystemdUnit(t *testing.T) {
	setGOOS(t, "linux")
	calls := fakeBins(t, "systemctl")

	if err := Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	got := joined(calls())
	if !strings.Contains(got, "systemctl --user is-active treemand") {
		t.Errorf("expected is-active probe, got:\n%s", got)
	}
	if !strings.Contains(got, "systemctl --user stop treemand") {
		t.Errorf("expected stop after is-active probe, got:\n%s", got)
	}
}

// emptyPATH removes every lookup target so exec.LookPath finds nothing
// — the systemd-less host simulation (Alpine, WSL1, containers).
func emptyPATH(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

// TestInstallNonDarwinWithoutSystemctlIsActionable pins the #93 guard:
// on a non-darwin host without systemctl, Install must refuse with the
// detached-start guidance instead of exec'ing into "executable file
// not found".
func TestInstallNonDarwinWithoutSystemctlIsActionable(t *testing.T) {
	setGOOS(t, "linux")
	emptyPATH(t)

	_, err := Install(context.Background())
	if err == nil {
		t.Fatal("Install should refuse without systemctl")
	}
	for _, want := range []string{"no supported init", "treeman daemon start"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

// TestUninstallNonDarwinWithoutSystemctl pins Uninstall's two
// systemd-less outcomes: a leftover unit file (written while systemd
// was available) is still cleaned up; nothing-installed is an explicit
// refusal, not a silent success.
func TestUninstallNonDarwinWithoutSystemctl(t *testing.T) {
	setGOOS(t, "linux")
	emptyPATH(t)

	unit := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", unit)
	dst := filepath.Join(unit, ".config", "systemd", "user", "treemand.service")

	t.Run("leftover unit removed", func(t *testing.T) {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, []byte("junk"), 0o644); err != nil {
			t.Fatal(err)
		}
		msg, err := Uninstall(context.Background())
		if err != nil {
			t.Fatalf("Uninstall with leftover unit: %v", err)
		}
		if !strings.Contains(msg, "leftover") {
			t.Errorf("summary %q missing \"leftover\"", msg)
		}
		if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
			t.Errorf("unit still on disk: %v", statErr)
		}
	})

	t.Run("nothing installed refuses", func(t *testing.T) {
		_, err := Uninstall(context.Background())
		if err == nil || !strings.Contains(err.Error(), "nothing to uninstall") {
			t.Fatalf("expected nothing-to-uninstall refusal, got %v", err)
		}
	})
}

// When the init system reports the unit is not enabled, Start must fall
// back to forking the treemand binary off PATH and return its pid.
func TestStartForksWhenUnitNotEnabled(t *testing.T) {
	setGOOS(t, "linux")
	dir := t.TempDir()
	marker := filepath.Join(dir, "started")

	// systemctl whose is-enabled exits non-zero → fork path.
	writeScript(t, dir, "systemctl",
		"#!/bin/sh\ncase \"$*\" in *is-enabled*) exit 1 ;; esac\nexit 0\n")
	// fake treemand records that it ran, then idles so we can reap it.
	writeScript(t, dir, "treemand",
		"#!/bin/sh\necho up > \""+marker+"\"\nsleep 30\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	pid, err := Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("expected forked pid > 0, got %d", pid)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("forked treemand never wrote its marker file")
}

func writeScript(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
