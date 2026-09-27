// Package ui provides terminal styling for the treeman CLI.
//
// Color detection follows the standard rules: respect NO_COLOR,
// honor FORCE_COLOR / CLICOLOR_FORCE, otherwise enable color only
// when stdout is a TTY. Styles degrade to plain text everywhere
// else (pipes, CI, redirects, dumb terminals).
package ui

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	ctermsize "github.com/charmbracelet/x/term"
	"github.com/mattn/go-isatty"
)

// ANSI SGR codes used by the helpers below.
const (
	ansiReset   = "\x1b[0m"
	ansiBold    = "\x1b[1m"
	ansiDim     = "\x1b[2m"
	ansiRed     = "\x1b[31m"
	ansiGreen   = "\x1b[32m"
	ansiYellow  = "\x1b[33m"
	ansiBlue    = "\x1b[34m"
	ansiMagenta = "\x1b[35m"
	ansiCyan    = "\x1b[36m"
	ansiGray    = "\x1b[90m"
)

var (
	colorOnce   sync.Once
	colorEnable bool
	// colorMode carries the --color flag override: "" (unset),
	// "auto" (env/TTY detection), "always" or "never". The flag is a
	// per-invocation override, so it must survive the once-guarded
	// detection and re-win on every later EnableColorForStderr call.
	colorMode string

	// symbol set — switched to ASCII when the locale looks non-UTF8.
	SymSuccess = "✓"
	SymError   = "✗"
	SymWarn    = "!"
	SymInfo    = "•"
	SymArrow   = "→"
	SymDot     = "·"

	// picker glyphs (internal/tui).
	SymPointer = "❯"
	SymMarkOn  = "●"
	SymMarkOff = "○"
)

// ColorEnabled reports whether ANSI sequences should be emitted.
// Lazy because os.Stdout's TTY status is stable for a process lifetime.
func ColorEnabled() bool {
	colorOnce.Do(detectColor)
	return colorEnable
}

func detectColor() {
	if os.Getenv("NO_COLOR") != "" {
		colorEnable = false
		return
	}
	if os.Getenv("FORCE_COLOR") != "" || os.Getenv("CLICOLOR_FORCE") != "" {
		colorEnable = true
		return
	}
	if t := os.Getenv("TERM"); t == "dumb" {
		colorEnable = false
		return
	}
	colorEnable = isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
	if !colorEnable {
		return
	}
	lang := strings.ToLower(os.Getenv("LANG") + os.Getenv("LC_ALL") + os.Getenv("LC_CTYPE"))
	if !strings.Contains(lang, "utf") && os.Getenv("TERM_PROGRAM") == "" {
		SymSuccess, SymError, SymWarn, SymInfo, SymArrow, SymDot = "[ok]", "[x]", "[!]", "*", "->", "."
		SymPointer, SymMarkOn, SymMarkOff = ">", "[x]", "[ ]"
	}
}

// SetColorMode applies the --color flag: "auto" (default) leaves
// env/TTY detection in charge, "always" forces color on (NO_COLOR
// still wins — ecosystem convention), "never" forces it off for this
// invocation, including the stderr re-enable pickers use.
func SetColorMode(mode string) error {
	switch mode {
	case "", "auto":
		colorMode = "auto"
		return nil
	case "always", "never":
		colorMode = mode
		colorOnce.Do(detectColor)
		switch {
		case mode == "never":
			colorEnable = false
		case os.Getenv("NO_COLOR") == "":
			colorEnable = true
		}
		return nil
	default:
		return fmt.Errorf("invalid --color %q (want auto|always|never)", mode)
	}
}

// EnableColorForStderr turns styling on when stderr is a terminal, for
// interactive TUIs that render to stderr while stdout is captured by a
// shell substitution (`cd "$(treeman worktree switch)"`). The default
// detection keys off stdout, which is a pipe in exactly that case.
// NO_COLOR, TERM=dumb and --color=never still win.
func EnableColorForStderr() {
	colorOnce.Do(detectColor)
	if colorMode == "never" || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return
	}
	if isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd()) {
		colorEnable = true
	}
}

// wrap returns s wrapped in `prefix`...`reset` when color is on,
// or s unchanged when color is off.
func wrap(prefix, s string) string {
	if !ColorEnabled() {
		return s
	}
	return prefix + s + ansiReset
}

// Style helpers — return the styled string without printing.
func Green(s string) string   { return wrap(ansiGreen, s) }
func Red(s string) string     { return wrap(ansiRed, s) }
func Yellow(s string) string  { return wrap(ansiYellow, s) }
func Blue(s string) string    { return wrap(ansiBlue, s) }
func Magenta(s string) string { return wrap(ansiMagenta, s) }
func Cyan(s string) string    { return wrap(ansiCyan, s) }
func Gray(s string) string    { return wrap(ansiGray, s) }
func Dim(s string) string     { return wrap(ansiDim, s) }
func Bold(s string) string    { return wrap(ansiBold, s) }

// Level colorizes a log-level token.
func Level(level string) string {
	switch strings.ToLower(level) {
	case "error":
		return Red(level)
	case "warn", "warning":
		return Yellow(level)
	case "info":
		return Blue(level)
	case "debug":
		return Gray(level)
	}
	return level
}

// EventType colorizes a treeman event_type token. Names that imply a
// state transition pick up an accent; informational events stay dim.
func EventType(et string) string {
	switch {
	case strings.HasSuffix(et, "_error") || strings.HasSuffix(et, "_failed"):
		return Red(et)
	case strings.HasSuffix(et, "_done") || strings.HasSuffix(et, "_complete"):
		return Green(et)
	case strings.HasSuffix(et, "_start") || strings.HasSuffix(et, "_begin") || strings.HasSuffix(et, "_queued"):
		return Cyan(et)
	case strings.HasSuffix(et, "_hit"):
		return Magenta(et)
	}
	return et
}

// Status colorizes worktree / daemon status tokens.
func Status(s string) string {
	switch strings.ToLower(s) {
	case "running", "ready", "ok", "active":
		return Green(s)
	case "stopped", "deleted", "gone":
		return Gray(s)
	case "preparing", "pending", "queued", "deferred":
		return Yellow(s)
	case "error", "broken", "failed":
		return Red(s)
	}
	return s
}

// StripANSI removes CSI escape sequences for accurate width math.
func StripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	inEsc := false
	for _, r := range s {
		if r == 0x1b {
			inEsc = true
			continue
		}
		if inEsc {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEsc = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Width returns the visible width of s: ANSI-stripped, counted in
// runes (not bytes) so multi-byte symbols like ★ or → pad correctly.
func Width(s string) int { return utf8.RuneCountInString(StripANSI(s)) }

// defaultTermWidth is the fallback when stdout isn't a terminal
// (pipes, CI, e2e captures): the classic terminal width.
const defaultTermWidth = 80

// TermWidth returns the terminal width in columns for line-fitting:
// $COLUMNS wins when set to a sane integer (tests and per-invocation
// overrides), else the stdout window size, else 80. Callers use it to
// truncate (Truncate, Table.SetWidth) instead of letting long paths
// wrap and shred alignment. Deliberately not cached: the ioctl is one
// syscall per render, and a cached value goes stale under $COLUMNS
// overrides and test isolation alike.
func TermWidth() int {
	if v := os.Getenv("COLUMNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 20 && n <= 1000 {
			return n
		}
	}
	if isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd()) {
		if w, _, err := ctermsize.GetSize(os.Stdout.Fd()); err == nil && w > 0 {
			return w
		}
	}
	return defaultTermWidth
}

// Truncate cuts s to at most w display columns, ending with an
// ellipsis when anything was cut. ANSI-aware: styled input keeps its
// escape codes balanced so downstream width math stays correct.
func Truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if Width(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}

// Out is where styled output goes. Tests override this; PagerStart
// also retargets it for the duration of a pager session.
var Out io.Writer = os.Stdout

// Err is where error / warn output goes.
var Err io.Writer = os.Stderr

// fprintln writes msg + newline to w.
func fprintln(w io.Writer, msg string) {
	_, _ = fmt.Fprintln(w, msg)
}

// IsTTY reports whether stdout is connected to a terminal. Cached
// via ColorEnabled's detect path. Useful when a command wants to
// switch behaviour for interactive vs piped use without re-running
// the syscall.
func IsTTY() bool {
	return isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
}
