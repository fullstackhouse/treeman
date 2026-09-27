package patcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stubbedev/treeman/internal/config"
	"github.com/stubbedev/treeman/internal/slug"
	"github.com/stubbedev/treeman/internal/template"
)

// TestApplyEnvInterpolationInSetValues pins the #78 escape hatch:
// `set:` values render {key} tokens FIRST, then ${VAR} env vars —
// so `set: {LOG_PATH: /tmp/$USER/{slug}.log}` lands expanded, and an
// unset $NOPE stays verbatim rather than becoming an empty string.
func TestApplyEnvInterpolationInSetValues(t *testing.T) {
	dir := t.TempDir()
	wtPath := filepath.Join(dir, "wt")
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(wtPath, "app.env")
	if err := os.WriteFile(envFile, []byte("OLD=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tplCtx := template.FromSlug(slug.Slug{Value: "wt_demo", Source: slug.SourceTicket})
	p := config.Patch{
		File: "app.env",
		Set: map[string]string{
			"LOG_PATH": "/tmp/${USER}/{slug}.log",
			"MISSING":  "value=${DEFINITELY_NOT_SET_XYZ}",
		},
	}
	t.Setenv("USER", "bob")
	res, err := Apply(p, wtPath, tplCtx)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Outcome != Updated {
		t.Fatalf("outcome = %v, want Updated", res.Outcome)
	}
	body, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "LOG_PATH=/tmp/bob/wt_demo.log") {
		t.Errorf("$USER + {slug} should both render:\n%s", text)
	}
	if !strings.Contains(text, "MISSING=value=${DEFINITELY_NOT_SET_XYZ}") {
		t.Errorf("unresolvable $VAR must stay verbatim:\n%s", text)
	}
}
