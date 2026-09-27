package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/urfave/cli/v3"

	"github.com/stubbedev/treeman/internal/store"
	"github.com/stubbedev/treeman/internal/tui"
	"github.com/stubbedev/treeman/internal/ui"
	"github.com/stubbedev/treeman/pkg/rpc"
)

// TuiCmd is the exported command wrapper app.go registers.
func TuiCmd() *cli.Command {
	return tuiCmd()
}

// tuiCmd — `treeman tui` opens a full-screen dashboard: worktree rows
// on the left, the live daemon event stream on the right (fed by
// rpc.SubscribeEvents, NOT by polling SQLite), and a daemon-state
// status line at the bottom.
func tuiCmd() *cli.Command {
	return &cli.Command{
		Name:  "tui",
		Usage: "full-screen dashboard: worktrees + live daemon events (needs a TTY)",
		Action: func(ctx context.Context, c *cli.Command) error {
			if !tui.Interactive() {
				ui.Hint(
					"%s",
					"treeman tui needs a terminal — when piping use `worktree list`, `logs tail --follow` and `daemon state` instead",
				)
				return tui.ErrNotTTY
			}
			return runDashboard(ctx)
		},
	}
}

// runDashboard opens the store + subscription the model needs, then
// hands off to bubbletea. Both handles close when the program exits.
func runDashboard(ctx context.Context) error {
	dbPath, _ := store.DefaultDBPath()
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	events, stopEvents, err := rpc.SubscribeEvents(ctx, rpc.EventSubscribeArgs{})
	if err != nil {
		return fmt.Errorf("event subscription: %w", err)
	}
	defer stopEvents()

	m := newDashboard(ctx, st, events)
	p := tea.NewProgram(&m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

// ─────────────────────────── model ───────────────────────────

type tickMsg struct{}

type eventMsg struct{ ev rpc.EventEnvelope }

type eventsClosedMsg struct{}

type rowsMsg struct {
	rows []wtRow
	err  error
}

type stateMsg struct {
	snap *rpc.DaemonStateSnapshot
	err  error
}

type dashboard struct {
	ctx    context.Context
	st     *store.Store
	events <-chan rpc.EventEnvelope

	rows   []wtRow
	cursor int

	// Filtering mirrors tui.Select's query machinery: `/` opens the
	// query, printable keys extend it, esc clears, and rows match by
	// case-insensitive substring over slug/branch/path.
	filter  string
	filterQ bool

	evLines []string
	state   string
	width   int
	height  int
	err     error
}

func newDashboard(ctx context.Context, st *store.Store, events <-chan rpc.EventEnvelope) dashboard {
	return dashboard{
		ctx:    ctx,
		st:     st,
		events: events,
		state:  "connecting…",
	}
}

func (d dashboard) Init() tea.Cmd {
	return tea.Batch(
		d.loadRows(),
		d.loadState(),
		waitForEvent(d.events),
		tickCmd(),
	)
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

// waitForEvent re-arms the subscription read so exactly one read is
// in flight per event.
func waitForEvent(ch <-chan rpc.EventEnvelope) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return eventsClosedMsg{}
		}
		return eventMsg{ev}
	}
}

func (d dashboard) loadRows() tea.Cmd {
	return func() tea.Msg {
		rows, err := loadWtRows(d.ctx, d.st, "", "id")
		return rowsMsg{rows: rows, err: err}
	}
}

func (d dashboard) loadState() tea.Cmd {
	return func() tea.Msg {
		resp, err := rpc.Call(d.ctx, rpc.Request{Method: rpc.MethodDaemonState})
		if err != nil {
			return stateMsg{err: err}
		}
		if resp.State == nil {
			return stateMsg{err: errors.New("empty daemon state")}
		}
		return stateMsg{snap: resp.State}
	}
}

func (d *dashboard) filtered() []wtRow {
	if d.filter == "" {
		return d.rows
	}
	needle := strings.ToLower(d.filter)
	out := make([]wtRow, 0, len(d.rows))
	for _, r := range d.rows {
		if strings.Contains(strings.ToLower(r.Slug), needle) ||
			strings.Contains(strings.ToLower(r.Branch), needle) ||
			strings.Contains(strings.ToLower(r.Path), needle) {
			out = append(out, r)
		}
	}
	return out
}

func (d *dashboard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.width, d.height = msg.Width, msg.Height
		return d, nil

	case tickMsg:
		return d, tea.Batch(d.loadRows(), d.loadState(), tickCmd())

	case rowsMsg:
		if msg.err != nil {
			d.err = msg.err
		} else {
			d.err = nil
			sort.SliceStable(msg.rows, func(i, j int) bool { return msg.rows[i].ID < msg.rows[j].ID })
			d.rows = msg.rows
			if d.cursor >= len(d.filtered()) {
				d.cursor = 0
			}
		}
		return d, nil

	case stateMsg:
		if msg.err != nil {
			d.state = "daemon unreachable"
		} else {
			d.state = fmt.Sprintf("watchers=%d finalizing=%d teardowns=%d backoffs=%d",
				msg.snap.WatcherCount,
				len(msg.snap.InFlightFinalizes),
				len(msg.snap.InFlightTeardowns),
				len(msg.snap.SyncBackoffs))
		}
		return d, nil

	case eventMsg:
		d.appendEventLine(msg.ev)
		return d, waitForEvent(d.events)

	case eventsClosedMsg:
		d.state = "event stream closed"
		return d, nil

	case tea.KeyMsg:
		return d.handleKey(msg)
	}
	return d, nil
}

// appendEventLine renders one envelope the same way `logs tail` does:
// time, colored level + event type, message.
func (d *dashboard) appendEventLine(ev rpc.EventEnvelope) {
	ts := time.UnixMilli(ev.Ts).Format("15:04:05")
	line := fmt.Sprintf("%s %s %s %s", ui.Dim(ts), ui.Level(ev.Level), ui.EventType(ev.EventType), ev.Message)
	d.evLines = append(d.evLines, line)
	if len(d.evLines) > 1000 {
		d.evLines = d.evLines[len(d.evLines)-1000:]
	}
}

func (d *dashboard) handleKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if d.filterQ {
		switch key.Type { //nolint:exhaustive // unmatched keys are printable rune appends
		case tea.KeyEnter, tea.KeyEsc:
			d.filterQ = false
		case tea.KeyBackspace:
			if r := []rune(d.filter); len(r) > 0 {
				d.filter = string(r[:len(r)-1])
			}
		case tea.KeyRunes:
			d.filter += string(key.Runes)
		}
		return d, nil
	}
	switch key.String() {
	case "q", "ctrl+c":
		return d, tea.Quit
	case "up", "k":
		if d.cursor > 0 {
			d.cursor--
		}
	case "down", "j":
		if d.cursor < len(d.filtered())-1 {
			d.cursor++
		}
	case "/":
		d.filterQ = true
	case "esc":
		d.filter = ""
	case "enter":
		rows := d.filtered()
		if d.cursor < len(rows) {
			self, err := os.Executable()
			if err != nil {
				return d, nil
			}
			show := exec.CommandContext(d.ctx, self, "worktree", "show", rows[d.cursor].Slug, "--no-pager")
			return d, tea.ExecProcess(show, func(error) tea.Msg { return tickMsg{} })
		}
	}
	return d, nil
}

// ─────────────────────────── view ───────────────────────────

func (d dashboard) View() string {
	if d.width == 0 {
		return "loading…"
	}
	leftW := min(d.width/2, 48)
	leftW = max(leftW, 20)
	bodyH := d.height - 2
	bodyH = max(bodyH, 5)

	left := d.renderRows(bodyH - 1)
	right := d.renderEvents(bodyH-1, leftW)
	var b strings.Builder
	for i := range bodyH - 1 {
		b.WriteString(pad(left[i], leftW))
		b.WriteString(ui.Dim("│"))
		b.WriteString(right[i])
		b.WriteString("\n")
	}
	filter := "no filter ( / to filter )"
	if d.filterQ {
		filter = "filter: " + d.filter + "█"
	} else if d.filter != "" {
		filter = "filter: " + d.filter
	}
	b.WriteString(ui.Dim(strings.Join([]string{
		pad(truncate(filter, leftW), leftW),
		d.state,
	}, "│")))
	b.WriteString("\n")
	return b.String()
}

func (d *dashboard) renderRows(height int) []string {
	rows := d.filtered()
	out := make([]string, 0, height)
	out = append(out, ui.Bold(" WORKTREES"))
	if d.err != nil {
		out = append(out, ui.Red(" "+d.err.Error()))
	}
	shown := rows
	if len(shown) > height-2 {
		shown = shown[:height-2]
	}
	for i, r := range shown {
		marker := "  "
		if r.IsMain {
			marker = ui.Dim("★ ")
		}
		cursor := "  "
		if i == d.cursor {
			cursor = ui.Cyan("> ")
		}
		line := fmt.Sprintf("%s%s%-14s %s", cursor, marker, truncate(r.Slug, 14), ui.Dim(truncate(r.Branch, 20)))
		out = append(out, line)
	}
	for len(out) < height {
		out = append(out, "")
	}
	return out[:height]
}

// renderEvents keeps the newest lines at the bottom of the pane, the
// same orientation as `logs tail`.
func (d *dashboard) renderEvents(height, leftW int) []string {
	w := max(d.width-leftW-1, 10)
	out := make([]string, 0, height)
	out = append(out, ui.Bold(" EVENTS"))
	for i := len(d.evLines) - 1; i >= 0 && len(out) < height; i-- {
		out = append(out, " "+truncate(d.evLines[i], w))
	}
	for len(out) < height {
		out = append(out, "")
	}
	rev := make([]string, len(out))
	for i, l := range out {
		rev[len(out)-1-i] = l
	}
	return rev
}

func pad(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}

func truncate(s string, w int) string {
	if w <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w <= 1 {
		return string(r[:w])
	}
	return string(r[:w-1]) + "…"
}
