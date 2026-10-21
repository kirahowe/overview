package review

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
)

// Command runs `overview review <sub> ...` for the agent: notes, reply
// and addressed. Everything it writes is by the agent; there is no way to
// resolve, reopen, edit or delete from here.
//
// Flags (only --store) come before the note id. Everything after the id
// is the text, verbatim, so it may start with a dash. With no text after
// the id, reply and addressed read it from in, which lets an agent pass
// markdown through a quoted heredoc that its shell leaves alone. Flag
// errors and usage come back as the error; out gets only results.
func Command(args []string, in io.Reader, out io.Writer, now func() time.Time, rnd io.Reader) error {
	if len(args) == 0 {
		return errors.New("review: need a subcommand: notes, reply or addressed")
	}
	switch args[0] {
	case "notes":
		return cmdNotes(args[1:], out)
	case "reply":
		return cmdReply(args[1:], in, out, now, rnd)
	case "addressed":
		return cmdAddressed(args[1:], in, out, now, rnd)
	}
	return fmt.Errorf("review: unknown subcommand %q: use notes, reply or addressed", args[0])
}

const (
	usageNotes     = "overview review notes --store FILE"
	usageReply     = "overview review reply --store FILE ID [TEXT]  (TEXT is verbatim, or read from stdin)"
	usageAddressed = "overview review addressed --store FILE ID [TEXT]  (TEXT is verbatim, or read from stdin)"
)

// parse reads --store, which must come before the id, then takes the id
// if want says so. rest is everything after the id (or after the flags),
// untouched.
func parse(name, usage string, args []string, wantID bool) (store, id string, rest []string, err error) {
	fs := flag.NewFlagSet("review "+name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&store, "store", "", "path of the review log")
	if err = fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", "", nil, errors.New("usage: " + usage)
		}
		return "", "", nil, fmt.Errorf("review %s: %v\nusage: %s", name, err, usage)
	}
	rest = fs.Args()
	if wantID && len(rest) > 0 {
		id, rest = rest[0], rest[1:]
	}
	if store == "" {
		err = errors.New("review " + name + ": --store is required\nusage: " + usage)
	}
	return
}

// bodyText is the text after the id joined by spaces, or all of in when
// there is none. Trailing whitespace goes; leading indentation stays.
func bodyText(rest []string, in io.Reader) (string, error) {
	text := strings.Join(rest, " ")
	if text == "" && in != nil {
		b, err := io.ReadAll(in)
		if err != nil {
			return "", err
		}
		text = string(b)
	}
	text = strings.TrimRightFunc(text, unicode.IsSpace)
	if strings.TrimSpace(text) == "" {
		return "", nil
	}
	return text, nil
}

func load(store string) (Review, []Event, error) {
	evs, err := Store{Path: store}.Load()
	return Fold(evs), evs, err
}

func cmdNotes(args []string, out io.Writer) error {
	store, _, rest, err := parse("notes", usageNotes, args, false)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("review notes: unexpected argument %q", rest[0])
	}
	r, _, err := load(store)
	if err != nil {
		return err
	}
	_, err = io.WriteString(out, notesText(r, store))
	return err
}

func notesText(r Review, store string) string {
	var b strings.Builder
	open := 0
	for _, n := range r.Notes {
		if n.Resolved {
			continue
		}
		open++
		fmt.Fprintf(&b, "[%s] %s%s\n", n.ID, where(n.Anchor), codeBlock(n.Anchor))
		for _, e := range n.Thread {
			fmt.Fprintf(&b, "  %s, %s:\n%s\n", e.Author, e.At.Format("2006-01-02 15:04"), indent(e.Body, "    "))
		}
		b.WriteString("\n")
	}
	if open == 0 {
		return "No open notes.\n"
	}
	fmt.Fprintf(&b, "Answer with (quoted heredoc, so markdown reaches us untouched):\n"+
		"  overview review reply --store %s <id> <<'EOF'\n  your reply\n  EOF\n"+
		"Then mark it done, with an optional note of what you did:\n"+
		"  overview review addressed --store %s <id> <<'EOF'\n  what you did\n  EOF\n",
		shellQuote(store), shellQuote(store))
	return b.String()
}

// target loads the log and finds the note, with an error naming the id.
func target(store, id string) (Note, Review, []Event, error) {
	r, evs, err := load(store)
	if err != nil {
		return Note{}, r, nil, err
	}
	n, ok := r.Find(id)
	if !ok {
		return Note{}, r, nil, fmt.Errorf("review: no note %q", id)
	}
	return n, r, evs, nil
}

func newID(evs []Event, rnd io.Reader) (string, error) {
	used := map[string]bool{}
	for _, e := range evs {
		used[e.ID] = true
	}
	return NewID(func(s string) bool { return used[s] }, rnd)
}

func cmdReply(args []string, in io.Reader, out io.Writer, now func() time.Time, rnd io.Reader) error {
	store, id, rest, err := parse("reply", usageReply, args, true)
	if err != nil {
		return err
	}
	if id == "" {
		return errors.New("review reply: need a note id and the reply text\nusage: " + usageReply)
	}
	text, err := bodyText(rest, in)
	if err != nil {
		return err
	}
	if text == "" {
		return errors.New("review reply: the reply text is empty (give it after the id or on stdin)")
	}
	n, _, evs, err := target(store, id)
	if err != nil {
		return err
	}
	rid, err := newID(evs, rnd)
	if err != nil {
		return err
	}
	e := Event{Op: OpReply, ID: rid, Note: id, Author: Agent, Body: text, At: now().UTC()}
	if err := (Store{Path: store}).Append(e); err != nil {
		return err
	}
	fmt.Fprintf(out, "Replied to %s (reply %s).\n", id, rid)
	if n.Resolved {
		fmt.Fprintf(out, "Note %s is resolved; the user has not been asked to look again.\n", id)
	}
	return nil
}

func cmdAddressed(args []string, in io.Reader, out io.Writer, now func() time.Time, rnd io.Reader) error {
	store, id, rest, err := parse("addressed", usageAddressed, args, true)
	if err != nil {
		return err
	}
	if id == "" {
		return errors.New("review addressed: need a note id\nusage: " + usageAddressed)
	}
	text, err := bodyText(rest, in)
	if err != nil {
		return err
	}
	n, _, evs, err := target(store, id)
	if err != nil {
		return err
	}
	if n.Resolved {
		return fmt.Errorf("review: note %q is already resolved by the user", id)
	}
	at := now().UTC()
	var es []Event
	if text != "" {
		rid, err := newID(evs, rnd)
		if err != nil {
			return err
		}
		es = append(es, Event{Op: OpReply, ID: rid, Note: id, Author: Agent, Body: text, At: at})
	}
	es = append(es, Event{Op: OpAddressed, Note: id, Author: Agent, At: at})
	if err := (Store{Path: store}).Append(es...); err != nil {
		return err
	}
	fmt.Fprintf(out, "Marked %s addressed.\n", id)
	return nil
}
