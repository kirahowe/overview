// Package review is the record of a review of a session's changes: an
// append-only log of notes and replies that the user and the agent both
// write, folded into notes with threads. Notes are never deleted, and only
// the user resolves them. Everything here is pure except Store.
package review

import (
	"fmt"
	"strings"
	"time"
)

// Side says which side of the diff a note's lines are on.
type Side uint8

const (
	New Side = iota // the changed file; the zero value
	Old             // the file before the change
)

// String is the side's name in the log.
func (s Side) String() string {
	if s == Old {
		return "old"
	}
	return "new"
}

// MarshalText writes the side as "new" or "old".
func (s Side) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// UnmarshalText reads "new" or "old".
func (s *Side) UnmarshalText(b []byte) error {
	switch string(b) {
	case "new":
		*s = New
	case "old":
		*s = Old
	default:
		return fmt.Errorf("review: unknown side %q", b)
	}
	return nil
}

// Anchor is the lines a note is about, with their text when the note was
// written, so the note can be found again after the diff changes.
type Anchor struct {
	Path  string   `json:"path"`
	Side  Side     `json:"side"`
	Start int      `json:"start"` // first line, from 1
	End   int      `json:"end"`   // last line, inclusive
	Lines []string `json:"lines"` // the text of lines Start..End
}

// valid says whether the anchor is well formed.
func (a Anchor) valid() bool {
	return a.Path != "" && a.Start >= 1 && a.End >= a.Start &&
		a.End-a.Start < 1<<24 && len(a.Lines) == a.End-a.Start+1
}

// Author is who wrote an event.
type Author string

const (
	User  Author = "user"
	Agent Author = "agent"
)

// Op is what an event does.
type Op string

const (
	OpNote      Op = "note"
	OpReply     Op = "reply"
	OpEdit      Op = "edit"
	OpResolve   Op = "resolve"
	OpReopen    Op = "reopen"
	OpAddressed Op = "addressed"
	OpSent      Op = "sent"
)

// Event is one line of the log. ID is the new note's or reply's id, and
// for an edit the entry edited. Note is the note a reply, resolve, reopen
// or addressed is about. IDs lists what a sent event delivered.
type Event struct {
	Op     Op        `json:"op"`
	ID     string    `json:"id,omitempty"`
	Note   string    `json:"note,omitempty"`
	Author Author    `json:"author"`
	Body   string    `json:"body,omitempty"`
	Anchor *Anchor   `json:"anchor,omitempty"`
	IDs    []string  `json:"ids,omitempty"`
	At     time.Time `json:"at"`
}

// Entry is one message in a note's thread.
type Entry struct {
	ID     string
	Author Author
	Body   string
	At     time.Time
	Sent   bool
}

// Note is an anchored comment with its replies.
type Note struct {
	ID        string
	Anchor    Anchor
	Thread    []Entry // Thread[0] is the note itself, then the replies
	Resolved  bool
	Addressed bool // the agent's claim; cleared by a user reply or a reopen
}

// Waiting says the note is open and the agent spoke last, so the user has
// something to read.
func (n Note) Waiting() bool {
	if n.Resolved || len(n.Thread) == 0 {
		return false
	}
	return n.Thread[len(n.Thread)-1].Author == Agent
}

// Review is every note, in the order they were written.
type Review struct{ Notes []Note }

// Find returns the note with the given id.
func (r Review) Find(id string) (Note, bool) {
	for _, n := range r.Notes {
		if n.ID == id {
			return n, true
		}
	}
	return Note{}, false
}

// at locates an entry: the note index and the thread index.
type at struct{ note, entry int }

// Fold reads the events in order into a review. An event that breaks the
// rules is ignored, so a damaged or hostile log still folds.
func Fold(events []Event) Review {
	var r Review
	pos := map[string]at{}
	for _, e := range events {
		switch e.Op {
		case OpNote:
			foldNote(&r, pos, e)
		case OpReply:
			foldReply(&r, pos, e)
		case OpEdit:
			foldEdit(&r, pos, e)
		case OpResolve, OpReopen:
			foldResolve(&r, e)
		case OpAddressed:
			foldAddressed(&r, e)
		case OpSent:
			foldSent(&r, pos, e)
		}
	}
	return r
}

func blank(s string) bool { return strings.TrimSpace(s) == "" }

func entryOf(e Event) Entry {
	return Entry{ID: e.ID, Author: e.Author, Body: e.Body, At: e.At}
}

func foldNote(r *Review, pos map[string]at, e Event) {
	if !knownAuthor(e.Author) || e.ID == "" || blank(e.Body) || e.Anchor == nil || !e.Anchor.valid() {
		return
	}
	if _, used := pos[e.ID]; used {
		return
	}
	a := *e.Anchor
	a.Lines = append([]string(nil), a.Lines...)
	pos[e.ID] = at{len(r.Notes), 0}
	r.Notes = append(r.Notes, Note{ID: e.ID, Anchor: a, Thread: []Entry{entryOf(e)}})
}

func knownAuthor(a Author) bool { return a == User || a == Agent }

func noteIndex(r *Review, pos map[string]at, id string) (int, bool) {
	w, ok := pos[id]
	if !ok || w.entry != 0 {
		return 0, false
	}
	return w.note, true
}

func foldReply(r *Review, pos map[string]at, e Event) {
	if !knownAuthor(e.Author) || e.ID == "" || blank(e.Body) {
		return
	}
	if _, used := pos[e.ID]; used {
		return
	}
	i, ok := noteIndex(r, pos, e.Note)
	if !ok {
		return
	}
	n := &r.Notes[i]
	pos[e.ID] = at{i, len(n.Thread)}
	n.Thread = append(n.Thread, entryOf(e))
	if e.Author == User {
		n.Addressed = false
		n.Resolved = false
	}
}

func foldEdit(r *Review, pos map[string]at, e Event) {
	w, ok := pos[e.ID]
	if !ok || blank(e.Body) {
		return
	}
	en := &r.Notes[w.note].Thread[w.entry]
	if en.Author != e.Author || en.Sent {
		return
	}
	en.Body = e.Body
}

// byID finds a note by id without the index map.
func byID(r *Review, id string) *Note {
	for i := range r.Notes {
		if r.Notes[i].ID == id {
			return &r.Notes[i]
		}
	}
	return nil
}

func foldResolve(r *Review, e Event) {
	n := byID(r, e.Note)
	if n == nil || e.Author != User {
		return
	}
	if e.Op == OpResolve {
		n.Resolved = true
		return
	}
	n.Resolved = false
	n.Addressed = false
}

func foldAddressed(r *Review, e Event) {
	n := byID(r, e.Note)
	if n == nil || e.Author != Agent || n.Resolved {
		return
	}
	n.Addressed = true
}

func foldSent(r *Review, pos map[string]at, e Event) {
	if e.Author != User {
		return
	}
	for _, id := range e.IDs {
		if w, ok := pos[id]; ok {
			r.Notes[w.note].Thread[w.entry].Sent = true
		}
	}
}

// Item is a note with the user's entries on it that are not yet sent.
type Item struct {
	Note    Note
	Entries []Entry
}

// Pending lists the open notes that have user entries not yet sent, in
// note order. The entries are the note itself and the user's replies.
func Pending(r Review) []Item {
	var items []Item
	for _, n := range r.Notes {
		if n.Resolved {
			continue
		}
		var es []Entry
		for _, e := range n.Thread {
			if e.Author == User && !e.Sent {
				es = append(es, e)
			}
		}
		if len(es) > 0 {
			items = append(items, Item{Note: n, Entries: es})
		}
	}
	return items
}

// Summary counts a review for display.
type Summary struct{ Open, Waiting, Unsent int }

// Summarise counts the open notes, those waiting on the user, and those
// with something unsent.
func Summarise(r Review) Summary {
	s := Summary{Unsent: len(Pending(r))}
	for _, n := range r.Notes {
		if !n.Resolved {
			s.Open++
		}
		if n.Waiting() {
			s.Waiting++
		}
	}
	return s
}
