package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
)

// In is the input stream for interactive prompts. Overridable for
// tests; defaults to os.Stdin.
var In io.Reader = os.Stdin

// Confirm asks the user `<question> [y/N]: ` and reports whether
// the operation should proceed.
//
// Returns false without prompting when stdin is not a TTY — a piped,
// CI, or shell-script invocation has no one to answer, so auto-yes
// would silently destroy state (the historical behaviour; scripts
// must now opt in via the command's `--yes/-y` flag, which every
// destructive command exposes).
//
// The prompt is written to stderr so stdout (often consumed by
// shell integrations: `cd "$(treeman worktree go foo)"`) stays
// clean.
func Confirm(question string) bool {
	if refuseNonTTY() {
		return false
	}
	_, _ = fmt.Fprint(Err, Yellow(SymWarn)+" "+question+" [y/N]: ")
	line, err := bufio.NewReader(In).ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// ConfirmYes is Confirm with a YES default: Enter (or anything that
// isn't n/N) proceeds, only an explicit no cancels. This is the
// warn-but-don't-obstruct contract of the old zsh _confirm_destructive
// prompts (push guard, stash clear, wipe) — the user is being warned,
// not gatekept. Same non-TTY refusal as Confirm.
func ConfirmYes(question string) bool {
	if refuseNonTTY() {
		return false
	}
	_, _ = fmt.Fprint(Err, Yellow(SymWarn)+" "+question+" ["+Green("Y")+"/"+Red("n")+"]: ")
	line, err := bufio.NewReader(In).ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "n", "no":
		return false
	}
	return true
}

// ConfirmAutoYes is ConfirmYes with the historical non-TTY behaviour:
// without a terminal the question is answered yes. Only for
// informational warn-and-continue gates whose "no" answer skips an
// optional safety, never for prompts guarding destructive work —
// those use Confirm/ConfirmYes, which refuse to auto-proceed without
// a TTY.
func ConfirmAutoYes(question string) bool {
	if !isatty.IsTerminal(os.Stdin.Fd()) && !isatty.IsCygwinTerminal(os.Stdin.Fd()) {
		return true
	}
	_, _ = fmt.Fprint(Err, Yellow(SymWarn)+" "+question+" ["+Green("Y")+"/"+Red("n")+"]: ")
	line, err := bufio.NewReader(In).ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "n", "no":
		return false
	}
	return true
}

// refuseNonTTY reports whether the session has no human to answer a
// prompt, printing the --yes hint when it does.
func refuseNonTTY() bool {
	if isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd()) {
		return false
	}
	Hint("non-interactive session — re-run with --yes to allow this")
	return true
}
