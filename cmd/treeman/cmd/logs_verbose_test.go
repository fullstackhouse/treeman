package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/treeman/internal/store"
	"github.com/stubbedev/treeman/internal/ui"
)

// captureOutErr redirects ui.Out AND ui.Err for the duration of f and
// returns what was written to each.
func captureOutErr(t *testing.T, f func()) (string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	oldOut, oldErr := ui.Out, ui.Err
	ui.Out = &out
	ui.Err = &errb //nolint:reassign // test seam: the package-level writers are the redirect point
	defer func() { ui.Out, ui.Err = oldOut, oldErr }()
	f()
	return out.String(), errb.String()
}

// captureOut redirects ui.Out for the duration of f and returns what
// was written.
func captureOut(t *testing.T, f func()) string {
	t.Helper()
	var buf bytes.Buffer
	old := ui.Out
	ui.Out = &buf
	defer func() { ui.Out = old }()
	f()
	return buf.String()
}

// TestSortedPayloadPairs pins the verbose payload rendering: keys come
// out sorted, the run_id bookkeeping key is dropped, values format
// with %v, and a malformed payload degrades to empty.
func TestSortedPayloadPairs(t *testing.T) {
	got := sortedPayloadPairs(`{"engine":"mysql","cache_hit":true,"run_id":"abc","duration_ms":4123}`)
	want := "cache_hit=true duration_ms=4123 engine=mysql"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := sortedPayloadPairs(""); got != "" {
		t.Errorf("empty payload should render empty, got %q", got)
	}
	if got := sortedPayloadPairs("not json"); got != "" {
		t.Errorf("malformed payload should degrade to empty, got %q", got)
	}
}

// TestPrintEventVerboseAndTimestamps pins the human-line contract
// (#81): --verbose appends sorted dim key=value pairs and drops
// run_id; today's events print time-of-day only unless --full-ts; the
// full stamp returns with --full-ts; --json is untouched by all three.
func TestPrintEventVerboseAndTimestamps(t *testing.T) {
	ev := store.Event{
		ID:          1,
		Ts:          time.Now().UnixMilli(),
		Level:       "info",
		EventType:   "prepare:end",
		Message:     "prepare finished",
		PayloadJSON: `{"engine":"mysql","cache_hit":true,"run_id":"abc"}`,
	}

	t.Run("verbose appends sorted payload pairs, drops run_id", func(t *testing.T) {
		out := captureOut(t, func() {
			printEventStyled(eventStyle{verbose: true}, ev)
		})
		if !strings.Contains(out, "cache_hit=true engine=mysql") {
			t.Errorf("verbose line missing sorted payload pairs:\n%s", out)
		}
		if strings.Contains(out, "run_id") {
			t.Errorf("verbose line leaked run_id:\n%s", out)
		}
	})

	t.Run("default today timestamp is time-of-day", func(t *testing.T) {
		out := captureOut(t, func() {
			printEventStyled(eventStyle{}, ev)
		})
		if regexp.MustCompile(`\d{2}:\d{2}:\d{2}`).MatchString(out) == false {
			t.Errorf("today's line should carry a bare time-of-day:\n%s", out)
		}
		if strings.Contains(out, fmt.Sprintf("%04d-", time.Now().Year())) {
			t.Errorf("today's line should not print the date:\n%s", out)
		}
	})

	t.Run("full-ts restores the wide stamp", func(t *testing.T) {
		out := captureOut(t, func() {
			printEventStyled(eventStyle{fullTS: true}, ev)
		})
		if !strings.Contains(out, formatTs(ev.Ts)) {
			t.Errorf("full-ts line should contain the full stamp:\n%s", out)
		}
	})

	t.Run("yesterday keeps the full stamp without the flag", func(t *testing.T) {
		old := ev
		old.Ts = time.Now().Add(-48 * time.Hour).UnixMilli()
		out := captureOut(t, func() {
			printEventStyled(eventStyle{}, old)
		})
		if !strings.Contains(out, formatTs(old.Ts)) {
			t.Errorf("non-today line should keep the full stamp:\n%s", out)
		}
	})

	t.Run("json output is untouched by the style knobs", func(t *testing.T) {
		out := captureOut(t, func() {
			printEventStyled(eventStyle{asJSON: true, verbose: true}, ev)
		})
		var decoded map[string]any
		if err := json.Unmarshal(bytes.TrimSpace([]byte(out)), &decoded); err != nil {
			t.Fatalf("json line malformed: %v\n%s", err, out)
		}
		payload, ok := decoded["payload"].(map[string]any)
		if !ok || payload["engine"] != "mysql" {
			t.Errorf("json payload changed: %v", decoded["payload"])
		}
		if strings.Contains(out, "cache_hit=true") {
			t.Errorf("json line must not gain human payload pairs:\n%s", out)
		}
	})
}
