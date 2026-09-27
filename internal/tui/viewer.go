package tui

import (
	"fmt"
	"os"
	"strings"

	bubbleskey "github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/stubbedev/treeman/internal/ui"
)

// viewerKeys is the viewer's keymap: viewport scrolling by default,
// `/` enters search, n/N repeat it, q/Esc quit.
type viewerKeys struct {
	Search bubbleskey.Binding
	Next   bubbleskey.Binding
	Prev   bubbleskey.Binding
	Quit   bubbleskey.Binding
}

func newViewerKeys() viewerKeys {
	return viewerKeys{
		Search: bubbleskey.NewBinding(bubbleskey.WithKeys("/"), bubbleskey.WithHelp("/", "search")),
		Next:   bubbleskey.NewBinding(bubbleskey.WithKeys("n"), bubbleskey.WithHelp("n", "next match")),
		Prev:   bubbleskey.NewBinding(bubbleskey.WithKeys("N"), bubbleskey.WithHelp("N", "prev match")),
		Quit:   bubbleskey.NewBinding(bubbleskey.WithKeys("q", "esc"), bubbleskey.WithHelp("q", "quit")),
	}
}

// viewerModel renders read-only content in a scrollable viewport with
// `/` search. Rendering goes to stderr like the pickers, keeping
// stdout free for piped output.
type viewerModel struct {
	vp     viewport.Model
	input  textinput.Model
	keys   viewerKeys
	title  string
	lines  []string // content split once; search jumps index into this
	search bool     // search prompt is focused
	query  string   // committed search query ("" = none)
	ready  bool
}

// ViewContent renders read-only `content` in the interactive viewer:
// j/k/pgup/pgdn scroll, `/pattern` + Enter jumps to the next match at
// or below the viewport, n/N repeat forward/backward, q/Esc quits.
// Same stderr/stdin conventions as the pickers. ErrNotTTY when either
// end is piped — callers fall back to plain output.
func ViewContent(title, content string) error {
	if !Interactive() {
		return ErrNotTTY
	}
	ui.EnableColorForStderr()
	vp := viewport.New(80, 24)
	vp.SetContent(content)
	input := textinput.New()
	input.Prompt = "/"
	m := &viewerModel{
		vp:    vp,
		input: input,
		keys:  newViewerKeys(),
		title: title,
		lines: strings.Split(content, "\n"),
	}
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithOutput(os.Stderr), tea.WithInput(os.Stdin))
	_, err := p.Run()
	return err
}

// jumpToQuery scrolls the viewport to the first line matching the
// committed query, scanning from `start` in `step` direction and
// landing the match at the top of the view. The current top line is
// always a valid resumption point, so n/N work regardless of how far
// the user has scrolled since.
func (m *viewerModel) jumpToQuery(start, step int) {
	if m.query == "" {
		return
	}
	end := len(m.lines)
	if step < 0 {
		end = -1
	}
	idx := findLineFrom(m.lines, m.query, start, end, step)
	if idx < 0 {
		// No match in the scan direction: wrap once so a long doc is
		// still fully traversable with just n.
		if step > 0 {
			idx = findLineFrom(m.lines, m.query, 0, start+1, step)
		} else {
			idx = findLineFrom(m.lines, m.query, len(m.lines)-1, start-1, step)
		}
	}
	if idx >= 0 {
		m.vp.SetYOffset(idx)
	}
}

// findLineFrom returns the line index of the first line containing
// `query`, scanning from `start` toward `end` (exclusive) by `step`.
// -1 when nothing matches.
func findLineFrom(lines []string, query string, start, end, step int) int {
	for i := start; i != end; i += step {
		if i >= 0 && i < len(lines) && strings.Contains(lines[i], query) {
			return i
		}
	}
	return -1
}

func (m *viewerModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m *viewerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if !m.ready {
			m.vp = viewport.New(msg.Width, msg.Height-1)
			m.vp.SetContent(strings.Join(m.lines, "\n"))
			m.ready = true
		} else {
			m.vp.Width = msg.Width
			m.vp.Height = msg.Height - 1
		}
		return m, nil
	case tea.KeyMsg:
		if m.search {
			switch msg.String() {
			case "enter":
				m.query = strings.TrimSpace(m.input.Value())
				m.search = false
				m.input.Blur()
				m.jumpToQuery(m.vp.YOffset, 1)
				return m, nil
			case "esc":
				m.search = false
				m.input.Blur()
				return m, nil
			}
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		switch {
		case bubbleskey.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case bubbleskey.Matches(msg, m.keys.Search):
			m.search = true
			m.input.Focus()
			m.input.SetValue(m.query)
			return m, textinput.Blink
		case bubbleskey.Matches(msg, m.keys.Next):
			m.jumpToQuery(m.vp.YOffset+1, 1)
			return m, nil
		case bubbleskey.Matches(msg, m.keys.Prev):
			m.jumpToQuery(m.vp.YOffset-1, -1)
			return m, nil
		}
	}
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

func (m *viewerModel) View() string {
	if !m.ready {
		return "loading…"
	}
	var status string
	switch {
	case m.search:
		status = ui.Cyan(m.input.View()) + "  " + ui.Dim("enter: jump · esc: cancel")
	case m.query != "":
		status = fmt.Sprintf("%s %s · %s", ui.Cyan("/"+m.query), ui.Dim("n: next · N: prev"), ui.Dim(m.title))
	default:
		status = ui.Dim(m.title + " · / search · q quit")
	}
	return m.vp.View() + "\n" + status
}
