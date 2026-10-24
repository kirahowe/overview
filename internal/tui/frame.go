package tui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"overview/internal/fleet"
	"overview/internal/tmux"
)

// tmux draws the rule along the top of each of its panes, as tmux.Frame
// sets it up, with a title the view gives each pane of its session: the
// list's own, a session's name and state, a terminal's command. The view
// draws the same rule for what it shows itself, the agents and the
// cockpit's cards, so every panel looks alike. The selected panel's rule
// is lit, in the accent colour.

// paneFrame is what a pane's rule shows: its title, in tmux's styles, and
// whether the pane is the selected panel.
type paneFrame struct {
	title string
	sel   bool
}

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
	return tmux.Colours{Accent: tmuxColour(t.Accent), Border: tmuxColour(t.Border), Text: tmuxColour(t.Text)}
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
		return []titleSeg{{text: "#" + strconv.Itoa(e.issue.ID) + " " + e.issue.Title}}
	case e.kind == projectEntry:
		return []titleSeg{{text: m.name(e.project)}}
	case e.kind == terminalEntry:
		return []titleSeg{{text: "$ " + e.term.Title}}
	}
	return m.stateTitle(e.row)
}

// paneTitle is the title of pane p of the view's session.
func (m model) paneTitle(p tmux.Pane, byTTY map[string]fleet.Row, byID map[string]fleet.Row) []titleSeg {
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
		if r, ok := byID[p.Review]; ok {
			name += " · " + r.Name
		}
		return []titleSeg{{text: name}}
	case p.Attach != "":
		for _, r := range byID {
			if r.ID == p.Attach {
				return m.stateTitle(r)
			}
		}
	case p.ID == m.opened.ID:
		return []titleSeg{{text: "claude"}, {"  starting", pal.Muted}}
	}
	if r, ok := byTTY[p.TTY]; ok && p.TTY != "" {
		return m.stateTitle(r)
	}
	segs := []titleSeg{{text: "$ " + strings.TrimPrefix(p.Command, "-")}}
	if p.Path != "" {
		segs = append(segs, titleSeg{"  " + m.short(p.Path), pal.Muted})
	}
	return segs
}

// frames is what the rule of every pane in the view's session should
// show. The selected panel's pane is lit, while the keyboard is in the
// view: tmux lights the pane the keys are in by itself.
func (m model) frames() map[string]paneFrame {
	byTTY, byID := map[string]fleet.Row{}, map[string]fleet.Row{}
	for _, r := range m.rows {
		if tty := m.ttys[r.PID]; tty != "" && !r.Background() {
			byTTY[tty] = r
		}
		byID[r.SessionID] = r
	}
	selected := ""
	if !m.blurred && !m.cockpit.open {
		if at := m.navAt(); at == projectsPanel {
			selected = m.self
		} else {
			selected = m.paneOfPanel(at)
		}
	}
	out := map[string]paneFrame{}
	for _, p := range m.panes {
		if p.Session == m.session && m.session != "" {
			out[p.ID] = paneFrame{title: tmuxTitle(m.paneTitle(p, byTTY, byID)), sel: p.ID == selected}
		}
	}
	return out
}

// reframe brings tmux's rules in line with the model after every update:
// the titles and the selection that changed, and the list's window
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
		if !ok || was.sel != f.sel {
			sel := ""
			if f.sel {
				sel = "1"
			}
			sets = append(sets, tmux.PaneSet{Pane: id, Name: tmux.SelectOption, Value: sel})
		}
	}
	self, ok := m.pane(m.self)
	frame := ok && !m.zooming && self.Framed == m.cockpit.open
	if len(sets) == 0 && !frame {
		m.framed = next
		return m, nil
	}
	m.framed, m.framing = next, true
	mux, on := m.mux, !m.cockpit.open
	return m, func() tea.Msg {
		err := mux.SetPaneOptions(sets)
		if !frame || err != nil {
			return framedMsg{err: err}
		}
		return framedMsg{self.Window, on, mux.SetFrame(self.ID, on)}
	}
}
