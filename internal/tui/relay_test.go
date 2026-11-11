package tui

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"pgregory.net/rapid"
)

// genPaneMsg draws something the agents' pane's program passes on.
func genPaneMsg(t *rapid.T) tea.Msg {
	switch rapid.IntRange(0, 4).Draw(t, "kind") {
	case 0:
		k := tea.Key{Type: tea.KeyType(rapid.IntRange(-30, 30).Draw(t, "type")), Alt: rapid.Bool().Draw(t, "alt"), Paste: rapid.Bool().Draw(t, "paste")}
		if k.Type == tea.KeyRunes {
			k.Runes = []rune(rapid.StringMatching(`[a-zé/ ]{1,4}`).Draw(t, "runes"))
		}
		return tea.KeyMsg(k)
	case 1:
		return tea.MouseMsg{X: rapid.IntRange(0, 300).Draw(t, "x"), Y: rapid.IntRange(0, 100).Draw(t, "y"), Shift: rapid.Bool().Draw(t, "shift"),
			Action: tea.MouseActionPress, Button: tea.MouseButton(rapid.IntRange(0, 5).Draw(t, "button"))}
	case 2:
		return tea.FocusMsg{}
	case 3:
		return tea.BlurMsg{}
	}
	return tea.WindowSizeMsg{Width: rapid.IntRange(0, 300).Draw(t, "w"), Height: rapid.IntRange(0, 100).Draw(t, "h")}
}

// What happens in the agents' pane reaches the view as the message it
// was, through the line of JSON that carries it.
func TestPropWhatThePanePassesOnArrivesAsItWas(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		msg := genPaneMsg(t)
		w, ok := toWire(msg)
		if !ok {
			t.Fatalf("%#v is not passed on", msg)
		}
		b, err := json.Marshal(w)
		if err != nil || strings.Contains(string(b), "\n") {
			t.Fatalf("the line %q, %v", b, err)
		}
		var back wire
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatal(err)
		}
		if got, ok := fromWire(back); !ok || !reflect.DeepEqual(got, msg) {
			t.Fatalf("%#v arrived as %#v", msg, got)
		}
	})
}

// A mouse that only moves, or a button let go, is not passed on, and a
// line that says nothing is no message.
func TestThePanePassesOnOnlyWhatTheViewUses(t *testing.T) {
	for _, msg := range []tea.Msg{
		tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft},
		tea.MouseMsg{Action: tea.MouseActionRelease},
		screenMsg("x"),
	} {
		if w, ok := toWire(msg); ok {
			t.Errorf("%#v is passed on as %+v", msg, w)
		}
	}
	screen := "x"
	if msg, ok := fromWire(wire{Screen: &screen}); ok {
		t.Errorf("a screen arrives as %#v", msg)
	}
}

// The socket's path is short enough for a unix socket whatever the
// session is called, sits beside tmux's, and differs between sessions.
func TestAgentsSocketIsShortAndPerSession(t *testing.T) {
	long := "overview-" + strings.Repeat("x", 200)
	a, b := agentsSocket("/private/tmp/tmux-501/overview", long), agentsSocket("/private/tmp/tmux-501/overview", "overview-other")
	if a == b || filepath.Dir(a) != "/private/tmp/tmux-501" || len(a) > 60 {
		t.Fatalf("sockets %q and %q", a, b)
	}
	if c := agentsSocket("/private/tmp/tmux-501/other", long); c == a {
		t.Fatalf("two servers share %q", c)
	}
}

// link is a view's end of the socket on a short path, with what it was
// given kept in order.
func testLink(t *testing.T) (*agentsLink, string, chan tea.Msg) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ovl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	// A file left by a view that was killed is in the way of nothing.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got := make(chan tea.Msg, 16)
	l, err := listenAgents(path, func(msg tea.Msg) { got <- msg })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.close)
	return l, path, got
}

func next(t *testing.T, got chan tea.Msg) tea.Msg {
	t.Helper()
	select {
	case msg := <-got:
		return msg
	case <-time.After(10 * time.Second):
		t.Fatal("nothing arrived")
		return nil
	}
}

// The view's end of the socket: what a pane passes on arrives in order,
// the screens go back, an older screen that lands late is dropped, and
// the view hears when the pane hangs up.
func TestTheLinkCarriesBothWays(t *testing.T) {
	l, path, got := testLink(t)
	if err := l.draw(1, "nobody is listening"); err != nil {
		t.Fatalf("a screen with no pane connected: %v", err)
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)
	for _, msg := range []tea.Msg{tea.WindowSizeMsg{Width: 40, Height: 5}, tea.FocusMsg{}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}} {
		w, _ := toWire(msg)
		if err := enc.Encode(w); err != nil {
			t.Fatal(err)
		}
		if arrived := next(t, got); !reflect.DeepEqual(arrived, agentsMsg{msg}) {
			t.Fatalf("%#v arrived as %#v", msg, arrived)
		}
	}
	for _, s := range []struct {
		seq    int
		screen string
	}{{3, "three"}, {2, "two, late"}, {4, "four\nlines"}} {
		if err := l.draw(s.seq, s.screen); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"three", "four\nlines"} {
		var w wire
		if err := dec.Decode(&w); err != nil || w.Screen == nil || *w.Screen != want {
			t.Fatalf("the pane was sent %+v, %v, want %q", w, err, want)
		}
	}
	conn.Close()
	if arrived := next(t, got); arrived != (agentsGone{}) {
		t.Fatalf("after the pane hung up: %#v", arrived)
	}
}

// The pane's program shows the screen it was last sent and passes on what
// happens in it. It quits when it can no longer reach the view.
func TestTheRelayShowsWhatItIsSentAndPassesOnTheRest(t *testing.T) {
	var passed []wire
	var fail error
	r := relay{pass: func(w wire) error { passed = append(passed, w); return fail }}
	m, _ := r.Update(screenMsg("one\ntwo"))
	if got := m.View(); got != "one\ntwo" || len(passed) != 0 {
		t.Fatalf("the screen %q, passed on %+v", got, passed)
	}
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(passed) != 1 || passed[0].Key == nil || passed[0].Key.Type != tea.KeyEnter || cmd != nil || m.View() != "one\ntwo" {
		t.Fatalf("enter was passed on as %+v", passed)
	}
	fail = os.ErrClosed
	if _, cmd = m.Update(tea.FocusMsg{}); cmd == nil {
		t.Fatal("the program goes on with no view to reach")
	} else if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Fatal("the program does not quit")
	}
}
