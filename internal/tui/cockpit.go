package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"strings"

	"overview/internal/issues"
)

// The cockpit is the view's own pane zoomed to fill its window: the list
// down the left, as it always is, and cards about the selected entry's
// workstream or project beside it. tmux keeps every other pane of the
// window as it was under the zoom, so the session there comes back as it
// was when the cockpit closes. While it is open the list stays in its
// window, and the cards follow the cursor.

// cockpitList is the list's width in the cockpit when it has none of its
// own to keep, and cockpitCardsMin the narrowest the cards go beside it.
// A window too narrow for both shows only the cards.
const (
	cockpitList     = 40
	cockpitCardsMin = 50
)

// cockpit is the cockpit's state. issues says the issues card has the
// keyboard, and pick is its cursor there.
type cockpit struct {
	open   bool
	issues bool
	pick   int
}

// zoomedMsg answers zoomCmd.
type zoomedMsg struct{ err error }

// zoomCmd zooms the list's pane, or ends the zoom, unless the listing
// shows it so already: tmux's zoom is a toggle, and the user may have
// ended it.
func zoomCmd(mux multiplexer, self string, on bool) tea.Cmd {
	return func() tea.Msg {
		panes, err := mux.Panes()
		if err != nil {
			return zoomedMsg{err}
		}
		for _, p := range panes {
			if p.ID == self && p.Zoomed != on {
				return zoomedMsg{mux.Zoom(self)}
			}
		}
		return zoomedMsg{}
	}
}

// openCockpit opens the cockpit, zooming the list inside tmux. The zoom
// counts as a spawn, so no listing from before it lands is taken to say
// the zoom has gone.
func (m model) openCockpit() (model, tea.Cmd) {
	if m.cockpit.open {
		return m, nil
	}
	m.cockpit = cockpit{open: true}
	if m.mux == nil {
		return m, nil
	}
	m.spawns++
	m.zooming = true
	return m, zoomCmd(m.mux, m.self, true)
}

// closeCockpit closes the cockpit and ends the zoom. The list goes back to
// following the cursor once the zoom has gone.
func (m model) closeCockpit() (model, tea.Cmd) {
	if !m.cockpit.open {
		return m, nil
	}
	m.cockpit = cockpit{}
	if m.mux == nil {
		return m, nil
	}
	m.spawns++
	m.zooming = true
	return m, zoomCmd(m.mux, m.self, false)
}

// zoomLost closes the cockpit, without touching tmux, when a current
// listing shows the list's window no longer zoomed: the user went to
// another pane, and tmux ended the zoom.
func (m model) zoomLost() model {
	if !m.cockpit.open || m.zooming || m.mux == nil {
		return m
	}
	if p, ok := m.pane(m.self); ok && !p.Zoomed {
		m.cockpit = cockpit{}
	}
	return m
}

// closesCockpit says whether an action leaves the cockpit before it acts:
// those that show another pane or take the list pane for something else.
func closesCockpit(v verb) bool {
	switch v {
	case open, newSession, newTerminal, openReview, addProject, toggleScreen, showScreen:
		return true
	}
	return false
}

// target is what the cockpit shows: a workstream, when task is set, or
// else the project at root.
type target struct {
	root  string
	task  string
	issue issues.Issue
}

// cockpitTarget is the cockpit's target for the selected entry: the
// workstream it is or sits under, or else the project it belongs to, as
// rootOf finds it. ok says whether anything is selected.
func cockpitTarget(e entry, ok bool, rootOf func(entry, bool) string) target {
	if root, i, on := e.workstream(); ok && on {
		return target{root: root, task: e.task, issue: i}
	}
	return target{root: rootOf(e, ok)}
}

// target is the cockpit's target now.
func (m model) target() target {
	e, _, ok := m.selected(m.ordered())
	return cockpitTarget(e, ok, m.rootOf)
}

// cockpitListWidth is the list's width in the cockpit, 0 when the window
// has room only for the cards.
func cockpitListWidth(want, w int) int {
	if want <= 0 {
		want = cockpitList
	}
	if w < want+1+cockpitCardsMin {
		return 0
	}
	return want
}

// cockpitBody is the cockpit's lines between the header and the footer:
// the list at its kept width and a border, then the cards.
func (m model) cockpitBody() []string {
	h := m.listHeight()
	lw := cockpitListWidth(m.width.want, m.w)
	cw := m.w
	var left []string
	if lw > 0 {
		lm := m
		lm.w = lw
		left = pad(lm.sessionsView(), h)
		for i, l := range left {
			if l == "" {
				left[i] = lm.plain("")
			}
		}
		cw = m.w - lw - 1
	}
	cards := m.cockpitCards(cw, h)
	out := make([]string, h)
	edge := fg(pal.Border).Render("│")
	for i := range out {
		if lw > 0 {
			out[i] = left[i] + edge + cards[i]
		} else {
			out[i] = cards[i]
		}
	}
	return out
}

// cockpitCards is the cards for the target, h lines of exactly w cells.
func (m model) cockpitCards(w, h int) []string {
	out := make([]string, h)
	for i := range out {
		out[i] = strings.Repeat(" ", w)
	}
	return out
}

// inCockpitList says whether a click at column x lands on the list in the
// cockpit.
func (m model) inCockpitList(x int) bool {
	lw := cockpitListWidth(m.width.want, m.w)
	return lw > 0 && x < lw
}
