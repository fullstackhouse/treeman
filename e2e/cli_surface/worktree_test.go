//go:build e2e

package cli_surface_e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makeWorktreeFixture lays down a git repo + .treeman.yaml, registers
// two worktrees in the SQLite DB, and writes a few seed events.
// Returns the repo root and the two worktree paths the tests then
// invoke commands against.
func makeWorktreeFixture(t *testing.T, e *env) (repo, wtA, wtB string) {
	t.Helper()
	repo = newGitRepo(t)
	// `wt delete` against this fixture auto-spawns a daemon that does the
	// git-worktree teardown inside `repo` asynchronously. Stop + drain it
	// before the repo's temp dir is removed (registered after newGitRepo
	// so it runs first under LIFO cleanup) to avoid a RemoveAll race.
	t.Cleanup(func() { stopDaemon(t, e) })
	writeConfig(t, repo, minimalConfig)
	wtA = filepath.Join(repo, ".worktrees", "feat_a")
	wtB = filepath.Join(repo, ".worktrees", "feat_b")
	for _, d := range []string{wtA, wtB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	st := openStore(t, e)
	repoID, err := st.EnsureRepo(ctx, repo, "repo")
	if err != nil {
		t.Fatal(err)
	}
	aID, err := st.EnsureWorktree(ctx, repoID, wtA, "feat_a", "feature/a")
	if err != nil {
		t.Fatal(err)
	}
	bID, err := st.EnsureWorktree(ctx, repoID, wtB, "feat_b", "feature/b")
	if err != nil {
		t.Fatal(err)
	}
	// Seed an event per worktree so `wt show` and `wt logs` have
	// something to print.
	for _, w := range []struct {
		id   int64
		slug string
	}{{aID, "feat_a"}, {bID, "feat_b"}} {
		if _, err := st.DB.ExecContext(ctx,
			`INSERT INTO events(ts, level, repo_id, worktree_id, event_type, message, payload_json)
			 VALUES (?, 'info', ?, ?, 'fixture_event', ?, '{}')`,
			time.Now().UnixMilli(), repoID, w.id, "seeded for "+w.slug); err != nil {
			t.Fatal(err)
		}
	}
	return repo, wtA, wtB
}

// ── treeman wt list ──────────────────────────────────────────────

func TestWtList(t *testing.T) {
	t.Run("empty registry prints info line, exits 0", func(t *testing.T) {
		repo := newGitRepo(t)
		e := newEnv(t)
		res := e.run(t, repo, "worktree", "list")
		if res.err != nil {
			t.Fatalf("wt list: %v\nstderr:\n%s", res.err, res.stderr)
		}
		// No worktrees → expected to print a "no worktrees" hint
		// rather than an empty table.
		combined := strings.ToLower(res.stdout + res.stderr)
		if !strings.Contains(combined, "no active worktrees") &&
			!strings.Contains(combined, "id") {
			t.Errorf("wt list empty output unexpected:\nstdout:\n%s\nstderr:\n%s",
				res.stdout, res.stderr)
		}
	})

	t.Run("populated registry prints rows", func(t *testing.T) {
		e := newEnv(t)
		repo, _, _ := makeWorktreeFixture(t, e)
		res := e.run(t, repo, "worktree", "list")
		if res.err != nil {
			t.Fatalf("wt list: %v\nstderr:\n%s", res.err, res.stderr)
		}
		for _, want := range []string{"feat_a", "feat_b"} {
			if !strings.Contains(res.stdout, want) {
				t.Errorf("wt list missing %q in:\n%s", want, res.stdout)
			}
		}
	})

	t.Run("--json shape", func(t *testing.T) {
		e := newEnv(t)
		repo, _, _ := makeWorktreeFixture(t, e)
		res := e.run(t, repo, "worktree", "list", "--json")
		if res.err != nil {
			t.Fatalf("wt list --json: %v\nstderr:\n%s", res.err, res.stderr)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(res.stdout)), &rows); err != nil {
			t.Fatalf("decode wt list JSON: %v\nstdout:\n%s", err, res.stdout)
		}
		if len(rows) != 2 {
			t.Errorf("expected 2 rows, got %d: %v", len(rows), rows)
		}
	})

	t.Run("--with-state adds STATE column", func(t *testing.T) {
		e := newEnv(t)
		repo, _, _ := makeWorktreeFixture(t, e)
		res := e.run(t, repo, "worktree", "list", "--with-state")
		if res.err != nil {
			t.Fatalf("wt list --with-state: %v\nstderr:\n%s", res.err, res.stderr)
		}
		if !strings.Contains(res.stdout, "STATE") {
			t.Errorf("expected STATE header:\n%s", res.stdout)
		}
	})

	t.Run("--sort visited is accepted", func(t *testing.T) {
		e := newEnv(t)
		repo, _, _ := makeWorktreeFixture(t, e)
		res := e.run(t, repo, "worktree", "list", "--sort", "visited")
		if res.err != nil {
			t.Errorf("wt list --sort visited: %v\nstderr:\n%s", res.err, res.stderr)
		}
	})

	t.Run("--repo override scopes the listing", func(t *testing.T) {
		e := newEnv(t)
		repo, _, _ := makeWorktreeFixture(t, e)
		other := newGitRepo(t)
		res := e.run(t, other, "worktree", "list", "--repo", repo)
		if res.err != nil {
			t.Fatalf("wt list --repo: %v\nstderr:\n%s", res.err, res.stderr)
		}
		if !strings.Contains(res.stdout, "feat_a") {
			t.Errorf("--repo override should surface registered worktrees:\n%s", res.stdout)
		}
	})
}

// ── treeman wt show ──────────────────────────────────────────────

func TestWtShow(t *testing.T) {
	e := newEnv(t)
	repo, _, _ := makeWorktreeFixture(t, e)

	t.Run("by slug surfaces seeded event", func(t *testing.T) {
		res := e.run(t, repo, "worktree", "show", "feat_a")
		if res.err != nil {
			t.Fatalf("wt show: %v\nstderr:\n%s", res.err, res.stderr)
		}
		for _, want := range []string{"feat_a", "fixture_event"} {
			if !strings.Contains(res.stdout, want) {
				t.Errorf("wt show missing %q:\n%s", want, res.stdout)
			}
		}
	})

	t.Run("by branch", func(t *testing.T) {
		res := e.run(t, repo, "worktree", "show", "feature/b")
		if res.err != nil {
			t.Fatalf("wt show feature/b: %v\nstderr:\n%s", res.err, res.stderr)
		}
		if !strings.Contains(res.stdout, "feat_b") {
			t.Errorf("wt show by branch should resolve to slug:\n%s", res.stdout)
		}
	})

	t.Run("unknown name errors", func(t *testing.T) {
		res := e.run(t, repo, "worktree", "show", "ghost")
		if res.err == nil {
			t.Errorf("expected error for unknown worktree, got stdout:\n%s", res.stdout)
		}
	})

	t.Run("--events / --hooks caps", func(t *testing.T) {
		res := e.run(t, repo, "worktree", "show", "feat_a", "--events", "1", "--hooks", "1")
		if res.err != nil {
			t.Errorf("wt show with caps: %v\nstderr:\n%s", res.err, res.stderr)
		}
	})
}

// ── treeman wt go / back / prev ──────────────────────────────────

func TestWtGoAndBack(t *testing.T) {
	e := newEnv(t)
	repo, wtA, _ := makeWorktreeFixture(t, e)

	t.Run("wt go by name prints path on stdout", func(t *testing.T) {
		res := e.run(t, repo, "worktree", "go", "feat_a")
		if res.err != nil {
			t.Fatalf("wt go: %v\nstderr:\n%s", res.err, res.stderr)
		}
		if strings.TrimSpace(res.stdout) != wtA {
			t.Errorf("wt go stdout = %q, want %q", strings.TrimSpace(res.stdout), wtA)
		}
	})

	t.Run("wt go unknown name errors", func(t *testing.T) {
		res := e.run(t, repo, "worktree", "go", "ghost")
		if res.err == nil {
			t.Errorf("expected error for unknown name, got stdout:\n%s", res.stdout)
		}
	})

	t.Run("wt back from worktree prints main repo path", func(t *testing.T) {
		res := e.run(t, wtA, "worktree", "back")
		if res.err != nil {
			t.Fatalf("wt back: %v\nstderr:\n%s", res.err, res.stderr)
		}
		// In a non-git linked-worktree (fixture path is just a dir
		// under the main repo), "back" resolves through repo-root
		// discovery → the main repo path.
		if !strings.Contains(strings.TrimSpace(res.stdout), repo) {
			t.Errorf("wt back stdout %q should contain repo root %q",
				strings.TrimSpace(res.stdout), repo)
		}
	})
}

func TestWtGoResolveByBranch(t *testing.T) {
	e := newEnv(t)
	repo, wtA, _ := makeWorktreeFixture(t, e)

	t.Run("by branch returns worktree path", func(t *testing.T) {
		res := e.run(t, repo, "worktree", "go", "feature/a")
		if res.err != nil {
			t.Fatalf("wt go: %v\nstderr:\n%s", res.err, res.stderr)
		}
		if strings.TrimSpace(res.stdout) != wtA {
			t.Errorf("wt go = %q, want %q", strings.TrimSpace(res.stdout), wtA)
		}
	})

	t.Run("unknown branch exits nonzero", func(t *testing.T) {
		res := e.run(t, repo, "worktree", "go", "no/such/branch")
		if res.err == nil {
			t.Errorf("expected nonzero exit, got stdout:\n%s", res.stdout)
		}
	})
}

func TestWtPrev(t *testing.T) {
	e := newEnv(t)
	repo, _, _ := makeWorktreeFixture(t, e)

	// With no prior visits there's no "previous" worktree; the command
	// should exit non-zero rather than print a blank path the shell
	// would `cd` into.
	res := e.run(t, repo, "worktree", "prev")
	if res.err == nil && strings.TrimSpace(res.stdout) != "" {
		t.Errorf("expected nonzero exit when no prev worktree, got stdout %q", res.stdout)
	}
}

// ── treeman wt register / unregister ─────────────────────────────

func TestWtRegisterUnregister(t *testing.T) {
	e := newEnv(t)
	repo := newGitRepo(t)
	writeConfig(t, repo, minimalConfig)
	wtPath := filepath.Join(repo, ".worktrees", "manual")
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("register adds a row to the registry", func(t *testing.T) {
		res := e.run(t, wtPath, "worktree", "register", "--branch", "manual-branch")
		if res.err != nil {
			t.Fatalf("wt register: %v\nstderr:\n%s", res.err, res.stderr)
		}
		// Confirm via wt list.
		res = e.run(t, repo, "worktree", "list", "--json")
		if res.err != nil {
			t.Fatalf("wt list: %v\nstderr:\n%s", res.err, res.stderr)
		}
		if !strings.Contains(res.stdout, "manual") {
			t.Errorf("registered worktree not in list:\n%s", res.stdout)
		}
	})

	t.Run("unregister marks deleted without touching filesystem", func(t *testing.T) {
		res := e.run(t, wtPath, "worktree", "unregister", "--json")
		if res.err != nil {
			t.Fatalf("wt unregister: %v\nstderr:\n%s", res.err, res.stderr)
		}
		// The --json surface echoes the resolved row: id + path.
		var out struct {
			WorktreeID int64  `json:"worktree_id"`
			Path       string `json:"path"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(res.stdout)), &out); err != nil {
			t.Fatalf("decode wt unregister --json: %v\nstdout:\n%s", err, res.stdout)
		}
		if out.WorktreeID == 0 {
			t.Errorf("wt unregister --json missing worktree_id:\n%s", res.stdout)
		}
		if out.Path != wtPath {
			t.Errorf("wt unregister --json path = %q, want %q", out.Path, wtPath)
		}
		if _, err := os.Stat(wtPath); err != nil {
			t.Errorf("wt unregister should leave fs alone, but path is gone: %v", err)
		}
		// Active list should no longer show it.
		res = e.run(t, repo, "worktree", "list", "--json")
		if strings.Contains(res.stdout, `"manual"`) {
			t.Errorf("expected unregistered worktree to drop from active list:\n%s", res.stdout)
		}
	})
}

// ── treeman wt logs (shorthand for `logs tail --worktree`) ──────

func TestWtLogsShorthand(t *testing.T) {
	e := newEnv(t)
	repo, _, _ := makeWorktreeFixture(t, e)
	res := e.run(t, repo, "worktree", "logs", "feat_a")
	if res.err != nil {
		t.Fatalf("wt logs: %v\nstderr:\n%s", res.err, res.stderr)
	}
	if !strings.Contains(res.stdout, "fixture_event") {
		t.Errorf("wt logs shorthand should surface seeded event:\n%s", res.stdout)
	}
}

// ── treeman wt wait (no daemon → timeout error fast) ────────────

func TestWtWait(t *testing.T) {
	e := newEnv(t)
	repo, _, _ := makeWorktreeFixture(t, e)
	// 1s timeout is plenty to confirm exit-nonzero behavior without
	// blocking the test suite. A missing finalize event = the wait
	// should NOT find one in the registry and should report so.
	res := e.run(t, repo, "worktree", "wait", "feat_a", "--timeout", "1s", "--quiet")
	if res.err == nil {
		t.Errorf("expected wt wait to error when no finalize event present, got stdout:\n%s", res.stdout)
	}
}

// TestColorFlag pins the --color global override: never strips ANSI
// even on a TTY-shaped run, auto stays quiet on pipes, always forces
// ANSI (once NO_COLOR is out of the way), and NO_COLOR beats always.
func TestColorFlag(t *testing.T) {
	repo := newGitRepo(t)
	e := newEnv(t)

	res := e.run(t, repo, "--color=never", "wt", "list")
	if strings.Contains(res.stdout+res.stderr, "\x1b[") {
		t.Errorf("--color=never emitted ANSI codes:\n%q", res.stdout)
	}

	res = e.run(t, repo, "wt", "list")
	if strings.Contains(res.stdout+res.stderr, "\x1b[") {
		t.Errorf("auto on a pipe emitted ANSI codes:\n%q", res.stdout)
	}

	res = e.runColor(t, repo, "--color=always", "wt", "list")
	if !strings.Contains(res.stdout+res.stderr, "\x1b[") {
		t.Errorf("--color=always emitted no ANSI codes:\n%q", res.stdout)
	}

	// NO_COLOR beats --color=always (ecosystem convention).
	res = e.run(t, repo, "--color=always", "wt", "list")
	if strings.Contains(res.stdout+res.stderr, "\x1b[") {
		t.Errorf("NO_COLOR=1 must suppress --color=always:\n%q", res.stdout)
	}

	res = e.run(t, repo, "--color=bogus", "wt", "list")
	if res.err == nil {
		t.Error("invalid --color value must error")
	}
}

// ── treeman wt delete dispatches to daemon ───────────────────────

func TestWtDeleteDispatch(t *testing.T) {
	// Engine teardown + git worktree removal are daemon-driven and
	// covered by the cli/ + engine-specific suites. Here we just
	// confirm the CLI surface accepts the command shape and emits the
	// "queued/teardown" status line — that's what the cd-substitution
	// shell shim depends on for the early-return contract.
	e := newEnv(t)
	repo, wtA, _ := makeWorktreeFixture(t, e)
	// --yes skips the dirty/unpushed confirm (the fixture dir isn't a
	// real git worktree, so the guard reads it as dirty); this test
	// pins the dispatch surface, not the confirm policy — that's
	// TestWtDeleteConfirmNonTTY.
	res := e.run(t, repo, "worktree", "delete", "--force", "--yes", "feat_a")
	combined := res.stdout + res.stderr
	if !strings.Contains(combined, "queued") && !strings.Contains(combined, "teardown") &&
		!strings.Contains(combined, "daemon") {
		t.Errorf("wt delete should mention queued/teardown/daemon status:\nstdout:\n%s\nstderr:\n%s",
			res.stdout, res.stderr)
	}
	// A clean worktree gets no confirmation, so the announcement line
	// naming the resolved target is the only pre-teardown output.
	if !strings.Contains(combined, "deleting "+wtA) {
		t.Errorf("wt delete should announce the resolved target %q:\nstdout:\n%s\nstderr:\n%s",
			wtA, res.stdout, res.stderr)
	}
}

// TestWtDeleteConfirmNonTTY pins the destructive-confirm refusal: a
// piped (non-TTY) `wt delete` of a DIRTY worktree must not auto-answer
// the destroy prompt — it aborts with the --yes hint and leaves the
// worktree registered; --yes is the scripting opt-in that proceeds.
func TestWtDeleteConfirmNonTTY(t *testing.T) {
	repo := newGitRepo(t)
	e := newEnv(t)
	dirty := filepath.Join(repo, ".worktrees", "dirty")
	mustGit(t, repo, "worktree", "add", "-b", "dirty-branch", dirty, "HEAD")
	if err := os.WriteFile(filepath.Join(dirty, "uncommitted.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := e.run(t, dirty, "worktree", "register", "--branch", "dirty-branch")
	if res.err != nil {
		t.Fatalf("worktree register: %v\nstderr:\n%s", res.err, res.stderr)
	}

	res = e.run(t, repo, "worktree", "delete", "dirty")
	if res.err != nil {
		t.Fatalf("refused delete must exit 0: %v\nstderr:\n%s", res.err, res.stderr)
	}
	if !strings.Contains(res.stdout+res.stderr, "--yes") {
		t.Errorf("refused dirty delete should hint at --yes:\nstdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
	}
	res = e.run(t, repo, "worktree", "list", "--json")
	if !strings.Contains(res.stdout, `"dirty"`) {
		t.Errorf("refused delete must leave the worktree registered:\n%s", res.stdout)
	}

	// --yes is the non-interactive opt-in that lets the teardown run.
	res = e.run(t, repo, "worktree", "delete", "--yes", "dirty")
	if res.err != nil {
		t.Fatalf("delete --yes: %v\nstderr:\n%s", res.err, res.stderr)
	}
}

// TestWtDeleteBatchConfirm pins the multi-target confirmation (#77):
// two dirty targets non-interactively refuse as a batch (one --yes
// hint, both worktrees intact); with --yes the teardown runs with zero
// prompts and the picked-set summary lands on stderr before the first
// teardown message.
func TestWtDeleteBatchConfirm(t *testing.T) {
	repo := newGitRepo(t)
	e := newEnv(t)
	makeDirty := func(name string) string {
		p := filepath.Join(repo, ".worktrees", name)
		mustGit(t, repo, "worktree", "add", "-b", name+"-branch", p, "HEAD")
		if err := os.WriteFile(filepath.Join(p, "uncommitted.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		res := e.run(t, p, "worktree", "register", "--branch", name+"-branch")
		if res.err != nil {
			t.Fatalf("register %s: %v\nstderr:\n%s", name, res.err, res.stderr)
		}
		return p
	}
	alpha := makeDirty("alpha")
	beta := makeDirty("beta")

	// Without --yes: the batch confirm refuses (non-TTY) after naming
	// BOTH targets with their reasons — and nothing is destroyed.
	res := e.run(t, repo, "worktree", "delete", alpha, beta)
	if res.err != nil {
		t.Fatalf("refused batch delete must exit 0: %v\nstderr:\n%s", res.err, res.stderr)
	}
	combined := res.stdout + res.stderr
	for _, want := range []string{"unregenerable state", "alpha", "beta", "uncommitted changes"} {
		if !strings.Contains(combined, want) {
			t.Errorf("batch confirm output missing %q:\nstdout:\n%s\nstderr:\n%s", want, res.stdout, res.stderr)
		}
	}
	if strings.Count(combined, "--yes") != 1 {
		t.Errorf("batch refusal should hint --yes exactly once (one batch question, not one per target):\nstdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
	}
	list := e.run(t, repo, "worktree", "list", "--json")
	if !strings.Contains(list.stdout, `"alpha-branch"`) || !strings.Contains(list.stdout, `"beta-branch"`) {
		t.Errorf("declined batch must leave both worktrees registered:\n%s", list.stdout)
	}

	// With --yes: zero prompts, both torn down; the picked-set summary
	// (picker path only) is absent for named args, but the batch block
	// must not appear either.
	res = e.run(t, repo, "worktree", "delete", "--yes", alpha, beta)
	if res.err != nil {
		t.Fatalf("batch delete --yes: %v\nstderr:\n%s", res.err, res.stderr)
	}
	// Teardown dispatches to the daemon and returns immediately —
	// poll until both rows leave the registry.
	deadline := time.Now().Add(15 * time.Second)
	for {
		list = e.run(t, repo, "worktree", "list", "--json")
		if !strings.Contains(list.stdout, `"alpha-branch"`) && !strings.Contains(list.stdout, `"beta-branch"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("batch --yes must delete both worktrees; still registered:\n%s", list.stdout)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// TestWtSwitchConsolidatedIntoGo pins the #91 consolidation: the
// legacy `switch` spelling produces the same stdout as `go --checkout`
// for the same input, and `worktree --help` lists one navigation entry
// (switch is hidden, not a second overlapping command).
func TestWtSwitchConsolidatedIntoGo(t *testing.T) {
	repo := newGitRepo(t)
	e := newEnv(t)
	t.Cleanup(func() { stopDaemon(t, e) })
	writeConfig(t, repo, minimalConfig)
	wtA := filepath.Join(repo, ".worktrees", "feat_a")
	mustGit(t, repo, "worktree", "add", "-b", "feature/a", wtA, "HEAD")
	res := e.run(t, wtA, "worktree", "register", "--branch", "feature/a")
	if res.err != nil {
		t.Fatalf("register: %v\nstderr:\n%s", res.err, res.stderr)
	}

	t.Run("switch and go --checkout print the same path", func(t *testing.T) {
		viaSwitch := e.run(t, repo, "worktree", "switch", "feature/a")
		viaGo := e.run(t, repo, "worktree", "go", "--checkout", "feature/a")
		if viaSwitch.err != nil {
			t.Fatalf("switch: %v\nstderr:\n%s", viaSwitch.err, viaSwitch.stderr)
		}
		if viaGo.err != nil {
			t.Fatalf("go --checkout: %v\nstderr:\n%s", viaGo.err, viaGo.stderr)
		}
		if strings.TrimSpace(viaSwitch.stdout) != wtA {
			t.Errorf("switch stdout = %q, want %q", strings.TrimSpace(viaSwitch.stdout), wtA)
		}
		if strings.TrimSpace(viaSwitch.stdout) != strings.TrimSpace(viaGo.stdout) {
			t.Errorf("switch and go --checkout diverge:\nswitch: %q\ngo:     %q", viaSwitch.stdout, viaGo.stdout)
		}
	})

	t.Run("help shows one navigation entry", func(t *testing.T) {
		res := e.run(t, repo, "worktree", "--help")
		if res.err != nil {
			t.Fatalf("worktree --help: %v\nstderr:\n%s", res.err, res.stderr)
		}
		if strings.Contains(res.stdout+res.stderr, "switch to or create a branch's worktree") {
			t.Errorf("legacy switch entry still advertised in help:\n%s", res.stdout+res.stderr)
		}
		if !strings.Contains(res.stdout+res.stderr, "worktree go") {
			t.Errorf("go entry missing from help:\n%s", res.stdout+res.stderr)
		}
	})
}

// TestInitEngineAndInteractive pins the #71 acceptance criteria:
// `init --engine postgres` produces a config where `config validate`
// passes with an active databases: block, and `init --interactive`
// completes non-interactively (declines the picker) when stdin is not
// a TTY.
func TestInitEngineAndInteractive(t *testing.T) {
	t.Run("--engine activates a valid databases block", func(t *testing.T) {
		repo := newGitRepo(t)
		e := newEnv(t)
		res := e.run(t, repo, "init", "--engine", "postgres,redis")
		if res.err != nil {
			t.Fatalf("init --engine: %v\nstderr:\n%s", res.err, res.stderr)
		}
		validate := e.run(t, repo, "config", "validate")
		if validate.err != nil {
			t.Fatalf("config validate after init --engine: %v\nstdout:\n%s\nstderr:\n%s",
				validate.err, validate.stdout, validate.stderr)
		}
		body, err := os.ReadFile(filepath.Join(repo, ".treeman.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		for _, want := range []string{"engine: postgres", "engine: redis", "key_prefix"} {
			if !strings.Contains(text, want) {
				t.Errorf("scaffold missing %q:\n%s", want, text)
			}
		}
	})

	t.Run("--interactive declines without a TTY and still scaffolds", func(t *testing.T) {
		repo := newGitRepo(t)
		e := newEnv(t)
		res := e.run(t, repo, "init", "--interactive")
		if res.err != nil {
			t.Fatalf("init --interactive non-TTY: %v\nstderr:\n%s", res.err, res.stderr)
		}
		combined := res.stdout + res.stderr
		if !strings.Contains(combined, "needs a terminal") {
			t.Errorf("non-TTY --interactive should say so:\n%s", combined)
		}
		if _, err := os.Stat(filepath.Join(repo, ".treeman.yaml")); err != nil {
			t.Errorf("scaffold should still be written: %v", err)
		}
	})
}

// TestRequireDaemonStrictMode pins the strict-daemon contract (#75):
// with --require-daemon (or TREEMAN_REQUIRE_DAEMON=1) and no daemon,
// submitPlan-backed commands fail fast naming `treeman daemon start`
// instead of silently running in-process; without the flag the
// daemon-less fallback keeps working, and the flag is a no-op when
// the daemon IS reachable.
func TestRequireDaemonStrictMode(t *testing.T) {
	repo := newGitRepo(t)
	e := newEnv(t)
	writeConfig(t, repo, minimalConfig)

	t.Run("flag fails fast with the start hint", func(t *testing.T) {
		res := e.run(t, repo, "--require-daemon", "prepare", "--repo", repo, "--worktree", repo)
		if res.err == nil {
			t.Fatal("strict mode with daemon down should fail")
		}
		combined := res.stdout + res.stderr
		for _, want := range []string{"--require-daemon", "treeman daemon start"} {
			if !strings.Contains(combined, want) {
				t.Errorf("strict failure missing %q:\nstdout:\n%s\nstderr:\n%s", want, res.stdout, res.stderr)
			}
		}
		if strings.Contains(combined, "running in-process") {
			t.Errorf("strict mode must not fall back in-process:\n%s", combined)
		}
	})

	t.Run("env var behaves like the flag", func(t *testing.T) {
		res := e.runEnv(t, repo, []string{"TREEMAN_REQUIRE_DAEMON=1"}, "db", "reset", "--repo", repo)
		if res.err == nil {
			t.Fatal("TREEMAN_REQUIRE_DAEMON=1 with daemon down should fail")
		}
		if !strings.Contains(res.stdout+res.stderr, "--require-daemon") {
			t.Errorf("env-driven strict failure missing the flag hint:\nstdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
		}
	})

	t.Run("without the flag the fallback still works", func(t *testing.T) {
		// Engine-free config: the fallback's in-process prepare must get
		// past the daemon-unreachable warn and fail (or succeed) on its
		// own merits — here it runs to completion because there are no
		// databases to prepare.
		engineFree := newGitRepo(t)
		writeConfig(t, engineFree, "worktrees:\n  root: .worktrees\n")
		res := e.run(t, engineFree, "prepare", "--repo", engineFree, "--worktree", engineFree)
		if res.err != nil {
			t.Fatalf("daemon-less fallback should still work: %v\nstderr:\n%s", res.err, res.stderr)
		}
		if !strings.Contains(res.stdout+res.stderr, "running in-process") {
			t.Errorf("fallback should announce the in-process run:\n%s", res.stdout+res.stderr)
		}
	})
}

// ── treeman wt alias ──────────────────────────────────────────

func TestWtAlias(t *testing.T) {
	t.Run("alias routes identically to the full spelling", func(t *testing.T) {
		repo := newGitRepo(t)
		e := newEnv(t)
		viaAlias := e.run(t, repo, "wt", "list")
		viaFull := e.run(t, repo, "worktree", "list")
		if viaAlias.err != nil {
			t.Fatalf("wt list: %v\nstderr:\n%s", viaAlias.err, viaAlias.stderr)
		}
		if viaAlias.stdout != viaFull.stdout {
			t.Errorf("wt list output diverges from worktree list:\nalias:\n%s\nfull:\n%s",
				viaAlias.stdout, viaFull.stdout)
		}
	})

	t.Run("bare wt shows the worktree help", func(t *testing.T) {
		e := newEnv(t)
		res := e.run(t, e.home, "wt")
		if res.err != nil {
			t.Fatalf("bare wt: %v\nstderr:\n%s", res.err, res.stderr)
		}
		combined := res.stdout + res.stderr
		for _, want := range []string{"worktree lifecycle", "worktree [command"} {
			if !strings.Contains(combined, want) {
				t.Errorf("bare wt help missing %q:\n%s", want, combined)
			}
		}
	})

	t.Run("typo near the alias suggests wt", func(t *testing.T) {
		e := newEnv(t)
		res := e.run(t, e.home, "wtt", "list")
		if res.err == nil {
			t.Errorf("expected wtt to fail as an unknown command")
		}
		if !strings.Contains(res.stdout+res.stderr, "wt") {
			t.Errorf("wtt should suggest wt:\nstdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
		}
	})
}
