package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Input works in two steps. Every key and mouse event arrives through
// Update, one at a time, and is first translated into actions: what the
// user asked for, whichever device asked. Translation reads the model's
// mode (searching, an overlay, the screen, the focused pane) and changes
// nothing. apply then carries out each action, and is the only place
// behaviour lives, so enter and a double click, or j and the wheel, do
// exactly the same thing.

type verb int

const (
	quit verb = iota
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
	focus     // n is the pane
	scroll    // the detail pane, n lines
	scrollTo  // the detail pane, to line n
	scrollDigest
	scrollDigestTo
	stepDigest // n digests newer, negative for older
	search     // start searching
	typeText   // add text to the search
	backspace
	cancelSearch // stop searching and clear it
	clearFilter
	open   // enter: jump to the session's terminal, or attach
	attach // a: attach, or report the tty without jumping
	click  // a click on row id at time at; a second one soon after opens it
	stop
	copyID
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
	page := max(m.h/2, 1)
	if m.screen == digestScreen {
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

	if m.focus == detailPane {
		switch k {
		case "j", "down":
			return doN(scroll, 1)
		case "k", "up":
			return doN(scroll, -1)
		case "g", "home":
			return doN(scrollTo, 0)
		case "G", "end":
			return doN(scrollTo, end)
		case "esc", "h", "left", "tab", "shift+tab":
			return doN(focus, int(listPane))
		}
	} else {
		switch k {
		case "j", "down":
			return doN(move, 1)
		case "k", "up":
			return doN(move, -1)
		case "g", "home":
			return doN(moveTo, 0)
		case "G", "end":
			return doN(moveTo, end)
		case "l", "right", "tab", "shift+tab":
			return doN(focus, int(detailPane))
		case "esc":
			return do(clearFilter)
		}
	}

	switch k {
	case "n":
		return doN(nextNeedy, 1)
	case "N":
		return doN(nextNeedy, -1)
	case "J", "ctrl+e":
		return doN(scroll, 1)
	case "K", "ctrl+y":
		return doN(scroll, -1)
	case "ctrl+d", "pgdown", " ":
		return doN(scroll, page)
	case "ctrl+u", "pgup":
		return doN(scroll, -page)
	case "/":
		return do(search)
	case "enter":
		return do(open)
	case "a":
		return do(attach)
	case "x":
		return do(stop)
	case "y":
		return do(copyID)
	}
	return nil
}

// mouseActions translates a mouse event that happened at time at. A click
// selects a row or focuses a pane, and two on one row open it. The wheel
// moves the cursor over the list and scrolls the detail pane and digests.
// A click closes an overlay, as any key does.
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
	if m.screen == digestScreen {
		return doN(scrollDigest, step*wheelLines)
	}

	lw, lh, _, _ := m.split()
	inList := msg.X < lw && msg.Y < lh
	switch {
	case !inList && msg.Y >= m.boxHeight():
		return nil // the status bar
	case !inList && step != 0:
		return doN(scroll, step*wheelLines)
	case !inList && m.filtering:
		return nil // the search keeps the list focused, as with keys
	case !inList:
		return doN(focus, int(detailPane))
	case step != 0:
		return doN(move, step)
	}
	out := doN(focus, int(listPane))
	cur, _, _ := m.selected(m.ordered())
	_, owners := m.listView(m.sections(), cur.SessionID)
	if line := msg.Y - 1; line >= 0 && line < min(len(owners), lh-2) && owners[line] != "" {
		out = append(out, action{do: click, id: owners[line], at: at})
	}
	return out
}
