package shellenv

import (
	"testing"
	"time"
)

func TestMergePaths(t *testing.T) {
	cases := []struct {
		name, base, extra, want string
	}{
		{"empty extra returns base", "/a:/b", "", "/a:/b"},
		{"empty base returns extra", "", "/x:/y", "/x:/y"},
		{"both empty", "", "", ""},
		{"appends only new dirs", "/a:/b", "/b:/c", "/a:/b:/c"},
		{"base precedence preserved", "/user/bin", "/nix/bin:/user/bin", "/user/bin:/nix/bin"},
		{"all new appended in order", "/a", "/b:/c", "/a:/b:/c"},
		{"skips empty segments in extra", "/a", ":/b:", "/a:/b"},
		{"fully overlapping extra is no-op", "/a:/b", "/a:/b", "/a:/b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MergePaths(c.base, c.extra); got != c.want {
				t.Errorf("MergePaths(%q, %q) = %q, want %q", c.base, c.extra, got, c.want)
			}
		})
	}
}

// TestBaseEnvMergesLoginPath asserts the inheritedEnv PATH keeps
// precedence (leads) while the login-shell PATH is folded in after.
func TestBaseEnvMergesLoginPath(t *testing.T) {
	orig := LoginShellPATH
	defer func() { LoginShellPATH = orig }()
	LoginShellPATH = func() string { return "/nix-profile/bin:/user/shims" }

	env := BaseEnv(map[string]string{"PATH": "/user/shims:/user/bin"})
	got := env["PATH"]
	const wantPrefix = "/user/shims:/user/bin"
	if got[:len(wantPrefix)] != wantPrefix {
		t.Fatalf("inheritedEnv PATH should lead, got %q", got)
	}
	if got != "/user/shims:/user/bin:/nix-profile/bin" {
		t.Errorf("login-shell dirs not merged correctly: %q", got)
	}
}

// TestBaseEnvLoginPathLeadsWithoutInherited asserts that with no
// inherited PATH (externally-created worktree) the login-shell dirs
// lead the daemon floor, so a stale /usr/local/bin tool can't shadow
// the user's profile bin (#121).
func TestBaseEnvLoginPathLeadsWithoutInherited(t *testing.T) {
	orig := LoginShellPATH
	defer func() { LoginShellPATH = orig }()
	LoginShellPATH = func() string { return "/nix-profile/bin:/usr/bin" }
	t.Setenv("PATH", "/usr/local/bin:/usr/bin")

	for name, inherited := range map[string]map[string]string{
		"nil":        nil,
		"empty PATH": {"PATH": ""},
		"no PATH":    {"FOO": "bar"},
	} {
		t.Run(name, func(t *testing.T) {
			got := BaseEnv(inherited)["PATH"]
			if want := "/nix-profile/bin:/usr/bin:/usr/local/bin"; got != want {
				t.Errorf("PATH = %q, want %q", got, want)
			}
		})
	}
}

// TestLoginShellPATHDoesNotCacheFailure asserts a failed probe is
// retried (after the throttle window) instead of being cached for the
// daemon's lifetime, and that a success is cached (#121).
func TestLoginShellPATHDoesNotCacheFailure(t *testing.T) {
	origProbe := loginProbe
	defer func() {
		loginProbe = origProbe
		loginPATH.value, loginPATH.lastFailed = "", time.Time{}
	}()
	loginPATH.value, loginPATH.lastFailed = "", time.Time{}

	calls := 0
	result := ""
	loginProbe = func() string { calls++; return result }

	if got := cachedLoginShellPATH(); got != "" || calls != 1 {
		t.Fatalf("first probe: got %q calls=%d", got, calls)
	}
	// Inside the throttle window: no re-probe.
	if got := cachedLoginShellPATH(); got != "" || calls != 1 {
		t.Fatalf("throttled probe: got %q calls=%d", got, calls)
	}
	// Window elapsed and the profile is ready now.
	loginPATH.lastFailed = time.Now().Add(-loginProbeRetry - time.Second)
	result = "/nix-profile/bin"
	if got := cachedLoginShellPATH(); got != "/nix-profile/bin" || calls != 2 {
		t.Fatalf("retry probe: got %q calls=%d", got, calls)
	}
	// Success is cached.
	result = ""
	if got := cachedLoginShellPATH(); got != "/nix-profile/bin" || calls != 2 {
		t.Fatalf("cached probe: got %q calls=%d", got, calls)
	}
}
