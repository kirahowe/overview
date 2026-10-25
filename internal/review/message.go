package review

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxCodeLines = 12
	maxQuote     = 100
)

// shellQuote quotes s for a POSIX shell, only when it needs it.
func shellQuote(s string) string {
	safe := s != ""
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '_' || r == '.' || r == '/' || r == '-') {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// where describes an anchor: "internal/x.go, new lines 3-4".
func where(a Anchor) string {
	if a.End > a.Start {
		return fmt.Sprintf("%s, %s lines %d-%d", a.Path, a.Side, a.Start, a.End)
	}
	return fmt.Sprintf("%s, %s line %d", a.Path, a.Side, a.Start)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// oneLine squeezes text onto one line of at most maxQuote characters.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= maxQuote {
		return s
	}
	return string([]rune(s)[:maxQuote-1]) + "…"
}

func indent(s, pad string) string {
	// A carriage return pasted into a prompt acts as Enter or overwrites
	// the line, so every line ending becomes a newline. Keep leading
	// indentation (a code snippet's first line); drop leading blank
	// lines and trailing space.
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	s = strings.TrimRightFunc(strings.TrimLeft(s, "\r\n"), unicode.IsSpace)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(pad+l, " ")
	}
	return strings.Join(lines, "\n")
}

func codeBlock(a Anchor) string {
	shown := a.Lines
	if len(shown) > maxCodeLines {
		shown = shown[:maxCodeLines]
	}
	width := len(fmt.Sprint(a.Start + len(shown) - 1))
	var b strings.Builder
	for i, l := range shown {
		// A CRLF file's lines end in a carriage return; see indent.
		l = strings.ReplaceAll(l, "\r", "")
		fmt.Fprintf(&b, "\n    %*d  %s", width, a.Start+i, l)
	}
	if len(a.Lines) > len(shown) {
		b.WriteString("\n    …")
	}
	return b.String()
}

// previous is the entry before the one with the given id, if any.
func previous(n Note, id string) (Entry, bool) {
	for i := 1; i < len(n.Thread); i++ {
		if n.Thread[i].ID == id {
			return n.Thread[i-1], true
		}
	}
	return Entry{}, false
}

// Message is the text the viewer pastes into the agent's session: the
// items' new notes and replies, and how to answer them. store is the log
// path and exe the program to run, both quoted for a shell.
func Message(items []Item, store, exe string) string {
	var notes, replies int
	var blocks []string
	for _, it := range items {
		for _, e := range it.Entries {
			if e.ID == it.Note.ID {
				notes++
				blocks = append(blocks, noteBlock(it.Note, e))
			} else {
				replies++
				blocks = append(blocks, replyBlock(it.Note, e))
			}
		}
	}
	var parts []string
	if notes > 0 {
		parts = append(parts, plural(notes, "new note", "new notes"))
	}
	if replies > 0 {
		parts = append(parts, plural(replies, "reply", "replies"))
	}
	head := "Review notes on your changes: " + strings.Join(parts, ", ") + "."
	if len(parts) == 0 {
		head = "Review notes on your changes: none."
	}
	cmd := shellQuote(exe) + " review %s --store " + shellQuote(store)
	var b strings.Builder
	b.WriteString(head + "\n\n")
	for _, bl := range blocks {
		b.WriteString(bl + "\n\n")
	}
	b.WriteString("Answer each in its thread, then make any changes they ask for:\n")
	b.WriteString("Put your text in a quoted heredoc so your shell leaves backticks and $VARS alone:\n")
	fmt.Fprintf(&b, "  "+cmd+" <id> <<'EOF'\n  your reply\n  EOF\n", "reply")
	fmt.Fprintf(&b, "  "+cmd+" <id> <<'EOF'\n  what you did (optional)\n  EOF\n", "addressed")
	fmt.Fprintf(&b, "  "+cmd+"     every open note with its thread\n", "notes")
	b.WriteString("Notes are never deleted, and only the user resolves them.\n")
	return b.String()
}

func noteBlock(n Note, e Entry) string {
	return fmt.Sprintf("[%s] %s%s\n%s", e.ID, where(n.Anchor), codeBlock(n.Anchor), indent(e.Body, "  "))
}

// replyBlock leads with the note's id, not the reply's, since every
// command the agent answers with takes a note's.
func replyBlock(n Note, e Entry) string {
	s := fmt.Sprintf("[%s] the user replied on %s\n", n.ID, where(n.Anchor))
	if p, ok := previous(n, e.ID); ok && p.Author == Agent {
		s += "  > " + oneLine(p.Body) + "\n"
	}
	return s + indent(e.Body, "  ")
}
