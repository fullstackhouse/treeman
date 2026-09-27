package ui

import (
	"fmt"
	"io"
	"strings"
)

// Table renders a tabular view to Out. ANSI-aware width math so
// colored cells don't break alignment.
type Table struct {
	headers  []string
	rows     [][]string
	gap      string
	maxWidth int // 0 = no fitting (natural widths, may wrap)
}

// NewTable builds a table with the given column headers. The first
// row of the rendered output is the bolded header row.
func NewTable(headers ...string) *Table {
	return &Table{headers: headers, gap: "  "}
}

// Row appends one row. Pass cells as styled strings (Green/Cyan/etc.)
// — the renderer strips ANSI before measuring width.
func (t *Table) Row(cells ...string) {
	t.rows = append(t.rows, cells)
}

// SetWidth caps the table's total rendered width: when the natural
// column widths would exceed w, the LAST column (the one carrying the
// unbounded payload — PATH, COMMAND) shrinks and its cells are
// truncated with an ellipsis instead of wrapping on narrow terminals.
// Returns the table for chaining.
func (t *Table) SetWidth(w int) *Table {
	t.maxWidth = w
	return t
}

// Render writes the table to w (defaults to ui.Out when nil).
func (t *Table) Render(w io.Writer) {
	if w == nil {
		w = Out
	}
	if len(t.rows) == 0 && len(t.headers) == 0 {
		return
	}
	widths := make([]int, len(t.headers))
	for i, h := range t.headers {
		widths[i] = Width(h)
	}
	for _, row := range t.rows {
		for i, c := range row {
			if i >= len(widths) {
				continue
			}
			if w := Width(c); w > widths[i] {
				widths[i] = w
			}
		}
	}
	widths, truncCol := t.fitWidths(widths)
	// Header
	for i, h := range t.headers {
		writeCell(w, Bold(h), widths[i])
		if i < len(t.headers)-1 {
			_, _ = fmt.Fprint(w, t.gap)
		}
	}
	_, _ = fmt.Fprintln(w)
	// Separator (dim)
	for i, wi := range widths {
		_, _ = fmt.Fprint(w, Dim(strings.Repeat("─", wi)))
		if i < len(widths)-1 {
			_, _ = fmt.Fprint(w, t.gap)
		}
	}
	_, _ = fmt.Fprintln(w)
	// Body
	for _, row := range t.rows {
		for i, cell := range row {
			if i >= len(widths) {
				continue
			}
			if i == truncCol {
				cell = Truncate(cell, widths[i])
			}
			writeCell(w, cell, widths[i])
			if i < len(row)-1 && i < len(widths)-1 {
				_, _ = fmt.Fprint(w, t.gap)
			}
		}
		_, _ = fmt.Fprintln(w)
	}
}

// fitWidths applies the SetWidth budget: shrink the last column to
// the leftover room and mark it truncatable. Other columns keep
// natural widths — their content is bounded (ids, phases, durations);
// the last one carries the paths/commands that actually overflow.
func (t *Table) fitWidths(widths []int) ([]int, int) {
	truncCol := -1
	if t.maxWidth > 0 && len(widths) > 1 {
		gaps := len(t.gap) * (len(widths) - 1)
		fixed := 0
		for _, wi := range widths[:len(widths)-1] {
			fixed += wi
		}
		last := t.maxWidth - gaps - fixed
		last = max(last, 4) // always room for the ellipsis marker
		if last < widths[len(widths)-1] {
			widths[len(widths)-1] = last
			truncCol = len(widths) - 1
		}
	}
	return widths, truncCol
}

// writeCell prints cell followed by enough trailing spaces to fill
// the column width. Padding is computed against the ANSI-stripped
// width so colored cells don't drift.
func writeCell(w io.Writer, cell string, width int) {
	_, _ = fmt.Fprint(w, cell)
	pad := width - Width(cell)
	if pad > 0 {
		_, _ = fmt.Fprint(w, strings.Repeat(" ", pad))
	}
}
