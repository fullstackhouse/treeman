package containerip

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeEngine writes a shell script that answers the engine CLI
// subcommands containerip needs: info (reachable), context inspect
// (daemon endpoint), and inspect (container metadata). The name is
// unique per test (keyed off t.Name()) so the package-level caches
// (keyed by engine) can't leak state between tests.
func fakeEngine(t *testing.T, endpointOutput string) string {
	t.Helper()
	dir := t.TempDir()
	name := "tm-fake-docker-" + strings.NewReplacer("/", "_", " ", "_", "-", "_").Replace(strings.TrimPrefix(t.Name(), "Test"))
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  info) echo ok;;\n" +
		"  context) echo '" + endpointOutput + "';;\n" +
		"  inspect) cat <<'JSON'\n" + sampleInspect + "\nJSON\n;;\n" +
		"  *) exit 1;;\n" +
		"esac\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return name
}

// TestHostFromEndpoint pins the endpoint parser: local endpoints
// (unix socket, named pipe, loopback tcp, garbage) yield "", remote
// tcp/ssh/http endpoints yield their host with userinfo and port
// stripped.
func TestHostFromEndpoint(t *testing.T) {
	cases := []struct{ ep, want string }{
		{"", ""},
		{"unix:///var/run/docker.sock", ""},
		{"npipe:////./pipe/docker_engine", ""},
		{"tcp://127.0.0.1:2375", ""},
		{"tcp://localhost:2375", ""},
		{"tcp://192.168.1.10:2375", "192.168.1.10"},
		{"tcp://user:pass@buildbox:2375", "buildbox"},
		{"ssh://deploy@buildbox", "buildbox"},
		{"https://registry.example.com", "registry.example.com"},
		{"gopher://deep-space", ""},
		{"  tcp://[::1]:2375  ", ""},
		{"nonsense", ""},
	}
	for _, tc := range cases {
		if got := hostFromEndpoint(tc.ep); got != tc.want {
			t.Errorf("hostFromEndpoint(%q) = %q, want %q", tc.ep, got, tc.want)
		}
	}
}

// TestResolveAddrRemoteEndpoint pins the #86 acceptance criteria: with
// a remote daemon endpoint, published-port resolution dials the REMOTE
// host instead of 127.0.0.1 — via the Opts-level injected lookup and
// via $DOCKER_HOST. A local endpoint (unix socket) keeps the loopback
// default.
func TestResolveAddrRemoteEndpoint(t *testing.T) {
	const container = "myapp-mysql-1"

	t.Run("injected Opts.RemoteHost wins", func(t *testing.T) {
		engine := fakeEngine(t, "tcp://ignored:2375")
		addr, err := ResolveAddr(context.Background(), Opts{
			Container:    container,
			Engine:       engine,
			InternalPort: 3306,
			RemoteHost: func(context.Context, string) string {
				return "buildbox.internal"
			},
		})
		if err != nil {
			t.Fatalf("ResolveAddr: %v", err)
		}
		if addr == nil || addr.Host != "buildbox.internal" || addr.Port != 3307 {
			t.Fatalf("addr = %+v, want buildbox.internal:3307 (published port)", addr)
		}
	})

	t.Run("DOCKER_HOST tcp endpoint", func(t *testing.T) {
		engine := fakeEngine(t, "tcp://buildbox.internal:2375")
		t.Setenv("DOCKER_HOST", "tcp://buildbox.internal:2375")
		addr, err := ResolveAddr(context.Background(), Opts{
			Container: container, Engine: engine, InternalPort: 3306,
		})
		if err != nil {
			t.Fatalf("ResolveAddr: %v", err)
		}
		if addr == nil || addr.Host != "buildbox.internal" {
			t.Fatalf("addr = %+v, want host buildbox.internal", addr)
		}
	})

	t.Run("local unix endpoint keeps 127.0.0.1", func(t *testing.T) {
		engine := fakeEngine(t, "unix:///var/run/docker.sock")
		t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
		addr, err := ResolveAddr(context.Background(), Opts{
			Container: container, Engine: engine, InternalPort: 3306,
		})
		if err != nil {
			t.Fatalf("ResolveAddr: %v", err)
		}
		if addr == nil || addr.Host != "127.0.0.1" {
			t.Fatalf("addr = %+v, want loopback default", addr)
		}
	})
}
