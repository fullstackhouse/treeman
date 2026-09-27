package ui

import (
	"regexp"
	"sort"
	"strings"
)

// cmdHintRe finds a backticked shell command inside an error message —
// the convention error authors use to embed the remediation command.
var cmdHintRe = regexp.MustCompile("`([^`]+)`")

// knownHints maps error-substring classes to a suggested next command,
// so failures that span many call sites get one curated remediation
// (the UX doctor's --fix hints already model).
var knownHints = []struct {
	match string
	hint  string
}{
	{"daemon unreachable", "treeman daemon start"},
	{"registry drift", "treeman doctor --fix"},
	{"schema out of date", "treeman doctor --fix"},
}

// RenderError prints err as: a red ✗ line with the top-level message,
// one dim indented line per unwrapped/joined cause, and — when the
// leaf message or a known error class names a backticked command — a
// dim `run: <cmd>` hint (#73). NO_COLOR / non-TTY output degrades to
// plain text through the same writers as every other ui call.
func RenderError(err error) {
	if err == nil {
		return
	}
	Error("%v", err)
	for _, cause := range causes(err) {
		Hint("↳ %v", cause)
	}
	for _, cmd := range hintCommands(err) {
		Hint("run: %s", cmd)
	}
}

// causes flattens err's Unwrap chain and Join trees (root-first,
// without the top-level message itself), so `fmt.Errorf("finalize:
// %w", cause)` renders cause and wrapper on separate lines.
func causes(err error) []error {
	var out []error
	var walk func(e error)
	walk = func(e error) {
		switch x := e.(type) { //nolint:errorlint // walking the unwrap chain is the point here, not matching a target type
		case interface{ Unwrap() error }:
			if inner := x.Unwrap(); inner != nil {
				out = append(out, inner)
				walk(inner)
			}
		case interface{ Unwrap() []error }:
			for _, inner := range x.Unwrap() {
				out = append(out, inner)
				walk(inner)
			}
		}
	}
	walk(err)
	return out
}

// hintCommands collects the remediation commands for err: a curated
// known-class match first, then backticked commands found in the leaf
// (deepest) message only — wrapper context like `finalize: ...` must
// not resurrect hints from prose. Sorted + deduped.
func hintCommands(err error) []string {
	var cmds []string
	msg := err.Error()
	for _, kh := range knownHints {
		if strings.Contains(msg, kh.match) {
			cmds = append(cmds, kh.hint)
			break
		}
	}
	if leaf := deepest(err); leaf != nil {
		for _, m := range cmdHintRe.FindAllStringSubmatch(leaf.Error(), -1) {
			cmds = append(cmds, m[1])
		}
	}
	sort.Strings(cmds)
	return slicesCompact(cmds)
}

// deepest returns the last error in the unwrap chain (the leaf
// message a caller actually wrote).
func deepest(err error) error {
	for {
		switch x := err.(type) { //nolint:errorlint // walking the unwrap chain is the point here, not matching a target type
		case interface{ Unwrap() error }:
			if inner := x.Unwrap(); inner != nil {
				err = inner
				continue
			}
		case interface{ Unwrap() []error }:
			joined := x.Unwrap()
			if len(joined) == 0 {
				return err
			}
			return joined[len(joined)-1]
		}
		return err
	}
}

func slicesCompact(sorted []string) []string {
	out := sorted[:0]
	var prev string
	for i, s := range sorted {
		if i == 0 || s != prev {
			out = append(out, s)
		}
		prev = s
	}
	return out
}
