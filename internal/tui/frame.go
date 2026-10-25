package tui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"overview/internal/fleet"
	"overview/internal/tmux"
	"overview/internal/world"
)

// tmux draws the rule along the top of each of its panes, as tmux.Frame
// sets it up, with a title the view gives each pane of its session: the
// list's own, a session's name and state, a terminal's command. The view
// draws the same rule for what it shows itself, the agents and the
// cockpit's cards, so every panel looks alike. The rule of the panel with
// the keys is lit, in the accent colour: tmux lights its active pane's by
// itself, and the view lights the ones it draws.

// paneFrame is what a pane's rule shows: its title, in tmux's styles.
type paneFrame struct{ title string }

// framedMsg answers reframe's Cmd. When it turned the rules of a window on
// or off, window names it and on says which, so the model knows before the
// next listing does.
type framedMsg struct {
	window string
	on     bool
	err    error
}

// tmuxColour is a theme colour as tmux takes it: #rrggbb, or colourN for
// an ANSI index.
func tmuxColour(c lipgloss.Color) string {
	s := string(c)
	switch {
	case len(s) == 4 && s[0] == '#':
		return "#" + strings.Repeat(s[1:2], 2) + strings.Repeat(s[2:3], 2) + strings.Repeat(s[3:4], 2)
	case strings.HasPrefix(s, "#"):
		return s
	}
	return "colour" + s
}

// frameColours are the theme's colours for tmux's rules.
func frameColours(t Theme) tmux.Colours {
	return tmux.Colours{Accent: tmuxColour(t.Accent), Border: tmuxColour(t.Border), Text: tmuxColour(t.Text), Muted: tmuxColour(t.Muted)}
}

// tmuxTitle renders a title for a pane's rule. tmux reads #[...] in it as
// a style, and ## as a #, so every # of the text is doubled.
func tmuxTitle(segs []titleSeg) string {
	var b strings.Builder
	for _, sg := range segs {
		if sg.colour != "" {
			b.WriteString("#[fg=" + tmuxColour(sg.colour) + " nobold]")
		}
		b.WriteString(strings.ReplaceAll(sg.text, "#", "##"))
	}
	return b.String()
}

// stateTitle is a session's name and state, as its pane's rule shows it.
func (m model) stateTitle(r fleet.Row) []titleSeg {
	mark, _, word, age, _ := m.stateMark(r)
	colour := pal.Muted
	switch {
	case r.State == fleet.Blocked:
		colour = pal.Blocked
	case r.Asking():
		colour = pal.Asking
	case r.State == fleet.Working:
		colour, mark = pal.Working, "●" // a spinner would retitle every frame
	}
	state := mark + " " + word
	if age != "" {
		state += " " + age
	}
	return []titleSeg{{text: r.Name}, {"  " + state, colour}}
}

// entryTitle is the title of the holder while it shows entry e.
func (m model) entryTitle(e entry, ok bool) []titleSeg {
	switch {
	case !ok:
		return []titleSeg{{text: "details"}}
	case e.kind == workstreamEntry:
		return []titleSeg{{text: "#" + strconv.Itoa(e.stream.ID) + " " + e.stream.Issue.Title}}
	case e.kind == projectEntry:
		return []titleSeg{{text: e.proj.Name}}
	case e.kind == terminalEntry:
		return []titleSeg{{text: "$ " + e.term.Title}}
	}
	return m.stateTitle(e.sess.Row)
}

// paneTitle is the title of pane p of the view's session, in w, where
// byPane has the session each pane shows, as Session.Pane has it.
func (m model) paneTitle(p tmux.Pane, w *world.World, byPane map[string]*world.Session) []titleSeg {
	switch {
	case p.ID == m.self:
		segs := m.listTitle()
		switch {
		case m.pick.open:
			segs = append(segs, titleSeg{"  · add project", pal.Muted})
		case m.taskPick.open:
			segs = append(segs, titleSeg{"  · start a task", pal.Muted})
		case m.screen == digestScreen:
			segs = append(segs, titleSeg{"  · digests", pal.Muted})
		}
		return segs
	case p.Holder:
		e, _, ok := m.selected(m.ordered())
		return m.entryTitle(e, ok)
	case p.Review != "":
		name := "review"
		if s := w.Sessions[p.Review]; s != nil {
			name += " · " + s.Name
		}
		return []titleSeg{{text: name}}
	case p.ID == m.opened.ID:
		return []titleSeg{{text: "claude"}, {"  starting", pal.Muted}}
	}
	if s := byPane[p.ID]; s != nil {
		return m.stateTitle(s.Row)
	}
	segs := []titleSeg{{text: "$ " + strings.TrimPrefix(p.Command, "-")}}
	if p.Path != "" {
		segs = append(segs, titleSeg{"  " + m.short(p.Path), pal.Muted})
	}
	return segs
}

// frames is what the rule of every pane in the view's session should
// show. Which session a pane shows is the World's Session.Pane.
func (m model) frames() map[string]paneFrame {
	w := m.world()
	byPane := map[string]*world.Session{}
	for _, s := range w.Sessions {
		if s.Pane != "" {
			byPane[s.Pane] = s
		}
	}
	out := map[string]paneFrame{}
	for _, p := range m.in.Panes {
		if p.Session == m.session && m.session != "" {
			out[p.ID] = paneFrame{title: tmuxTitle(m.paneTitle(p, w, byPane))}
		}
	}
	return out
}

// reframe brings tmux's rules in line with the model after every update:
// the titles that changed, and the list's window
// drawing its rules unless the cockpit, which draws its own, has it
// zoomed. One Cmd is out at a time, so an older one cannot land last.
func (m model) reframe() (model, tea.Cmd) {
	if m.mux == nil || m.framing || m.frameStale {
		return m, nil
	}
	want := m.frames()
	var sets []tmux.PaneSet
	next := make(map[string]paneFrame, len(want))
	for id, f := range want {
		next[id] = f
		was, ok := m.framed[id]
		if !ok || was.title != f.title {
			sets = append(sets, tmux.PaneSet{Pane: id, Name: tmux.TitleOption, Value: f.title})
		}
	}
	self, ok := m.pane(m.self)
	frame := ok && !m.zooming && self.Framed == m.zoomWant()
	if len(sets) == 0 && !frame {
		m.framed = next
		return m, nil
	}
	m.framed, m.framing = next, true
	mux, on := m.mux, !m.zoomWant()
	return m, func() tea.Msg {
		err := mux.SetPaneOptions(sets)
		if !frame || err != nil {
			return framedMsg{err: err}
		}
		return framedMsg{self.Window, on, mux.SetFrame(self.ID, on)}
	}
}
