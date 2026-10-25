package review

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"overview/internal/diff"
)

var t0 = time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)

func anchor(path string, start int, lines ...string) *Anchor {
	return &Anchor{Path: path, Start: start, End: start + len(lines) - 1, Lines: lines}
}

func note(id string, a Author, body string, an *Anchor) Event {
	return Event{Op: OpNote, ID: id, Author: a, Body: body, Anchor: an, At: t0}
}

func reply(id, noteID string, a Author, body string) Event {
	return Event{Op: OpReply, ID: id, Note: noteID, Author: a, Body: body, At: t0}
}

func TestFold(t *testing.T) {
	an := anchor("a.go", 3, "x", "y")
	tests := []struct {
		name   string
		events []Event
		check  func(Review) string
	}{
		{"note", []Event{note("n1", User, "hi", an)}, func(r Review) string {
			if len(r.Notes) != 1 || r.Notes[0].Thread[0].Body != "hi" {
				return "want one note"
			}
			return ""
		}},
		{"duplicate id ignored", []Event{note("n1", User, "hi", an), note("n1", User, "again", an), reply("n1", "n1", User, "r")}, func(r Review) string {
			if len(r.Notes) != 1 || len(r.Notes[0].Thread) != 1 {
				return "duplicate accepted"
			}
			return ""
		}},
		{"bad anchors ignored", []Event{
			note("a", User, "b", nil),
			note("b", User, "b", &Anchor{Path: "", Start: 1, End: 1, Lines: []string{"x"}}),
			note("c", User, "b", &Anchor{Path: "p", Start: 0, End: 0, Lines: []string{"x"}}),
			note("d", User, "b", &Anchor{Path: "p", Start: 2, End: 1}),
			note("e", User, "b", &Anchor{Path: "p", Start: 1, End: 2, Lines: []string{"x"}}),
			note("f", User, "  \n", an),
			note("", User, "b", an),
		}, func(r Review) string {
			if len(r.Notes) != 0 {
				return "accepted a bad note"
			}
			return ""
		}},
		{"user reply reopens and clears addressed", []Event{
			note("n", User, "b", an),
			{Op: OpResolve, Note: "n", Author: User},
			{Op: OpReopen, Note: "n", Author: User},
			{Op: OpAddressed, Note: "n", Author: Agent},
			reply("r", "n", User, "more"),
		}, func(r Review) string {
			n := r.Notes[0]
			if n.Resolved || n.Addressed {
				return fmt.Sprintf("%+v", n)
			}
			return ""
		}},
		{"resolved by user then user reply reopens", []Event{
			note("n", User, "b", an), {Op: OpResolve, Note: "n", Author: User}, reply("r", "n", User, "x"),
		}, func(r Review) string {
			if r.Notes[0].Resolved {
				return "still resolved"
			}
			return ""
		}},
		{"agent reply does not reopen", []Event{
			note("n", User, "b", an), {Op: OpResolve, Note: "n", Author: User}, reply("r", "n", Agent, "x"),
		}, func(r Review) string {
			if !r.Notes[0].Resolved {
				return "reopened"
			}
			return ""
		}},
		{"agent cannot resolve or reopen", []Event{
			note("n", User, "b", an), {Op: OpResolve, Note: "n", Author: Agent},
		}, func(r Review) string {
			if r.Notes[0].Resolved {
				return "agent resolved"
			}
			return ""
		}},
		{"addressed ignored when resolved or by user", []Event{
			note("n", User, "b", an), {Op: OpAddressed, Note: "n", Author: User},
			{Op: OpResolve, Note: "n", Author: User}, {Op: OpAddressed, Note: "n", Author: Agent},
		}, func(r Review) string {
			if r.Notes[0].Addressed {
				return "addressed"
			}
			return ""
		}},
		{"edit own unsent only", []Event{
			note("n", User, "b", an), reply("r", "n", Agent, "a"),
			{Op: OpEdit, ID: "n", Author: Agent, Body: "hijack"},
			{Op: OpEdit, ID: "r", Author: Agent, Body: "a2"},
			{Op: OpEdit, ID: "n", Author: User, Body: "b2"},
			{Op: OpSent, Author: User, IDs: []string{"n"}},
			{Op: OpEdit, ID: "n", Author: User, Body: "b3"},
			{Op: OpEdit, ID: "r", Author: Agent, Body: " "},
		}, func(r Review) string {
			th := r.Notes[0].Thread
			if th[0].Body != "b2" || th[1].Body != "a2" || !th[0].Sent {
				return fmt.Sprintf("%+v", th)
			}
			return ""
		}},
		{"waiting", []Event{note("n", User, "b", an), reply("r", "n", Agent, "a")}, func(r Review) string {
			if !r.Notes[0].Waiting() {
				return "not waiting"
			}
			return ""
		}},
		{"unknown op", []Event{{Op: "boom", ID: "x"}, {}}, func(r Review) string {
			if len(r.Notes) != 0 {
				return "note"
			}
			return ""
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if msg := tc.check(Fold(tc.events)); msg != "" {
				t.Error(msg)
			}
		})
	}
}

func TestPendingAndSummarise(t *testing.T) {
	an := anchor("a.go", 1, "x")
	r := Fold([]Event{
		note("n1", User, "one", an),
		note("n2", User, "two", an),
		note("n3", Agent, "agent's", an),
		reply("r1", "n1", Agent, "ok"),
		reply("r2", "n1", User, "no"),
		{Op: OpSent, Author: User, IDs: []string{"n2"}},
		{Op: OpResolve, Note: "n3", Author: User},
	})
	p := Pending(r)
	if len(p) != 1 || p[0].Note.ID != "n1" || len(p[0].Entries) != 2 {
		t.Fatalf("pending %+v", p)
	}
	got := Summarise(r)
	if got != (Summary{Open: 2, Waiting: 0, Unsent: 1}) {
		t.Errorf("summary %+v", got)
	}
}

func TestSideText(t *testing.T) {
	var s Side
	if err := s.UnmarshalText([]byte("old")); err != nil || s != Old {
		t.Fatal(s, err)
	}
	if s.UnmarshalText([]byte("x")) == nil {
		t.Error("accepted x")
	}
	b := Encode(note("n", User, "b", &Anchor{Path: "p", Side: Old, Start: 1, End: 1, Lines: []string{"x"}}))
	if !strings.Contains(string(b), `"side":"old"`) {
		t.Errorf("%s", b)
	}
}

func TestPathFor(t *testing.T) {
	for id, ok := range map[string]bool{"abc-123_X": true, "": false, "a/b": false, "..": false, "a.b": false, strings.Repeat("a", 129): false, strings.Repeat("a", 128): true} {
		p, err := PathFor("/d", id)
		if (err == nil) != ok || ok && p != "/d/"+id+".jsonl" {
			t.Errorf("%q: %q %v", id, p, err)
		}
	}
}

func TestParseLog(t *testing.T) {
	log := string(Encode(Event{Op: OpReopen, Note: "a", Author: User})) + "garbage\n\n" +
		string(Encode(Event{Op: OpReopen, Note: "b", Author: User})) + `{"op":"reopen","note":"c"`
	got := ParseLog([]byte(log))
	if len(got) != 2 || got[1].Note != "b" {
		t.Errorf("%+v", got)
	}
}

func TestStoreAppendAfterTornWrite(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "sub", "x.jsonl")}
	if evs, err := s.Load(); err != nil || evs != nil {
		t.Fatal(evs, err)
	}
	if err := s.Append(Event{Op: OpReopen, Note: "a", Author: User}); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(s.Path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"op":"reo`)
	f.Close()
	if err := s.Append(Event{Op: OpReopen, Note: "b", Author: User}); err != nil {
		t.Fatal(err)
	}
	evs, _ := s.Load()
	if len(evs) != 2 || evs[1].Note != "b" {
		t.Errorf("%+v", evs)
	}
}

func TestStoreConcurrentAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.jsonl")
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := Store{Path: path}
			for i := range 50 {
				id := fmt.Sprintf("g%di%d", g, i)
				if err := s.Append(reply(id, "n", User, strings.Repeat("long body ", 500))); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	evs, err := Store{Path: path}.Load()
	if err != nil || len(evs) != 400 {
		t.Fatalf("%d events, %v", len(evs), err)
	}
	seen := map[string]bool{}
	for _, e := range evs {
		seen[e.ID] = true
	}
	if len(seen) != 400 {
		t.Errorf("%d distinct", len(seen))
	}
}

func TestNewID(t *testing.T) {
	id, err := NewID(nil, bytes.NewReader([]byte{0, 1, 25, 26, 31}))
	if err != nil || id != "abz27" {
		t.Errorf("%q %v", id, err)
	}
	n := 0
	id, err = NewID(func(string) bool { n++; return n < 3 }, bytes.NewReader(bytes.Repeat([]byte{1}, 15)))
	if err != nil || n != 3 || id != "bbbbb" {
		t.Errorf("%q %d %v", id, n, err)
	}
	if _, err := NewID(nil, bytes.NewReader([]byte{1})); err == nil {
		t.Error("short reader accepted")
	}
	if _, err := NewID(func(string) bool { return true }, bytes.NewReader(bytes.Repeat([]byte{1}, 5005))); err == nil {
		t.Error("endless retry")
	}
}

func file(oldp, newp string, lines ...diff.Line) diff.File {
	return diff.File{OldPath: oldp, NewPath: newp, Hunks: []diff.Hunk{{Lines: lines}}}
}

func TestLocate(t *testing.T) {
	f := file("a.go", "a.go",
		diff.Line{Kind: diff.Context, Old: 1, New: 1, Text: "a"},
		diff.Line{Kind: diff.Removed, Old: 2, Text: "gone"},
		diff.Line{Kind: diff.Added, New: 2, Text: "b"},
		diff.Line{Kind: diff.Added, New: 3, Text: "c"},
		diff.Line{Kind: diff.Context, Old: 3, New: 4, Text: "b"},
		diff.Line{Kind: diff.Added, New: 5, Text: "c"},
	)
	tests := []struct {
		name string
		a    Anchor
		want int
		ok   bool
	}{
		{"same place", Anchor{Side: New, Start: 2, End: 3, Lines: []string{"b", "c"}}, 2, true},
		{"moved", Anchor{Side: New, Start: 5, End: 6, Lines: []string{"b", "c"}}, 4, true},
		{"tie goes earlier", Anchor{Side: New, Start: 3, End: 4, Lines: []string{"b", "c"}}, 2, true},
		{"changed", Anchor{Side: New, Start: 2, End: 2, Lines: []string{"zzz"}}, 0, false},
		{"old side", Anchor{Side: Old, Start: 2, End: 2, Lines: []string{"gone"}}, 2, true},
		{"old side lacks added", Anchor{Side: Old, Start: 2, End: 2, Lines: []string{"c"}}, 0, false},
		{"no lines", Anchor{Side: New, Start: 1, End: 0}, 0, false},
		{"tie", Anchor{Side: New, Start: 3, End: 3, Lines: []string{"b"}}, 2, true},
	}
	for _, tc := range tests {
		got, ok := Locate(tc.a, f)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: %d %v", tc.name, got, ok)
		}
	}
}

func TestPlaces(t *testing.T) {
	files := []diff.File{
		file("", "new.go", diff.Line{Kind: diff.Added, New: 1, Text: "x"}),
		file("old.go", "", diff.Line{Kind: diff.Removed, Old: 1, Text: "y"}),
	}
	r := Fold([]Event{
		note("a", User, "b", &Anchor{Path: "new.go", Start: 1, End: 1, Lines: []string{"x"}}),
		note("b", User, "b", &Anchor{Path: "new.go", Start: 1, End: 1, Lines: []string{"q"}}),
		note("c", User, "b", &Anchor{Path: "gone.go", Start: 1, End: 1, Lines: []string{"x"}}),
		note("d", User, "b", &Anchor{Path: "old.go", Side: Old, Start: 1, End: 1, Lines: []string{"y"}}),
		note("e", User, "b", &Anchor{Path: "old.go", Side: New, Start: 1, End: 1, Lines: []string{"y"}}),
	})
	got := Places(r, files)
	want := map[string]Place{
		"a": {0, 1, 1, false}, "b": {0, 1, 1, true}, "c": {-1, 1, 1, true},
		"d": {1, 1, 1, false}, "e": {-1, 1, 1, true},
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: %+v want %+v", id, got[id], w)
		}
	}
}

func TestMessageGolden(t *testing.T) {
	n1 := Fold([]Event{note("k3f9a", User, "This should be a pure function.", anchor("internal/tui/tui.go", 120, "a", "b"))}).Notes[0]
	r := Fold([]Event{
		note("b7x2q", User, "q", anchor("internal/x.go", 40, "z")),
		reply("c1", "b7x2q", Agent, "Done,\nit is   fixed."),
		reply("d2", "b7x2q", User, "Still wrong."),
	})
	n2, _ := r.Find("b7x2q")
	items := []Item{
		{Note: n1, Entries: n1.Thread},
		{Note: n2, Entries: []Entry{n2.Thread[2]}},
	}
	got := Message(items, "/tmp/it's/r.jsonl", "/bin/overview")
	want := `Review notes on your changes: 1 new note, 1 reply.

[k3f9a] internal/tui/tui.go, new lines 120-121
    120  a
    121  b
  This should be a pure function.

[b7x2q] the user replied on internal/x.go, new line 40
  > Done, it is fixed.
  Still wrong.

Answer each in its thread, then make any changes they ask for:
Put your text in a quoted heredoc so your shell leaves backticks and $VARS alone:
  /bin/overview review reply --store '/tmp/it'\''s/r.jsonl' <id> <<'EOF'
  your reply
  EOF
  /bin/overview review addressed --store '/tmp/it'\''s/r.jsonl' <id> <<'EOF'
  what you did (optional)
  EOF
  /bin/overview review notes --store '/tmp/it'\''s/r.jsonl'     every open note with its thread
Notes are never deleted, and only the user resolves them.
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// A carriage return in a note would reach the agent's prompt as Enter.
// The property once failed on the body "\rA".
func TestMessageCarriageReturns(t *testing.T) {
	for _, body := range []string{"\rA", "one\rtwo", "one\r\ntwo"} {
		n := Fold([]Event{note("n", User, body, anchor("p", 1, "x"))}).Notes[0]
		got := Message([]Item{{Note: n, Entries: n.Thread}}, "s", "e")
		if strings.Contains(got, "\r") {
			t.Errorf("%q: carriage return left in message:\n%q", body, got)
		}
	}
	n := Fold([]Event{note("n", User, "one\rtwo", anchor("p", 1, "x\r"))}).Notes[0]
	got := Message([]Item{{Note: n, Entries: n.Thread}}, "s", "e")
	if strings.Contains(got, "\r") || !strings.Contains(got, "  one\n  two\n") {
		t.Errorf("lines not split:\n%s", got)
	}
}

func TestMessageTruncates(t *testing.T) {
	lines := make([]string, 15)
	for i := range lines {
		lines[i] = "l"
	}
	n := Fold([]Event{note("n", User, "b", anchor("p", 8, lines...))}).Notes[0]
	got := Message([]Item{{Note: n, Entries: n.Thread}}, "s", "e")
	if strings.Count(got, "  l\n") != 12 || !strings.Contains(got, "    …\n") || !strings.Contains(got, "  19  l") {
		t.Errorf("%s", got)
	}
}

func TestReady(t *testing.T) {
	rule := strings.Repeat("─", 40)
	tests := []struct {
		name   string
		screen string
		want   error
	}{
		{"empty nbsp", "out\n" + rule + "\n❯ \n" + rule + "\n  status\n", nil},
		{"empty space", "out\n" + rule + "\n❯ \n" + rule + "\n", nil},
		{"draft first line", rule + "\n❯ hello\n" + rule + "\n", ErrDraft},
		{"draft second line", rule + "\n❯ \n  more\n" + rule + "\n", ErrDraft},
		{"shell mode", rule + "\n! \n" + rule + "\n", ErrNoPrompt},
		{"no rule below", "out\n" + rule + "\n❯ \n  status\n", ErrNoPrompt},
		{"no rules", "❯ \n", ErrNoPrompt},
		{"empty screen", "", ErrNoPrompt},
		{"adjacent rules", rule + "\n" + rule + "\n", ErrNoPrompt},
		{"rules in output above", rule + "\ntext\n" + rule + "\nmore\n" + rule + "\n❯ \n" + rule + "\n", nil},
		{"short rule is not one", "──────\n❯ \n──────\n", ErrNoPrompt},
		{"padded rules", "  " + rule + "  \n❯ \n  " + rule + "\n", nil},
	}
	for _, tc := range tests {
		if got := Ready(tc.screen); !errors.Is(got, tc.want) {
			t.Errorf("%s: %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestReadyCaptures(t *testing.T) {
	for file, want := range map[string]error{
		"idle-empty.txt": nil, "multiline-draft.txt": ErrDraft,
		"bash-mode.txt": ErrNoPrompt, "trust-dialog.txt": ErrNoPrompt,
	} {
		b, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		if got := Ready(string(b)); !errors.Is(got, want) {
			t.Errorf("%s: %v want %v", file, got, want)
		}
	}
}

func runCmd(t *testing.T, rnd string, args ...string) (string, error) {
	t.Helper()
	return runIn(t, "", rnd, args...)
}

func runIn(t *testing.T, in, rnd string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := Command(args, strings.NewReader(in), &out, func() time.Time { return t0 }, strings.NewReader(rnd))
	return out.String(), err
}

func TestCommandVerbatimText(t *testing.T) {
	store := filepath.Join(t.TempDir(), "s.jsonl")
	Store{Path: store}.Append(note("n1", User, "b", anchor("a", 1, "x")))
	last := func() string {
		r, _, _ := load(store)
		n, _ := r.Find("n1")
		return n.Thread[len(n.Thread)-1].Body
	}
	// Text after the id is never parsed as flags.
	k := 0
	for _, text := range []string{"- Fixed the parser", "--no-verify was the cause"} {
		for _, sub := range []string{"reply", "addressed"} {
			k++
			out, err := runCmd(t, strings.Repeat(string(rune(10+k)), 5), sub, "--store", store, "n1", text)
			if err != nil || strings.Contains(out, "Usage") {
				t.Fatalf("%s %q: %q %v", sub, text, out, err)
			}
			if got := last(); got != text {
				t.Errorf("%s: body %q want %q", sub, got, text)
			}
		}
	}
	// A body on stdin arrives untouched, however the shell would have
	// treated it unquoted.
	body := "- Fixed `parse` in $HOME/x\n\n    indented \"q\" 'p'\n"
	if _, err := runIn(t, body, "\x05\x05\x05\x05\x05", "reply", "--store", store, "n1"); err != nil {
		t.Fatal(err)
	}
	if got := last(); got != strings.TrimRight(body, "\n") {
		t.Errorf("stdin reply %q", got)
	}
	if _, err := runIn(t, body, "\x06\x06\x06\x06\x06", "addressed", "--store", store, "n1"); err != nil {
		t.Fatal(err)
	}
	if got := last(); got != strings.TrimRight(body, "\n") {
		t.Errorf("stdin addressed %q", got)
	}
	// Args win over stdin.
	if _, err := runIn(t, "from stdin", "\x07\x07\x07\x07\x07", "reply", "--store", store, "n1", "from args"); err != nil || last() != "from args" {
		t.Errorf("%v %q", err, last())
	}
	// Empty stdin: addressed is fine, reply is not.
	if _, err := runIn(t, "\n", "\x08\x08\x08\x08\x08", "reply", "--store", store, "n1"); err == nil {
		t.Error("empty stdin reply accepted")
	}
	if _, err := runIn(t, "", "\x09\x09\x09\x09\x09", "addressed", "--store", store, "n1"); err != nil {
		t.Error(err)
	}
	// --store after the id is text, not a flag.
	if _, err := runCmd(t, "\x0a\x0a\x0a\x0a\x0a", "reply", "n1", "--store", store); err == nil || !strings.Contains(err.Error(), "--store is required") {
		t.Errorf("--store after the id counted: %v", err)
	}
}

func TestCommandFlagErrorsStayOffOut(t *testing.T) {
	for _, args := range [][]string{
		{"reply", "--bogus", "n1", "x"}, {"notes", "--nope"}, {"addressed", "-h"}, {"reply", "--store", "f"},
	} {
		out, err := runCmd(t, "", args...)
		if err == nil || out != "" {
			t.Errorf("%v: out %q err %v", args, out, err)
		}
	}
	if _, err := runCmd(t, "", "reply", "--bogus", "n1", "x"); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Errorf("no usage in %v", err)
	}
}

func TestCommand(t *testing.T) {
	store := filepath.Join(t.TempDir(), "r", "s.jsonl")
	s := Store{Path: store}
	if out, err := runCmd(t, "", "notes", "--store", store); err != nil || out != "No open notes.\n" {
		t.Fatalf("%q %v", out, err)
	}
	s.Append(note("n0001", User, "Please rename.", anchor("a.go", 5, "func f() {", "}")),
		note("n0002", User, "Old.", anchor("a.go", 9, "x")))
	s.Append(Event{Op: OpResolve, Note: "n0002", Author: User})

	out, err := runCmd(t, "", "notes", "--store", store)
	if err != nil || !strings.Contains(out, "[n0001] a.go, new lines 5-6") || strings.Contains(out, "n0002") ||
		!strings.Contains(out, "    5  func f() {") || !strings.Contains(out, "user, 2026-10-21 09:00") {
		t.Fatalf("%q %v", out, err)
	}

	// Text with a dash word.
	out, err = runCmd(t, "\x00\x00\x00\x00\x00", "reply", "--store", store, "n0001", "Renamed", "--quickly", "now")
	if err != nil || !strings.Contains(out, "Replied to n0001 (reply aaaaa)") {
		t.Fatalf("%q %v", out, err)
	}
	r, _, _ := load(store)
	n, _ := r.Find("n0001")
	if len(n.Thread) != 2 || n.Thread[1].Body != "Renamed --quickly now" || n.Thread[1].Author != Agent || !n.Waiting() {
		t.Fatalf("%+v", n)
	}

	out, err = runCmd(t, "\x00\x00\x00\x00\x00\x01\x01\x01\x01\x01", "addressed", "--store", store, "n0001", "Did", "it")
	if err != nil || !strings.Contains(out, "Marked n0001 addressed") {
		t.Fatalf("%q %v", out, err)
	}
	r, _, _ = load(store)
	n, _ = r.Find("n0001")
	if !n.Addressed || len(n.Thread) != 3 || n.Thread[2].ID != "bbbbb" {
		t.Fatalf("%+v", n)
	}
	if _, err := runCmd(t, "", "addressed", "--store", store, "n0001"); err != nil {
		t.Fatal(err)
	}

	out, err = runCmd(t, "\x02\x02\x02\x02\x02", "reply", "--store", store, "n0002", "late")
	if err != nil || !strings.Contains(out, "resolved") {
		t.Errorf("%q %v", out, err)
	}
}

func TestCommandErrors(t *testing.T) {
	store := filepath.Join(t.TempDir(), "s.jsonl")
	Store{Path: store}.Append(note("n1", User, "b", anchor("a", 1, "x")), Event{Op: OpResolve, Note: "n1", Author: User})
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"none", nil, "notes, reply or addressed"},
		{"resolve", []string{"resolve", "--store", store, "n1"}, `unknown subcommand "resolve"`},
		{"no store", []string{"notes"}, "--store"},
		{"extra", []string{"notes", "--store", store, "x"}, "unexpected"},
		{"reply missing note", []string{"reply", "--store", store, "zz", "hi"}, `no note "zz"`},
		{"reply empty", []string{"reply", "--store", store, "n1", " "}, "empty"},
		{"reply no id", []string{"reply", "--store", store}, "need a note id"},
		{"addressed resolved", []string{"addressed", "--store", store, "n1"}, "already resolved by the user"},
		{"addressed missing", []string{"addressed", "--store", store, "zz"}, `no note "zz"`},
		{"bad flag", []string{"notes", "--nope"}, "nope"},
	}
	for _, tc := range tests {
		if _, err := runCmd(t, "aaaaaaaaaaaa", tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}
