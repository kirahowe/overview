package diffview

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"overview/internal/diff"
	"overview/internal/review"
)

// key handles a key: in the editor while a note is being written, else as
// a command on the diff.
func (m model) key(k tea.KeyMsg) (model, tea.Cmd) {
	if m.comp.open {
		return m.editKey(k)
	}
	if m.help {
		switch k.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}
		m.help = false
		return m, nil
	}
	switch k.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.help = true
	case "esc":
		m.mark, m.note = -1, ""
	case "j", "down":
		m = m.step(1)
	case "k", "up":
		m = m.step(-1)
	case "ctrl+d", "pgdown", " ":
		m = m.page(1)
	case "ctrl+u", "pgup":
		m = m.page(-1)
	case "g", "home":
		m = m.moveTo(settle(m.items, 0))
	case "G", "end":
		m = m.moveTo(nextSelectable(m.items, len(m.items)-1, -1))
	case "]":
		m = m.jump(1, func(it item) bool { return it.kind == fileItem })
	case "[":
		m = m.jump(-1, func(it item) bool { return it.kind == fileItem })
	case "}":
		m = m.jump(1, func(it item) bool { return it.kind == hunkItem })
	case "{":
		m = m.jump(-1, func(it item) bool { return it.kind == hunkItem })
	case "n":
		m = m.toNote(1)
	case "N":
		m = m.toNote(-1)
	case "enter", "c":
		return m.compose()
	case "v":
		if m.cur < len(m.items) && m.items[m.cur].kind == rowItem {
			if m.mark >= 0 {
				m.mark = -1
			} else {
				m.mark = m.cur
				m = m.say("range started: move, then enter")
			}
		}
	case "e":
		return m.startEdit()
	case "x":
		return m.resolve()
	case "s":
		return m.send()
	case "h":
		m.opts.showResolved = !m.opts.showResolved
		m = m.relayout()
		if m.opts.showResolved {
			return m.say("showing resolved notes"), nil
		}
		return m.say("hiding resolved notes"), nil
	case "m":
		m.opts.split, m.chosen = !m.opts.split, true
		return m.relayout(), nil
	case "b":
		if m.cfg.Started.IsZero() {
			return m.say("this review has no session start to diff from"), nil
		}
		m.scope = 1 - m.scope
		m = m.say("showing changes " + m.scope.label())
		return m.load()
	case "R":
		m = m.say("reloading")
		return m.load()
	case "z":
		return m.resize()
	}
	return m.scrollToCursor(), nil
}

// moveTo puts the cursor on item i and scrolls to it.
func (m model) moveTo(i int) model {
	if len(m.items) == 0 {
		return m
	}
	m.cur = max(0, min(i, len(m.items)-1))
	return m.scrollToCursor()
}

// step moves the cursor to the next selectable item in direction dir.
func (m model) step(dir int) model {
	j := m.cur + dir
	if j < 0 || j >= len(m.items) {
		return m
	}
	if k := nextSelectable(m.items, j, dir); m.items[k].selectable() {
		return m.moveTo(k)
	}
	return m
}

// jump moves to the first selectable item after the next item that is,
// in direction dir.
func (m model) jump(dir int, is func(item) bool) model {
	for j := m.cur + dir; j >= 0 && j < len(m.items); j += dir {
		if !is(m.items[j]) {
			continue
		}
		if dir < 0 && j < m.cur {
			// Going back from inside a file first goes to its own start.
			if k := nextSelectable(m.items, j, 1); k >= m.cur {
				continue
			}
		}
		if k := nextSelectable(m.items, j, 1); m.items[k].selectable() {
			return m.moveTo(k)
		}
	}
	return m
}

func (m model) toNote(dir int) model {
	for j := m.cur + dir; j >= 0 && j < len(m.items); j += dir {
		if m.items[j].kind == noteItem {
			return m.moveTo(j)
		}
	}
	return m.say("no more notes that way")
}

// page moves the cursor about half a screen.
func (m model) page(dir int) model {
	want := max(1, m.bodyHeight()/2)
	j, moved := m.cur, 0
	for moved < want {
		k := j + dir
		if k < 0 || k >= len(m.items) {
			break
		}
		moved += len(m.draw(k))
		j = k
	}
	if j == m.cur {
		return m
	}
	if !m.items[j].selectable() {
		j = nextSelectable(m.items, j, dir)
		if !m.items[j].selectable() {
			j = nextSelectable(m.items, j, -dir)
		}
	}
	return m.moveTo(j)
}

// scrollToCursor moves the top of the screen so the cursor's item is on
// it, as little as it can.
func (m model) scrollToCursor() model {
	if len(m.items) == 0 || m.w <= 0 {
		return m
	}
	m.top = max(0, min(m.top, len(m.items)-1))
	if m.cur < m.top || (m.cur == m.top && m.off > 0) {
		m.top, m.off = m.cur, 0
		return m.keepHeader()
	}
	// Walk back from the cursor a screen's worth of rows. Reaching the
	// top means the cursor is on screen already; otherwise the screen
	// starts where the walk stopped, so a jump costs a screen of drawing
	// however far it goes.
	body := m.bodyHeight()
	t, used := m.cur, m.height(m.cur)
	for t > m.top && used+m.height(t-1) <= body {
		t--
		used += m.height(t)
	}
	if t > m.top || used-m.off > body {
		m.top, m.off = t, 0
	}
	return m.keepHeader()
}

// keepHeader shows a file's own header item at the top rather than under
// the header row that repeats it, when the screen starts just after it.
func (m model) keepHeader() model {
	if m.top > 0 && m.off == 0 && m.items[m.top-1].kind == fileItem && m.top-1 < m.cur {
		m.top--
	}
	return m
}

// height is how many rows item i takes. A file header at the top of the
// screen takes none, since the header row shows it.
func (m model) height(i int) int {
	if i == m.top && m.items[i].kind == fileItem {
		return 0
	}
	return len(m.draw(i))
}

// scroll moves the screen n rows without moving the cursor, unless it
// would leave the screen.
func (m model) scroll(n int) model {
	for ; n > 0 && m.top < len(m.items)-1; n-- {
		if m.off+1 < m.height(m.top) {
			m.off++
		} else {
			m.top, m.off = m.top+1, 0
		}
	}
	for ; n < 0 && (m.top > 0 || m.off > 0); n++ {
		if m.off > 0 {
			m.off--
		} else {
			m.top--
			m.off = max(0, m.height(m.top)-1)
		}
	}
	_, owners := m.frame()
	seen := false
	first := -1
	for _, o := range owners {
		if o == m.cur {
			seen = true
		}
		if first < 0 && o >= 0 && m.items[o].selectable() {
			first = o
		}
	}
	if !seen && first >= 0 {
		m.cur = first
	}
	return m
}

func (m model) mouse(e tea.MouseMsg) (model, tea.Cmd) {
	if m.comp.open || m.help {
		return m, nil
	}
	switch {
	case e.Button == tea.MouseButtonWheelDown:
		return m.scroll(3), nil
	case e.Button == tea.MouseButtonWheelUp:
		return m.scroll(-3), nil
	case e.Action == tea.MouseActionPress && e.Button == tea.MouseButtonLeft:
		_, owners := m.frame()
		if e.Y >= 0 && e.Y < len(owners) {
			if o := owners[e.Y]; o >= 0 && m.items[o].selectable() {
				m.cur = o
			}
		}
	}
	return m, nil
}

// anchor is the lines a new note is on: the cursor's row, or the range
// from the mark to it, within the cursor's hunk. The new side is used when
// any of the rows has a line on it.
func (m model) anchor() (review.Anchor, error) {
	if m.cur >= len(m.items) || m.items[m.cur].kind != rowItem {
		return review.Anchor{}, errors.New("move to a line to write a note")
	}
	it := m.items[m.cur]
	lo, hi := m.cur, m.cur
	if m.mark >= 0 {
		lo, hi = min(m.mark, m.cur), max(m.mark, m.cur)
	}
	f := m.files[it.file]
	h := f.Hunks[it.hunk]
	var news, olds []int
	for i := lo; i <= hi; i++ {
		r := m.items[i]
		if r.kind != rowItem || r.file != it.file || r.hunk != it.hunk {
			continue
		}
		if r.new >= 0 {
			news = append(news, h.Lines[r.new].New)
		}
		if r.old >= 0 && h.Lines[r.old].Kind != diff.Context {
			olds = append(olds, h.Lines[r.old].Old)
		}
	}
	side, nums := review.New, news
	if len(news) == 0 {
		side, nums = review.Old, olds
	}
	if len(nums) == 0 {
		return review.Anchor{}, errors.New("no line here to write a note on")
	}
	a := review.Anchor{Path: f.Path(), Side: side, Start: nums[0], End: nums[0]}
	for _, n := range nums {
		a.Start, a.End = min(a.Start, n), max(a.End, n)
	}
	if side == review.Old {
		a.Path = f.OldPath
	}
	text := map[int]string{}
	for _, l := range h.Lines {
		switch {
		case side == review.New && l.Kind != diff.Removed:
			text[l.New] = l.Text
		case side == review.Old && l.Kind != diff.Added:
			text[l.Old] = l.Text
		}
	}
	for n := a.Start; n <= a.End; n++ {
		t, ok := text[n]
		if !ok {
			return review.Anchor{}, fmt.Errorf("line %d is not in the diff", n)
		}
		a.Lines = append(a.Lines, t)
	}
	return a, nil
}

// compose opens the editor: for a reply on a note, else for a new note on
// the cursor's line or range.
func (m model) compose() (model, tea.Cmd) {
	if m.cur < len(m.items) && m.items[m.cur].kind == noteItem {
		m.comp = composing{open: true, reply: m.items[m.cur].note}
		return m, nil
	}
	a, err := m.anchor()
	if err != nil {
		return m.complain(err), nil
	}
	m.comp = composing{open: true, anchor: a}
	return m, nil
}

// editable is the id of the entry of n that e edits: the user's latest
// unsent entry, "" for none.
func (m model) editable(n review.Note) string {
	for i := len(n.Thread) - 1; i >= 0; i-- {
		if e := n.Thread[i]; e.Author == review.User && !e.Sent {
			return e.ID
		}
	}
	return ""
}

func (m model) noteAtCursor() (review.Note, bool) {
	if m.cur >= len(m.items) || m.items[m.cur].kind != noteItem {
		return review.Note{}, false
	}
	return m.rev.Find(m.items[m.cur].note)
}

func (m model) startEdit() (model, tea.Cmd) {
	n, ok := m.noteAtCursor()
	if !ok {
		return m.say("move to a note to edit it"), nil
	}
	id := m.editable(n)
	if id == "" {
		return m.say("only what you have not sent can be edited"), nil
	}
	for _, e := range n.Thread {
		if e.ID == id {
			ed := editor{}.insert(e.Body)
			m.comp = composing{open: true, edit: id, ed: ed}
		}
	}
	return m, nil
}

func (m model) resolve() (model, tea.Cmd) {
	n, ok := m.noteAtCursor()
	if !ok {
		return m.say("move to a note to resolve it"), nil
	}
	op, done := review.OpResolve, "resolved "+n.ID
	if n.Resolved {
		op, done = review.OpReopen, "reopened "+n.ID
	}
	ev := review.Event{Op: op, Note: n.ID, Author: review.User}
	return m.write(done, func(string) []review.Event { return []review.Event{ev} })
}

func (m model) editKey(k tea.KeyMsg) (model, tea.Cmd) {
	c := m.comp
	if k.String() != "esc" {
		c.discard = false
	}
	switch k.String() {
	case "esc":
		if strings.TrimSpace(c.ed.text) != "" && !c.discard {
			c.discard = true
			m.comp = c
			return m, nil
		}
		m.comp = composing{}
		return m.scrollToCursor(), nil
	case "enter":
		if k.Paste {
			c.ed = c.ed.insert("\n")
			break
		}
		return m.save()
	case "ctrl+j", "alt+enter":
		c.ed = c.ed.insert("\n")
	case "backspace", "ctrl+h":
		c.ed = c.ed.backspace()
	case "delete", "ctrl+d":
		c.ed = c.ed.del()
	case "left", "ctrl+b":
		c.ed = c.ed.left()
	case "right", "ctrl+f":
		c.ed = c.ed.right()
	case "up":
		c.ed = c.ed.up()
	case "down":
		c.ed = c.ed.down()
	case "home", "ctrl+a":
		c.ed = c.ed.home()
	case "end", "ctrl+e":
		c.ed = c.ed.end()
	case "ctrl+w", "alt+backspace":
		c.ed = c.ed.deleteWord()
	case "ctrl+u":
		c.ed = c.ed.killLine()
	case "ctrl+c":
		// As esc does: a note is never thrown away by one key.
		return m.editKey(tea.KeyMsg{Type: tea.KeyEsc})
	default:
		if k.Type == tea.KeyRunes || k.Type == tea.KeySpace {
			c.ed = c.ed.insert(string(k.Runes))
		}
	}
	m.comp = c
	return m, nil
}

// save writes the note, reply or edit being composed.
func (m model) save() (model, tea.Cmd) {
	c := m.comp
	body := strings.TrimSpace(c.ed.text)
	if body == "" {
		m.comp = composing{}
		return m.say("nothing written"), nil
	}
	m.comp, m.mark = composing{}, -1
	switch {
	case c.edit != "":
		ev := review.Event{Op: review.OpEdit, ID: c.edit, Author: review.User, Body: body}
		return m.write("edited", func(string) []review.Event { return []review.Event{ev} })
	case c.reply != "":
		note := c.reply
		return m.write("replied, s sends it", func(id string) []review.Event {
			return []review.Event{{Op: review.OpReply, ID: id, Note: note, Author: review.User, Body: body}}
		})
	}
	a := c.anchor
	return m.write("noted, s sends it", func(id string) []review.Event {
		return []review.Event{{Op: review.OpNote, ID: id, Author: review.User, Body: body, Anchor: &a}}
	})
}

// write appends the events build makes, given a fresh id, stamped now,
// and says done.
func (m model) write(done string, build func(id string) []review.Event) (model, tea.Cmd) {
	d := m.deps
	taken := map[string]bool{}
	for _, e := range m.events {
		taken[e.ID] = true
	}
	m.writing++
	return m, func() tea.Msg {
		id, err := review.NewID(func(s string) bool { return taken[s] }, d.rnd)
		if err != nil {
			return doneMsg{err: err, write: true}
		}
		es := build(id)
		now := d.now().UTC()
		for i := range es {
			es[i].At = now
		}
		if err := d.store.Append(es...); err != nil {
			return doneMsg{err: err, write: true}
		}
		return doneMsg{note: done, wrote: true, write: true}
	}
}

// send pastes every unsent note and reply into the session's prompt, when
// it is there and empty, and marks them sent. With no session pane the
// message goes on the clipboard instead.
func (m model) send() (model, tea.Cmd) {
	switch {
	case m.sending:
		return m.say("already sending"), nil
	case m.writing > 0:
		return m.say("still saving, press s again"), nil
	}
	items := review.Pending(m.rev)
	if len(items) == 0 {
		return m.say("nothing to send"), nil
	}
	text := review.Message(items, m.cfg.Store, m.cfg.Exe)
	var ids []string
	for _, it := range items {
		for _, e := range it.Entries {
			ids = append(ids, e.ID)
		}
	}
	what := plural(len(items), "note")
	d, target, title := m.deps, m.cfg.Target, m.cfg.Title
	sent := func() error {
		return d.store.Append(review.Event{Op: review.OpSent, Author: review.User, IDs: ids, At: d.now().UTC()})
	}
	m.sending = true
	if target == "" || d.mux == nil {
		return m.say("copying…"), func() tea.Msg {
			if err := d.copy(text); err != nil {
				return doneMsg{send: true, err: fmt.Errorf("could not copy the notes: %w", err)}
			}
			return doneMsg{send: true, note: "copied " + what + " to paste into the session", err: sent(), wrote: true}
		}
	}
	return m.say("sending…"), func() tea.Msg {
		command, screen, err := d.mux.Live(target)
		if err != nil {
			return doneMsg{send: true, err: fmt.Errorf("could not read the session's pane: %w", err)}
		}
		if shell(command) {
			return doneMsg{send: true, err: errors.New("not sent: the session's pane runs " + command + ", not Claude Code")}
		}
		if err := review.Ready(screen); err != nil {
			return doneMsg{send: true, err: fmt.Errorf("not sent: %w", err)}
		}
		buffer := "overview-review-" + strconv.FormatInt(d.now().UnixNano(), 36)
		if err := d.mux.Paste(target, buffer, text); err != nil {
			return doneMsg{send: true, err: fmt.Errorf("could not send: %w", err)}
		}
		if err := sent(); err != nil {
			return doneMsg{send: true, err: fmt.Errorf("sent, but could not mark them sent: %w", err), wrote: true}
		}
		to := "the session"
		if title != "" {
			to = title
		}
		return doneMsg{send: true, note: "sent " + what + " to " + to, wrote: true}
	}
}

// shell says whether a pane's foreground command is a shell, which a
// session's pane runs once Claude Code has exited, whatever it last drew.
func shell(command string) bool {
	switch strings.TrimPrefix(command, "-") {
	case "sh", "bash", "zsh", "fish", "dash", "ksh", "tcsh", "csh", "nu", "elvish", "xonsh", "pwsh":
		return true
	}
	return false
}

func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + one + "s"
}

// sizes are the pane's heights, peek and tall; the third size is zoomed.
var sizes = []string{"45%", "80%"}

// resize gives the pane its next size: taller, then the whole window, then
// back.
func (m model) resize() (model, tea.Cmd) {
	d, self := m.deps, m.cfg.Self
	if d.mux == nil || self == "" {
		return m.say("the review is not in a pane of overview's tmux"), nil
	}
	from := m.size
	m.size = (m.size + 1) % 3
	return m, func() tea.Msg {
		var err error
		switch from {
		case 0:
			err = d.mux.Resize(self, sizes[1])
		case 1:
			err = d.mux.Zoom(self)
		case 2:
			if err = d.mux.Zoom(self); err == nil {
				err = d.mux.Resize(self, sizes[0])
			}
		}
		return doneMsg{err: err}
	}
}

// highlightVisible asks for the syntax colours of the files on screen
// that have none yet.
func (m model) highlightVisible() (model, tea.Cmd) {
	if m.w <= 0 || len(m.items) == 0 || m.cfg.Theme.Syntax == "" {
		return m, nil
	}
	var cmds []tea.Cmd
	_, owners := m.frame()
	for _, o := range owners {
		if o < 0 {
			continue
		}
		fi := m.items[o].file
		if fi < 0 || m.litting[fi] {
			continue
		}
		m.litting = with(m.litting, fi, true)
		gen, f := m.gen, m.files[fi]
		cmds = append(cmds, func() tea.Msg { return litMsg{gen, fi, highlight(f)} })
	}
	return m, tea.Batch(cmds...)
}
