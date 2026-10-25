package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Input works in two steps. Every key, mouse and focus event arrives
// through Update, one at a time, and is first translated into actions:
// what the user asked for, whichever device asked. Translation reads the
// model's mode (searching, an overlay, the screen, the panel the keys are
// in) and changes nothing. apply then carries out each action, and is the
// only place behaviour lives, so enter and a double click, or j and the
// wheel, do exactly the same thing. It runs nothing itself: what reaches
// tmux or the disk comes back as a Cmd. A key or a click first says where
// the user is, as an action of its own, and is translated for the model
// that results.

type verb int

const (
	keysBack    verb = iota // the keyboard is back in the view, from the pane it was in
	keysLeft                // the keyboard has gone to another pane
	pointerHere             // the mouse is over the view, wherever the keyboard is
	quit
	detach // leave the view running in tmux and close this terminal's view of it
	openHelp
	dismiss     // close the overlay; a pending stop is cancelled
	confirmStop // say yes to the pending stop
	showScreen  // n is the screen
	toggleScreen
	writeDigest
	refresh
	move      // n rows down the list, negative for up
	moveTo    // the nth row, clamped to the list
	nextNeedy // the next (n=1) or previous (n=-1) row that needs you
	scrollDigest
	scrollDigestTo
	stepDigest // n digests newer, negative for older
	search     // start searching
	typeText   // add text to the search
	backspace
	cancelSearch // stop searching and clear it
	clearFilter
	open  // enter: type into the session beside the list
	click // a click on the entry keyed id at time at; a second one soon after opens it
	stop  // stop a background session or close a terminal, after a yes; unpin an empty project
	copyID
	newSession  // a claude in the selected entry's project
	newTerminal // a shell in the selected entry's project
	addProject  // open the add-project picker
	pickType    // add text to the picker's query
	pickBack    // backspace in the picker
	pickMove    // n matches down the picker, negative for up
	pickTab     // complete the query to the selected match
	pickEnter   // pin the selected match
	pickCancel  // close the picker
	startTask   // open the task picker for the selected entry's project
	taskType    // add text to the task picker's query
	taskBack    // backspace in the task picker
	taskMove    // n issues down the task picker, negative for up
	taskScroll  // n lines down the task picker's details, negative for up
	taskEnter   // start or go to the selected issue
	taskCancel  // close the task picker
	taskClick   // a click on the nth issue at time at; a second one soon after acts on it
	openReview  // review the selected session's changes in a split above it
	toCockpit   // open the cockpit for the selected entry
	outCockpit  // close the cockpit
	cockpitTab  // move the keyboard between the list and the cockpit's issues card
	cockpitMove // n issues down the issues card, negative for up
	cockpitPick // start a workstream on the issue under the issues card's cursor
	navMove     // select the nearest panel in direction n
	navIn       // go into the selected panel
	navOut      // come out of the panel the keys are in, to choose another
)

type action struct {
	do   verb
	n    int
	id   string
	text string
	at   time.Time
}

// end is a line or row number past any list, clamped to the last.
const end = 1 << 30

func do(v verb) []action { return []action{{do: v}} }

func doN(v verb, n int) []action { return []action{{do: v, n: n}} }

// keyActions translates a key press.
func (m model) keyActions(msg tea.KeyMsg) []action {
	k := msg.String()
	if m.taskPick.open {
		switch msg.Type {
		case tea.KeyEsc:
			return do(taskCancel)
		case tea.KeyEnter:
			return do(taskEnter)
		case tea.KeyUp, tea.KeyCtrlP:
			return doN(taskMove, -1)
		case tea.KeyDown, tea.KeyCtrlN:
			return doN(taskMove, 1)
		case tea.KeyPgDown, tea.KeyCtrlD:
			return doN(taskScroll, max(m.listHeight()/2, 1))
		case tea.KeyPgUp, tea.KeyCtrlU:
			return doN(taskScroll, -max(m.listHeight()/2, 1))
		case tea.KeyBackspace:
			return do(taskBack)
		case tea.KeyRunes, tea.KeySpace:
			return []action{{do: taskType, text: printable(string(msg.Runes))}}
		case tea.KeyCtrlC:
			return do(quit)
		}
		return nil
	}
	if m.pick.open {
		switch msg.Type {
		case tea.KeyEsc:
			return do(pickCancel)
		case tea.KeyEnter:
			return do(pickEnter)
		case tea.KeyTab:
			return do(pickTab)
		case tea.KeyUp, tea.KeyCtrlP:
			return doN(pickMove, -1)
		case tea.KeyDown, tea.KeyCtrlN:
			return doN(pickMove, 1)
		case tea.KeyBackspace:
			return do(pickBack)
		case tea.KeyRunes, tea.KeySpace:
			return []action{{do: pickType, text: printable(string(msg.Runes))}}
		case tea.KeyCtrlC:
			return do(quit)
		}
		return nil
	}
	if m.filtering {
		switch msg.Type {
		case tea.KeyEsc:
			return do(cancelSearch)
		case tea.KeyEnter:
			return do(open)
		case tea.KeyUp, tea.KeyCtrlP:
			return doN(move, -1)
		case tea.KeyDown, tea.KeyCtrlN:
			return doN(move, 1)
		case tea.KeyBackspace:
			return do(backspace)
		case tea.KeyRunes, tea.KeySpace:
			return []action{{do: typeText, text: printable(string(msg.Runes))}}
		case tea.KeyCtrlC:
			return do(quit)
		}
		return nil
	}
	if m.confirm != "" {
		if k == "y" {
			return do(confirmStop)
		}
		return do(dismiss)
	}
	if m.help {
		return do(dismiss)
	}
	// Several runes in one message are a paste or a fast burst, not
	// keystrokes. They can start a search but never trigger an action.
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 {
		if msg.Runes[0] == '/' && m.screen == sessionsScreen {
			return []action{{do: search}, {do: typeText, text: printable(string(msg.Runes[1:]))}}
		}
		return nil
	}

	switch k {
	case "q", "ctrl+c":
		return do(detach)
	case "Q":
		return do(quit)
	case "?":
		return do(openHelp)
	case "d":
		return do(toggleScreen)
	case "1":
		return doN(showScreen, int(sessionsScreen))
	case "2":
		return doN(showScreen, int(digestScreen))
	case "D":
		return do(writeDigest)
	case "r":
		return do(refresh)
	}
	if m.screen == digestScreen {
		page := max(m.h/2, 1)
		switch k {
		case "j", "down", "J":
			return doN(scrollDigest, 1)
		case "k", "up", "K":
			return doN(scrollDigest, -1)
		case "ctrl+d", "pgdown", " ":
			return doN(scrollDigest, page)
		case "ctrl+u", "pgup":
			return doN(scrollDigest, -page)
		case "g", "home":
			return doN(scrollDigestTo, 0)
		case "G", "end":
			return doN(scrollDigestTo, end)
		case "[", "h", "left":
			return doN(stepDigest, -1)
		case "]", "l", "right":
			return doN(stepDigest, 1)
		case "esc", "tab":
			return doN(showScreen, int(sessionsScreen))
		}
		return nil
	}

	if m.cockpit.open {
		if as := m.cockpitKeys(k); as != nil {
			return as
		}
	}
	if !m.nav.inside {
		switch k {
		case "h", "left":
			return doN(navMove, int(leftward))
		case "l", "right":
			return doN(navMove, int(rightward))
		case "k", "up":
			return doN(navMove, int(upward))
		case "j", "down":
			return doN(navMove, int(downward))
		case "enter":
			return do(navIn)
		}
	}
	switch k {
	case "j", "down":
		return doN(move, 1)
	case "k", "up":
		return doN(move, -1)
	case "g", "home":
		return doN(moveTo, 0)
	case "G", "end":
		return doN(moveTo, end)
	case "esc":
		if m.filter == "" && m.nav.inside {
			return do(navOut)
		}
		return do(clearFilter)
	case "n":
		return doN(nextNeedy, 1)
	case "N":
		return doN(nextNeedy, -1)
	case "/":
		return do(search)
	case "enter", "tab", "l", "right":
		return do(open)
	case "x":
		return do(stop)
	case "y":
		return do(copyID)
	case "c":
		return do(newSession)
	case "t":
		return do(newTerminal)
	case "v":
		return do(openReview)
	case "a":
		return do(addProject)
	case "s":
		return do(startTask)
	case "o":
		return do(toCockpit)
	}
	return nil
}

// cockpitKeys translates the keys that mean something else while the
// cockpit is open, nil for the others: o closes it, and so does esc once
// no filter is applied to the list and the keyboard is out on the panels,
// tab moves the keyboard between the list and the issues card, and in
// that card the arrows move its cursor and enter acts on its issue.
func (m model) cockpitKeys(k string) []action {
	switch k {
	case "esc":
		switch {
		case m.filter != "":
			return do(clearFilter)
		case m.nav.inside:
			return do(navOut)
		}
		return do(outCockpit)
	case "o":
		return do(outCockpit)
	case "tab":
		return do(cockpitTab)
	}
	if !m.inIssues() {
		return nil
	}
	switch k {
	case "j", "down":
		return doN(cockpitMove, 1)
	case "k", "up":
		return doN(cockpitMove, -1)
	case "enter":
		return do(cockpitPick)
	}
	return nil
}

// mouseActions translates a mouse event that happened at time at. A click
// selects a row, and two on one row open it. The wheel moves the cursor over
// the list and scrolls the digests. A click closes an overlay, as any key
// does.
func (m model) mouseActions(msg tea.MouseMsg, at time.Time) []action {
	step := 0
	switch {
	case msg.Action != tea.MouseActionPress:
		return nil
	case msg.Button == tea.MouseButtonWheelUp:
		step = -1
	case msg.Button == tea.MouseButtonWheelDown:
		step = 1
	case msg.Button != tea.MouseButtonLeft:
		return nil
	}
	if m.confirm != "" || m.help {
		if step != 0 {
			return nil
		}
		return do(dismiss)
	}
	if m.pick.open {
		return nil // the picker is keys only
	}
	if m.taskPick.open {
		switch {
		case step != 0 && msg.X >= m.taskListWidth():
			return doN(taskScroll, step*wheelLines)
		case step != 0:
			return doN(taskMove, step)
		case msg.Y >= m.listHeight():
			return nil // the footer
		}
		_, owners := m.taskPickBody()
		if line := msg.Y; msg.X < m.taskListWidth() && line >= 0 && line < len(owners) && owners[line] >= 0 {
			return []action{{do: taskClick, n: owners[line], at: at}}
		}
		return nil
	}
	if m.screen == digestScreen {
		if step == 0 {
			return nil
		}
		return doN(scrollDigest, step*wheelLines)
	}

	lh := m.listHeight()
	switch {
	case msg.Y >= lh:
		return nil // the footer
	case m.cockpit.open && !m.inCockpitList(msg.X):
		return nil // the cards
	}
	if step != 0 {
		return doN(move, step)
	}
	col, line := m.shownColumn(), msg.Y
	if m.cockpit.open {
		line-- // under the rule the cockpit draws
	}
	if line >= 0 && line < len(col.owners) && col.owners[line] != "" {
		return []action{{do: click, id: col.owners[line], at: at}}
	}
	return nil
}

// shownColumn is the list's column as the screen shows it, where clicks land.
func (m model) shownColumn() column {
	secs := m.sections()
	cur, _, _ := m.selected(flatten(secs))
	if m.cockpit.open {
		lm := m
		lm.w = cockpitListWidth(m.width.want, m.w)
		return lm.column(secs, cur.key(), m.listHeight()-1)
	}
	return m.column(secs, cur.key(), m.listHeight())
}
