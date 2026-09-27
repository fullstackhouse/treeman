package daemonctl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Install wires treemand into the OS init system (systemd --user on
// Linux, a LaunchAgent on macOS) so it auto-starts at login. Returns a
// human-readable summary of what was installed. Shared by the CLI
// (`treeman daemon install`) and the MCP daemon_control tool.
//
// A non-darwin host without systemctl gets an explicit refusal rather
// than an exec failure: autostart is the only thing Install provides,
// and there is nothing to write a unit into. The daemon itself still
// works everywhere — daemonctl.Start's detached fork is the fallback.
func Install(ctx context.Context) (string, error) {
	if goos == "darwin" {
		return InstallLaunchd(ctx)
	}
	if !systemdAvailable() {
		return "", errors.New(
			"no supported init system — treeman autostart needs systemd --user (systemctl not found on PATH); the daemon works without it: `treeman daemon start` runs it detached, and every command auto-starts it on demand",
		)
	}
	return InstallSystemd(ctx)
}

// Uninstall removes the auto-start unit. Returns a summary. The caller
// owns any confirmation prompt — this just does the removal. On a
// systemd-less host it still clears a leftover unit file (written
// before systemd went missing, e.g. after switching distros/WSL
// setups) but refuses when there's nothing to remove.
func Uninstall(ctx context.Context) (string, error) {
	if goos == "darwin" {
		return UninstallLaunchd(ctx)
	}
	if !systemdAvailable() {
		home, _ := os.UserHomeDir()
		dst := filepath.Join(home, ".config", "systemd", "user", "treemand.service")
		if err := os.Remove(dst); err == nil {
			return "removed leftover treemand.service (systemd unavailable on this host)", nil
		}
		return "", errors.New(
			"nothing to uninstall — treeman was not installed via systemd (systemctl not found); a running detached daemon stops with `treeman daemon stop`",
		)
	}
	return UninstallSystemd(ctx)
}

// resolveTreemand finds the treemand binary for the unit's ExecStart,
// falling back to a conventional path when it isn't on PATH.
func resolveTreemand() string {
	if p, err := exec.LookPath("treemand"); err == nil {
		return p
	}
	return "/usr/local/bin/treemand"
}

// systemdUnitContent renders the treemand.service unit with the given
// ExecStart path. Kept pure so tests can assert the generated unit
// without touching the real systemd manager.
func systemdUnitContent(execStart string) string {
	return `[Install]
WantedBy=default.target

[Service]
ExecStart=` + execStart + `
Restart=on-failure
RestartSec=2
Type=simple

[Unit]
After=default.target
Description=Treeman per-worktree DB orchestrator daemon
`
}

// launchdPlistContent renders the LaunchAgent plist for treemand. Pure
// so tests can validate the XML + keys without invoking launchctl.
func launchdPlistContent(bin, logPath string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>            <string>` + LaunchdLabel + `</string>
    <key>ProgramArguments</key> <array><string>` + bin + `</string></array>
    <key>RunAtLoad</key>        <true/>
    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>ThrottleInterval</key> <integer>5</integer>
    <key>StandardOutPath</key>  <string>` + logPath + `</string>
    <key>StandardErrorPath</key><string>` + logPath + `</string>
    <key>ProcessType</key>      <string>Background</string>
</dict>
</plist>
`
}

// InstallSystemd writes the systemd-user unit and enables+starts it.
func InstallSystemd(ctx context.Context) (string, error) {
	unit := systemdUnitContent(resolveTreemand())
	home, _ := os.UserHomeDir()
	dst := filepath.Join(home, ".config", "systemd", "user", "treemand.service")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(dst, []byte(unit), 0o644); err != nil {
		return "", err
	}
	if err := exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload").Run(); err != nil {
		return "", err
	}
	if err := exec.CommandContext(ctx, "systemctl", "--user", "enable", "--now", "treemand").Run(); err != nil {
		return "", err
	}
	return "installed + enabled treemand.service at " + dst, nil
}

// UninstallSystemd disables the unit and removes the file.
func UninstallSystemd(ctx context.Context) (string, error) {
	_ = exec.CommandContext(ctx, "systemctl", "--user", "disable", "--now", "treemand").Run()
	home, _ := os.UserHomeDir()
	dst := filepath.Join(home, ".config", "systemd", "user", "treemand.service")
	_ = os.Remove(dst)
	_ = exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload").Run()
	return "uninstalled treemand.service", nil
}

// InstallLaunchd writes the LaunchAgent plist and bootstraps it.
func InstallLaunchd(ctx context.Context) (string, error) {
	bin := resolveTreemand()
	home, _ := os.UserHomeDir()
	logDir := filepath.Join(home, ".local", "share", "treeman")
	_ = os.MkdirAll(logDir, 0o755)
	plist := launchdPlistContent(bin, filepath.Join(logDir, "treemand.log"))
	dst := filepath.Join(home, "Library", "LaunchAgents", LaunchdLabel+".plist")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(dst, []byte(plist), 0o644); err != nil {
		return "", err
	}
	uid := os.Getuid()
	domain := fmt.Sprintf("gui/%d", uid)
	// Best-effort unload first so re-install doesn't error.
	_ = exec.CommandContext(ctx, "launchctl", "bootout", domain, dst).Run()
	if err := exec.CommandContext(ctx, "launchctl", "bootstrap", domain, dst).Run(); err != nil {
		return "", fmt.Errorf("launchctl bootstrap %s: %w", dst, err)
	}
	if err := exec.CommandContext(ctx, "launchctl", "enable", domain+"/"+LaunchdLabel).Run(); err != nil {
		return "", fmt.Errorf("launchctl enable %s: %w", LaunchdLabel, err)
	}
	return fmt.Sprintf("installed + enabled %s at %s", LaunchdLabel, dst), nil
}

// UninstallLaunchd boots out and removes the plist.
func UninstallLaunchd(ctx context.Context) (string, error) {
	home, _ := os.UserHomeDir()
	dst := filepath.Join(home, "Library", "LaunchAgents", LaunchdLabel+".plist")
	uid := os.Getuid()
	domain := fmt.Sprintf("gui/%d", uid)
	_ = exec.CommandContext(ctx, "launchctl", "bootout", domain, dst).Run()
	_ = os.Remove(dst)
	return "uninstalled " + LaunchdLabel, nil
}
