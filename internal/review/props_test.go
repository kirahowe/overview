package review

import (
	"bytes"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"

	"pgregory.net/rapid"

	"overview/internal/diff"
)

func genText() *rapid.Generator[string] {
	return rapid.OneOf(
		rapid.String(),
		rapid.SampledFrom([]string{"", " ", "\n", "héllo", "日本語", "line\nbreak", "a b"}),
		rapid.StringMatching(`[a-z ]{1,12}`),
	)
}

func genTime() *rapid.Generator[time.Time] {
	return rapid.Custom(func(t *rapid.T) time.Time {
		sec := rapid.Int64Range(0, 253402300799).Draw(t, "sec")
		return time.Unix(sec, int64(rapid.IntRange(0, 999999999).Draw(t, "ns"))).UTC()
	})
}

func genID() *rapid.Generator[string] {
	return rapid.OneOf(rapid.SampledFrom([]string{"", "a", "b", "c", "d"}), rapid.StringMatching(`[a-z0-9]{1,3}`))
}

func genAnchor() *rapid.Generator[*Anchor] {
	return rapid.Custom(func(t *rapid.T) *Anchor {
		if rapid.IntRange(0, 9).Draw(t, "nil") == 0 {
			return nil
		}
		a := Anchor{
			Path:  rapid.SampledFrom([]string{"", "a.go", "b/c.go"}).Draw(t, "path"),
			Side:  rapid.SampledFrom([]Side{New, Old}).Draw(t, "side"),
			Start: rapid.IntRange(-1, 6).Draw(t, "start"),
		}
		a.End = a.Start + rapid.IntRange(-1, 3).Draw(t, "len")
		n := a.End - a.Start + 1
		if rapid.IntRange(0, 9).Draw(t, "bad") == 0 {
			n++
		}
		for i := 0; i < n && i < 8; i++ {
			a.Lines = append(a.Lines, genText().Draw(t, "line"))
		}
		return &a
	})
}

var allOps = []Op{OpNote, OpReply, OpEdit, OpResolve, OpReopen, OpAddressed, OpSent, "zzz", ""}

func genEvent() *rapid.Generator[Event] {
	return rapid.Custom(func(t *rapid.T) Event {
		e := Event{
			Op:     rapid.SampledFrom(allOps).Draw(t, "op"),
			ID:     genID().Draw(t, "id"),
			Note:   genID().Draw(t, "note"),
			Author: rapid.SampledFrom([]Author{User, Agent, ""}).Draw(t, "author"),
			Body:   genText().Draw(t, "body"),
			Anchor: genAnchor().Draw(t, "anchor"),
			IDs:    rapid.SliceOfN(genID(), 0, 3).Draw(t, "ids"),
			At:     genTime().Draw(t, "at"),
		}
		if len(e.IDs) == 0 {
			e.IDs = nil
		}
		if e.Anchor != nil && len(e.Anchor.Lines) == 0 {
			e.Anchor.Lines = nil
		}
		return e
	})
}

// genValidNote draws a note event that Fold accepts, with a fresh id.
func genValidNote(t *rapid.T, id string) Event {
	n := rapid.IntRange(1, 3).Draw(t, "n")
	a := Anchor{Path: "a.go", Side: rapid.SampledFrom([]Side{New, Old}).Draw(t, "side"), Start: rapid.IntRange(1, 5).Draw(t, "s")}
	a.End = a.Start + n - 1
	for range n {
		a.Lines = append(a.Lines, genText().Draw(t, "l"))
	}
	return Event{Op: OpNote, ID: id, Author: rapid.SampledFrom([]Author{User, Agent}).Draw(t, "au"), Body: "x", Anchor: &a, At: t0}
}

func TestPropEncodeRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		es := rapid.SliceOf(genEvent()).Draw(t, "events")
		var log []byte
		for _, e := range es {
			b := Encode(e)
			if b == nil || b[len(b)-1] != '\n' || bytes.Count(b, []byte("\n")) != 1 {
				t.Fatalf("bad encoding %q", b)
			}
			log = append(log, b...)
		}
		got := ParseLog(log)
		if len(es) == 0 {
			es = nil
		}
		if len(got) != len(es) {
			t.Fatalf("%d events, want %d", len(got), len(es))
		}
		for i := range es {
			if !reflect.DeepEqual(got[i], es[i]) {
				t.Fatalf("event %d: %+v != %+v", i, got[i], es[i])
			}
		}
	})
}

func TestPropParsePrefix(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		es := rapid.SliceOf(genEvent()).Draw(t, "events")
		var log []byte
		for _, e := range es {
			log = append(log, Encode(e)...)
		}
		cut := rapid.IntRange(0, len(log)).Draw(t, "cut")
		got, all := ParseLog(log[:cut]), ParseLog(log)
		if len(got) > len(all) || !reflect.DeepEqual(got, all[:len(got)]) && len(got) > 0 {
			t.Fatalf("not a prefix: %d of %d", len(got), len(all))
		}
		whole := bytes.Count(log[:cut], []byte("\n"))
		if len(got) != whole {
			t.Fatalf("%d events from %d whole lines", len(got), whole)
		}
	})
}

func TestPropParseNeverPanics(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ParseLog(rapid.SliceOf(rapid.Byte()).Draw(t, "bytes"))
	})
}

func TestPropFoldNeverPanics(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := Fold(rapid.SliceOf(genEvent()).Draw(t, "events"))
		Summarise(r)
		Pending(r)
		for _, n := range r.Notes {
			n.Waiting()
		}
		Message(Pending(r), "s", "e")
	})
}

// A log of valid notes with junk between them keeps every note.
func TestPropNotesNeverDisappear(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var es []Event
		valid := 0
		for i := range rapid.IntRange(0, 8).Draw(t, "count") {
			es = append(es, rapid.SliceOfN(genEvent(), 0, 3).Draw(t, "junk")...)
			// junk may itself hold valid notes under other ids; use a prefix they cannot take.
			es = append(es, genValidNote(t, fmt.Sprintf("valid-%d", i)))
			valid++
		}
		es = append(es, rapid.SliceOfN(genEvent(), 0, 5).Draw(t, "tail")...)
		want := 0
		seen := map[string]bool{}
		for _, e := range es {
			if e.Op == OpNote && e.ID != "" && !seen[e.ID] && foldOK(e) {
				seen[e.ID] = true
				want++
			}
		}
		got := len(Fold(es).Notes)
		if got < valid {
			t.Fatalf("%d notes, at least %d valid were written", got, valid)
		}
		// Junk ids are short; a junk reply can use one before a junk note does, which only lowers this.
		if got > want {
			t.Fatalf("%d notes, only %d note events were valid", got, want)
		}
	})
}

func foldOK(e Event) bool {
	return knownAuthor(e.Author) && !blank(e.Body) && e.Anchor != nil && e.Anchor.valid()
}

func TestPropAgentCannotResolve(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		base := []Event{genValidNote(t, "n1"), genValidNote(t, "n2"), {Op: OpResolve, Note: "n2", Author: User}}
		before := Fold(base)
		agent := rapid.SliceOf(genEvent()).Draw(t, "agent")
		for i := range agent {
			agent[i].Author = Agent
			if agent[i].Op == OpNote || agent[i].Op == OpReply {
				agent[i].ID = "x-" + agent[i].ID // do not collide with the base ids
			}
		}
		after := Fold(append(append([]Event{}, base...), agent...))
		for i, n := range before.Notes {
			if after.Notes[i].Resolved != n.Resolved {
				t.Fatalf("note %s Resolved %v -> %v", n.ID, n.Resolved, after.Notes[i].Resolved)
			}
		}
	})
}

func TestPropPending(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := Fold(rapid.SliceOf(genEvent()).Draw(t, "events"))
		for _, it := range Pending(r) {
			if it.Note.Resolved || len(it.Entries) == 0 {
				t.Fatalf("bad item %+v", it)
			}
			for _, e := range it.Entries {
				if e.Author != User || e.Sent {
					t.Fatalf("bad entry %+v", e)
				}
			}
		}
	})
}

func TestPropSentClearsPending(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		es := rapid.SliceOf(genEvent()).Draw(t, "events")
		var ids []string
		for _, it := range Pending(Fold(es)) {
			for _, e := range it.Entries {
				ids = append(ids, e.ID)
			}
		}
		es = append(es, Event{Op: OpSent, Author: User, IDs: ids, At: t0})
		if p := Pending(Fold(es)); len(p) != 0 {
			t.Fatalf("still pending: %+v", p)
		}
	})
}

func TestPropMessage(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := Fold(rapid.SliceOf(genEvent()).Draw(t, "events"))
		items := Pending(r)
		msg := Message(items, rapid.String().Draw(t, "store"), rapid.String().Draw(t, "exe"))
		// Every block leads with its note's id, which the agent answers
		// with, and carries every line the user wrote.
		for _, it := range items {
			if !strings.Contains(msg, "["+it.Note.ID+"]") {
				t.Fatalf("note %q missing from message", it.Note.ID)
			}
			for _, e := range it.Entries {
				for _, line := range strings.Split(e.Body, "\n") {
					if !strings.Contains(msg, strings.TrimRightFunc(line, unicode.IsSpace)) {
						t.Fatalf("line %q of %s missing from message", line, e.ID)
					}
				}
			}
		}
		blocks := msg
		if i := strings.LastIndex(msg, "\nAnswer each"); i >= 0 {
			blocks = msg[:i]
		}
		if strings.Count(blocks, "\n[") != entries(items) {
			t.Fatalf("expected one block per pending entry:\n%s", msg)
		}
	})
}

func TestPropShellQuote(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.String().Draw(t, "s")
		q := shellQuote(s)
		if q == s {
			if s == "" || strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./-") != "" {
				t.Fatalf("%q left bare", s)
			}
			return
		}
		if !strings.HasPrefix(q, "'") || !strings.HasSuffix(q, "'") {
			t.Fatalf("%q", q)
		}
		// Undo the quoting the way a shell would.
		un := strings.ReplaceAll(q[1:len(q)-1], `'\''`, "'")
		if un != s {
			t.Fatalf("%q -> %q -> %q", s, q, un)
		}
	})
}

func TestPropLocate(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 12).Draw(t, "n")
		var ls []diff.Line
		var texts []string
		for i := 1; i <= n; i++ {
			tx := rapid.SampledFrom([]string{"a", "b", "c"}).Draw(t, "text")
			texts = append(texts, tx)
			ls = append(ls, diff.Line{Kind: diff.Added, New: i, Text: tx})
		}
		f := diff.File{NewPath: "p", Hunks: []diff.Hunk{{Lines: ls}}}
		s := rapid.IntRange(1, n).Draw(t, "s")
		e := rapid.IntRange(s, n).Draw(t, "e")
		a := Anchor{Path: "p", Start: s, End: e, Lines: texts[s-1 : e]}
		// A note on unchanged text is found where it was.
		if got, ok := Locate(a, f); !ok || got != s {
			t.Fatalf("Locate = %d, %v want %d", got, ok, s)
		}
		// Moved anchors land on a run with the same text.
		a.Start = rapid.IntRange(1, 30).Draw(t, "moved")
		a.End = a.Start + e - s
		got, ok := Locate(a, f)
		if !ok {
			t.Fatal("not found, though the text is there")
		}
		if !reflect.DeepEqual(texts[got-1:got-1+len(a.Lines)], a.Lines) {
			t.Fatalf("Locate returned %d, whose text differs", got)
		}
	})
}

func TestPropStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	i := 0
	rapid.Check(t, func(t *rapid.T) {
		i++
		s := Store{Path: filepath.Join(dir, fmt.Sprint(i), "x.jsonl")}
		es := rapid.SliceOfN(genEvent(), 0, 6).Draw(t, "events")
		k := rapid.IntRange(0, len(es)).Draw(t, "split")
		if err := s.Append(es[:k]...); err != nil {
			t.Fatal(err)
		}
		if err := s.Append(es[k:]...); err != nil {
			t.Fatal(err)
		}
		got, err := s.Load()
		if err != nil || len(got) != len(es) {
			t.Fatalf("%d events, want %d, %v", len(got), len(es), err)
		}
	})
}

// entries is how many pending entries items hold, one block each.
func entries(items []Item) int {
	n := 0
	for _, it := range items {
		n += len(it.Entries)
	}
	return n
}

func TestPropIndentKeepsLeading(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		body := rapid.StringMatching(`[ \t]{0,3}[A-Za-z]{1,5}(\n[ \t]{0,3}[A-Za-z]{1,5}){0,3}[ \n]{0,3}`).Draw(t, "body")
		got := indent(body, "  ")
		first := strings.SplitN(body, "\n", 2)[0]
		if !strings.HasPrefix(got, "  "+strings.TrimRight(first, " ")) {
			t.Fatalf("%q -> %q", body, got)
		}
		if got != strings.TrimRight(got, " \n") {
			t.Fatalf("trailing space left in %q", got)
		}
	})
}
