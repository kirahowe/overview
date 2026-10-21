package diffview

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rivo/uniseg"

	"overview/internal/diff"
	"overview/internal/review"
)

// Drawing. Every row an item draws is exactly the pane's width, and a
// frame is exactly the pane's height, so nothing the code contains can
// push the frame out of shape. Only the items on screen are drawn.

// View is the frame as the terminal gets it.
func (m model) View() string {
	if m.w <= 0 || m.h <= 0 {
		return ""
	}
	rows, _ := m.frame()
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = paint(r, m.cfg.Profile)
	}
	return strings.Join(out, "\n")
}

// bodyHeight is how many rows the items get: all but the file header on
// top, the status bar and the editor when it is open.
func (m model) bodyHeight() int {
	return max(1, m.h-2-m.editorHeight())
}

func (m model) editorHeight() int {
	if !m.comp.open {
		return 0
	}
	rows, _ := m.comp.ed.rows(max(1, m.w-2), m.pal.text)
	return min(len(rows), editorRows) + 2
}

// frame is every row of the screen, and for each the item drawn there, -1
// for none.
func (m model) frame() (rows [][]Seg, owners []int) {
	body := m.bodyHeight()
	blank := func() []Seg { return pad(nil, m.w, Style{}) }
	switch {
	case m.help:
		rows = m.helpRows()
	case !m.loaded:
		rows = [][]Seg{blank(), pad([]Seg{plain("  loading the diff…", m.pal.muted)}, m.w, Style{})}
	case m.loadErr != nil && len(m.files) == 0:
		rows = [][]Seg{blank()}
		for _, r := range wrap([]Seg{plain("  "+m.loadErr.Error(), m.pal.warn)}, m.w, Style{}) {
			rows = append(rows, pad(r, m.w, Style{}))
		}
	}
	if rows != nil {
		rows = append([][]Seg{m.headerRow()}, rows...)
		for range rows {
			owners = append(owners, -1)
		}
	} else {
		rows = append(rows, m.headerRow())
		owners = append(owners, -1)
		first := true
		for i := m.top; i < len(m.items) && len(rows) < body+1; i++ {
			drawn := m.draw(i)
			if first {
				// The header row already names the first item's file.
				if m.items[i].kind == fileItem {
					first = false
					continue
				}
				drawn = drawn[min(m.off, len(drawn)):]
				first = false
			}
			for _, r := range drawn {
				if len(rows) == body+1 {
					break
				}
				rows = append(rows, r)
				owners = append(owners, i)
			}
		}
	}
	for len(rows) < body+1 {
		rows = append(rows, blank())
		owners = append(owners, -1)
	}
	rows, owners = rows[:body+1], owners[:body+1]
	if m.comp.open {
		for _, r := range m.editorRows() {
			rows = append(rows, r)
			owners = append(owners, -1)
		}
	}
	rows = append(rows, m.statusRow())
	owners = append(owners, -1)
	// A pane too short for the header, a line, the editor and the status
	// bar shows the bottom of the frame.
	if over := len(rows) - m.h; m.h > 0 && over > 0 {
		rows, owners = rows[over:], owners[over:]
	}
	return rows, owners
}

// headerRow names the file at the top of the screen.
func (m model) headerRow() []Seg {
	if m.top < len(m.items) && !m.help {
		if f := m.items[m.top].file; f >= 0 && f < len(m.files) {
			return m.fileRow(f)
		}
	}
	title := "review"
	if m.cfg.Title != "" {
		title = "review of " + m.cfg.Title
	}
	return pad([]Seg{plain(" "+title, m.pal.file)}, m.w, Style{})
}

// draw is the rows of item i.
func (m model) draw(i int) [][]Seg {
	it := m.items[i]
	switch it.kind {
	case fileItem:
		return [][]Seg{m.fileRow(it.file)}
	case infoItem:
		return [][]Seg{pad([]Seg{plain("   "+info(m.files[it.file]), m.pal.muted)}, m.w, Style{})}
	case hunkItem:
		h := m.files[it.file].Hunks[it.hunk]
		text := fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.OldStart, h.OldCount, h.NewStart, h.NewCount)
		if h.Section != "" {
			text += " " + h.Section
		}
		return [][]Seg{pad([]Seg{plain("   "+truncate(text, max(1, m.w-3)), m.pal.hunk)}, m.w, Style{})}
	case rowItem:
		if m.opts.split {
			return m.splitRow(i, it)
		}
		return m.unifiedRow(i, it)
	case noteItem:
		return m.noteRows(i, it)
	case strayItem:
		return [][]Seg{pad(nil, m.w, Style{}), pad([]Seg{plain(" Notes on files not in this diff", m.pal.file)}, m.w, Style{})}
	case emptyItem:
		text := "  No changes " + m.scope.label() + "."
		return [][]Seg{pad(nil, m.w, Style{}), pad([]Seg{plain(text, m.pal.muted)}, m.w, Style{})}
	}
	return nil
}

func (m model) fileRow(fi int) []Seg {
	f := m.files[fi]
	add, del := counts(f)
	name := f.Path()
	if f.Status == diff.Renamed || f.Status == diff.Copied {
		name = f.OldPath + " → " + f.NewPath
	}
	// Cleaned before it is cut, so a tab is measured as it is drawn.
	name, _ = clean(name)
	tail := fmt.Sprintf("  %s  +%d −%d  %d/%d", status(f), add, del, fi+1, len(m.files))
	room := max(1, m.w-1-len([]rune(tail)))
	return pad([]Seg{plain(" ", m.pal.file), plain(truncate(name, room), m.pal.file), plain(tail, m.pal.muted)}, m.w, Style{})
}

func counts(f diff.File) (add, del int) {
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			switch l.Kind {
			case diff.Added:
				add++
			case diff.Removed:
				del++
			}
		}
	}
	return add, del
}

func status(f diff.File) string {
	switch f.Status {
	case diff.Created:
		return "new"
	case diff.Deleted:
		return "deleted"
	case diff.Renamed:
		return "renamed"
	case diff.Copied:
		return "copied"
	}
	return "changed"
}

func info(f diff.File) string {
	switch {
	case f.Binary:
		return "binary file, not shown"
	case f.Status == diff.Renamed || f.Status == diff.Copied:
		return status(f) + ", no change to its lines"
	case f.OldMode != f.NewMode:
		return "mode " + f.OldMode + " → " + f.NewMode
	}
	return "no lines to show"
}

// numWidth is how wide a file's line numbers are.
func numWidth(f diff.File) int {
	n := 0
	for _, h := range f.Hunks {
		n = max(n, h.OldStart+h.OldCount, h.NewStart+h.NewCount)
	}
	return max(3, len(strconv.Itoa(n)))
}

// markCell is the first column of an item: the cursor, the range being
// chosen, or nothing.
func (m model) markCell(i int) Seg {
	switch {
	case i == m.cur && !m.help:
		return Seg{"▌", m.pal.cursor}
	case m.mark >= 0 && i >= min(m.mark, m.cur) && i <= max(m.mark, m.cur) && m.items[i].kind == rowItem:
		return Seg{"│", m.pal.selected}
	}
	return Seg{" ", Style{}}
}

// lineLook is how a line of a kind is drawn: its number and sign, its
// code, and its changed words.
func (m model) lineLook(k diff.Kind) (num, code, word Style, sign string) {
	switch k {
	case diff.Added:
		return m.pal.added, m.pal.addedLine, m.pal.addedWord, "+"
	case diff.Removed:
		return m.pal.removed, m.pal.removedLine, m.pal.removedWord, "-"
	}
	return m.pal.muted, m.pal.text, m.pal.text, " "
}

// spans is where line li of the hunk differs from its mate, the line it
// replaced or was replaced by, -1 for none.
func spans(h diff.Hunk, li, mate int) []diff.Span {
	if mate < 0 || h.Lines[li].Kind == diff.Context || h.Lines[mate].Kind == diff.Context {
		return nil
	}
	old, new := li, mate
	if h.Lines[li].Kind == diff.Added {
		old, new = mate, li
	}
	if len(h.Lines[old].Text) > maxDrawn || len(h.Lines[new].Text) > maxDrawn {
		return nil
	}
	os, ns, ok := diff.WordDiff(h.Lines[old].Text, h.Lines[new].Text)
	if !ok {
		return nil
	}
	if li == old {
		return os
	}
	return ns
}

// code is line li of hunk hi in file fi as segments: its syntax colours on
// the line's background, and its changed words on a stronger one.
func (m model) code(fi, hi, li, mate int) []Seg {
	h := m.files[fi].Hunks[hi]
	l := h.Lines[li]
	_, base, word, _ := m.lineLook(l.Kind)
	raw, more := drawn(l.Text)
	text, at := clean(raw)
	var tail []Seg
	if more > 0 {
		tail = []Seg{{fmt.Sprintf(" … %d more bytes", more), m.pal.muted}}
	}
	if text == "" {
		return tail
	}
	st := make([]Style, len(text))
	for i := range st {
		st[i] = base
	}
	if hl, ok := m.lit[fi]; ok && hi < len(hl) && li < len(hl[hi]) {
		for _, t := range hl[hi][li] {
			s := m.pal.tokenStyle(t.tt, base)
			for b := at[min(t.start, len(raw))]; b < at[min(t.end, len(raw))]; b++ {
				st[b] = s
			}
		}
	}
	for _, sp := range spans(h, li, mate) {
		for b := at[min(sp.Start, len(raw))]; b < at[min(sp.End, len(raw))]; b++ {
			st[b].Bg = word.Bg
		}
	}
	// A style changes only between graphemes: a cluster split across two
	// segments would be measured as two.
	var segs []Seg
	start := 0
	g := uniseg.NewGraphemes(text)
	for g.Next() {
		if b, _ := g.Positions(); st[b] != st[start] {
			segs = append(segs, Seg{text[start:b], st[start]})
			start = b
		}
	}
	return append(append(segs, Seg{text[start:], st[start]}), tail...)
}

// maxDrawn is how much of one line is drawn. A minified file can have a
// line of megabytes, which would take the whole screen and be cleaned
// and wrapped again on every frame.
const maxDrawn = 4096

// drawn is the part of text that is drawn, cut on a rune boundary, and
// how many bytes are left out.
func drawn(text string) (string, int) {
	if len(text) <= maxDrawn {
		return text, 0
	}
	cut := maxDrawn
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut], len(text) - cut
}

// numCell is a line number right-aligned in w cells, blank for 0.
func numCell(n, w int, st Style) Seg {
	if n == 0 {
		return Seg{strings.Repeat(" ", w), Style{}}
	}
	s := strconv.Itoa(n)
	return Seg{strings.Repeat(" ", max(0, w-len(s))) + s, st}
}

func (m model) unifiedRow(i int, it item) [][]Seg {
	f := m.files[it.file]
	h := f.Hunks[it.hunk]
	li := it.new
	if li < 0 {
		li = it.old
	}
	l := h.Lines[li]
	nw := numWidth(f)
	num, base, _, sign := m.lineLook(l.Kind)
	gutter := 2*nw + 5
	cw := max(1, m.w-gutter)
	code := wrap(m.code(it.file, it.hunk, li, it.mate), cw, base)
	out := make([][]Seg, len(code))
	for r, c := range code {
		var g []Seg
		if r == 0 {
			g = []Seg{m.markCell(i), numCell(l.Old, nw, num), {" ", Style{}}, numCell(l.New, nw, num), {" ", Style{}}, {sign, num}, {" ", base}}
		} else {
			g = []Seg{m.markCell(i), {strings.Repeat(" ", gutter-2), Style{}}, {" ", base}}
		}
		out[r] = pad(append(pad(g, min(gutter, m.w), Style{}), pad(c, cw, base)...), m.w, Style{})
	}
	return out
}

// half is one side of a split row, w cells wide, its rows filled to w.
func (m model) half(fi, hi, li, mate, nw, w int, lead Seg) [][]Seg {
	g0 := []Seg{lead}
	gutter := nw + 3 + len(lead.Text) // the mark's cell, if any, is one byte
	if lead.Text == "" {
		gutter = nw + 3
		g0 = nil
	}
	cw := max(1, w-gutter)
	if li < 0 {
		return [][]Seg{pad(append(g0, Seg{"", Style{}}), w, Style{})}
	}
	l := m.files[fi].Hunks[hi].Lines[li]
	num, base, _, sign := m.lineLook(l.Kind)
	n := l.New
	if l.Kind == diff.Removed {
		n = l.Old
	}
	code := wrap(m.code(fi, hi, li, mate), cw, base)
	out := make([][]Seg, len(code))
	for r, c := range code {
		var g []Seg
		if r == 0 {
			g = append(append([]Seg{}, g0...), numCell(n, nw, num), Seg{" ", Style{}}, Seg{sign, num}, Seg{" ", base})
		} else {
			g = append(append([]Seg{}, g0...), Seg{strings.Repeat(" ", nw+2), Style{}}, Seg{" ", base})
		}
		out[r] = pad(append(pad(g, min(gutter, w), Style{}), pad(c, cw, base)...), w, Style{})
	}
	return out
}

func (m model) splitRow(i int, it item) [][]Seg {
	f := m.files[it.file]
	nw := numWidth(f)
	lw := max(1, (m.w-1)/2)
	rw := max(1, m.w-1-lw)
	mark := m.markCell(i)
	// The mark cell is drawn on the left half, so the right half's lead
	// is empty.
	left := m.half(it.file, it.hunk, it.old, it.new, nw, lw, mark)
	right := m.half(it.file, it.hunk, it.new, it.old, nw, rw, Seg{})
	if it.old >= 0 && it.new >= 0 && it.old == it.new {
		// A context line is the same on both sides.
		right = m.half(it.file, it.hunk, it.new, -1, nw, rw, Seg{})
	}
	n := max(len(left), len(right))
	out := make([][]Seg, n)
	for r := range n {
		var l, rt []Seg
		if r < len(left) {
			l = left[r]
		} else {
			l = pad(nil, lw, Style{})
		}
		if r < len(right) {
			rt = right[r]
		} else {
			rt = pad(nil, rw, Style{})
		}
		row := append(append(append([]Seg{}, l...), Seg{"│", m.pal.border}), rt...)
		out[r] = pad(row, m.w, Style{})
	}
	return out
}

// noteRows draws a note and its thread as a box under its line.
func (m model) noteRows(i int, it item) [][]Seg {
	n, ok := m.rev.Find(it.note)
	if !ok {
		return [][]Seg{pad(nil, m.w, Style{})}
	}
	bw := max(1, m.w-2)
	lead := []Seg{m.markCell(i), {" ", Style{}}}
	edge := m.pal.border
	if i == m.cur {
		edge = m.pal.accent
	}
	var out [][]Seg
	line := func(segs ...Seg) {
		out = append(out, pad(append(append([]Seg{}, lead...), pad(segs, bw, Style{})...), m.w, Style{}))
	}
	body := func(prefix string, text string, st Style) {
		for _, para := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			for _, r := range wrap([]Seg{plain(para, st)}, max(1, bw-len([]rune(prefix))-2), Style{}) {
				line(append([]Seg{{"│ ", edge}, {prefix, Style{}}}, r...)...)
			}
		}
	}
	head := []Seg{{"╭─ ", edge}}
	if it.outdated {
		where := fmt.Sprintf("%s:%d", n.Anchor.Path, n.Anchor.Start)
		if n.Anchor.End != n.Anchor.Start {
			where += fmt.Sprintf("-%d", n.Anchor.End)
		}
		head = append(head, plain(where+" ", m.pal.muted))
	}
	head = append(head, m.who(n.Thread[0].Author), Seg{" · " + n.Thread[0].At.Local().Format("15:04"), m.pal.muted})
	head = append(head, m.noteState(n, it.outdated)...)
	line(head...)
	if it.outdated {
		line(Seg{"│ ", edge}, plain("on lines that have since changed:", m.pal.resolved))
		for k, l := range n.Anchor.Lines {
			if k == 3 {
				line(Seg{"│ ", edge}, Seg{"  …", m.pal.muted})
				break
			}
			line(Seg{"│ ", edge}, plain("  "+l, m.pal.muted))
		}
	}
	textStyle := m.pal.text
	if n.Resolved {
		textStyle = m.pal.resolved
	}
	body("", n.Thread[0].Body, textStyle)
	for _, e := range n.Thread[1:] {
		tag := []Seg{{"│ ", edge}, {"↳ ", m.pal.muted}, m.who(e.Author), {" · " + e.At.Local().Format("15:04"), m.pal.muted}}
		if e.Author == review.User && !e.Sent {
			tag = append(tag, Seg{" · unsent", m.pal.unsent})
		}
		line(tag...)
		body("  ", e.Body, textStyle)
	}
	foot := []Seg{{"╰─", edge}}
	if i == m.cur {
		foot = append(foot, Seg{" enter reply · x " + map[bool]string{true: "reopen", false: "resolve"}[n.Resolved], m.pal.muted})
		if m.editable(n) != "" {
			foot = append(foot, Seg{" · e edit", m.pal.muted})
		}
	}
	line(foot...)
	return out
}

func (m model) who(a review.Author) Seg {
	if a == review.Agent {
		return Seg{"agent", m.pal.agent}
	}
	return Seg{"you", m.pal.user}
}

// noteState is the tag after a note's head: where it stands.
func (m model) noteState(n review.Note, outdated bool) []Seg {
	var out []Seg
	add := func(text string, st Style) { out = append(out, Seg{" · " + text, st}) }
	switch {
	case n.Resolved:
		add("resolved", m.pal.resolved)
	case n.Addressed:
		add("agent says addressed", m.pal.waiting)
	case n.Waiting():
		add("reply waiting", m.pal.waiting)
	}
	if n.Thread[0].Author == review.User && !n.Thread[0].Sent && !n.Resolved {
		add("unsent", m.pal.unsent)
	}
	if outdated {
		add("outdated", m.pal.muted)
	}
	return out
}

func (m model) editorRows() [][]Seg {
	w := max(1, m.w-2)
	rows, cur := m.comp.ed.rows(w, m.pal.text)
	first := max(0, min(cur-editorRows+1, len(rows)-editorRows))
	if first < 0 {
		first = 0
	}
	shown := rows[first:min(len(rows), first+editorRows)]
	title := "new note"
	switch {
	case m.comp.reply != "":
		title = "reply to " + m.comp.reply
	case m.comp.edit != "":
		title = "editing " + m.comp.edit
	default:
		a := m.comp.anchor
		title += fmt.Sprintf(" on %s, %s line %d", a.Path, a.Side, a.Start)
		if a.End != a.Start {
			title += fmt.Sprintf("-%d", a.End)
		}
	}
	hint := "enter save · ctrl+j new line · esc cancel"
	if m.comp.discard {
		hint = "esc again to throw this away"
	}
	out := [][]Seg{pad([]Seg{Seg{"─ ", m.pal.accent}, plain(truncate(title, max(1, m.w-3)), m.pal.accent)}, m.w, Style{})}
	for _, r := range shown {
		out = append(out, pad(append([]Seg{{"  ", Style{}}}, pad(r, w, m.pal.text)...), m.w, Style{}))
	}
	out = append(out, pad([]Seg{plain("  "+hint, m.pal.muted)}, m.w, Style{}))
	return out
}

func (m model) statusRow() []Seg {
	st, key := m.pal.status, m.pal.statusKey
	var left string
	switch {
	case m.base != "":
		left = " " + m.scope.label() + " · " + short(m.base)
	default:
		left = " " + m.scope.label()
	}
	if m.opts.split {
		left += " · split"
	} else {
		left += " · unified"
	}
	sum := review.Summarise(m.rev)
	var right []string
	if sum.Open > 0 {
		right = append(right, fmt.Sprintf("%d open", sum.Open))
	}
	if sum.Unsent > 0 {
		right = append(right, fmt.Sprintf("%d to send", sum.Unsent))
	}
	if sum.Waiting > 0 {
		right = append(right, fmt.Sprintf("%d waiting", sum.Waiting))
	}
	r := strings.Join(right, " · ") + "  ? keys "
	// A note takes the place of the scope, which it matters more than.
	leftSt := st
	if m.note != "" {
		left = " " + plain(m.note, st).Text
		leftSt.Bold = m.warn
	}
	rw := uniseg.StringWidth(r)
	lw := max(0, m.w-rw)
	leftSegs := pad([]Seg{{truncate(left, lw), leftSt}}, lw, st)
	return pad(append(leftSegs, Seg{truncate(r, m.w-lw), key}), m.w, st)
}

// short is a commit id cut to twelve characters.
func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

var helpText = []string{
	"Moving",
	"  j k ↓ ↑        line by line          ctrl+d ctrl+u   half a page",
	"  g G            first and last        ] [             next and previous file",
	"  } {            next and previous hunk  n N           next and previous note",
	"",
	"Notes",
	"  enter c        write a note on the line, or reply to the note",
	"  v              start a range, then move and press enter",
	"  e              edit a note or reply you have not sent",
	"  x              resolve or reopen a note (only you can)",
	"  s              send every unsent note and reply to the session",
	"  h              show or hide resolved notes",
	"",
	"View",
	"  m              split or unified      b               since the session started, or uncommitted",
	"  R              reload the diff       z               taller, full screen, back",
	"  q              close the review      ? esc           close this",
}

func (m model) helpRows() [][]Seg {
	var out [][]Seg
	for _, l := range helpText {
		st := m.pal.text
		if l != "" && l[0] != ' ' {
			st = m.pal.accent
		}
		out = append(out, pad([]Seg{plain(" "+truncate(l, max(1, m.w-1)), st)}, m.w, Style{}))
	}
	return out
}
