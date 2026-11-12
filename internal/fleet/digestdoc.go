package fleet

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DigestDoc is a digest read from its Markdown: when it looked at the
// fleet, and its sections of items. ParseDigest reads any text into one,
// so a digest a model wrote loosely still shows whole.
type DigestDoc struct {
	// Stamp is the "# " heading Publish puts first, without the hash.
	Stamp    string
	Sections []DigestSection
}

// DigestSection is a heading and the items under it. Title is "" for
// what comes before the first heading.
type DigestSection struct {
	Title string
	Items []DigestItem
}

// DigestKind is what a section holds, which its title says.
type DigestKind int

const (
	DigestOther DigestKind = iota
	DigestNeeds
	DigestFinished
	DigestProgress
)

// Kind reads the section's kind from how its title starts, so "Needs
// your attention" and "Needs you" are the same section.
func (s DigestSection) Kind() DigestKind {
	t := strings.ToLower(s.Title)
	switch {
	case strings.HasPrefix(t, "needs"):
		return DigestNeeds
	case strings.HasPrefix(t, "finished"), strings.HasPrefix(t, "done"):
		return DigestFinished
	case strings.HasPrefix(t, "in progress"):
		return DigestProgress
	}
	return DigestOther
}

// DigestItem is one thing a digest reports. Names are the sessions it is
// about and Project where they run. Issue says what it is in a line. For
// an item that waits on the reader, Do is what they are asked to do, Why
// the reason when the issue does not give it, and Since when the wait
// began. Notes are its other lines. Text outside any bullet is an item
// with no names, its text in Issue.
type DigestItem struct {
	Names   []string
	Project string
	Issue   string
	Do      string
	Why     string
	Since   string
	Notes   []string
}

var (
	// digestHead is how an item starts: its names in bold, with commas
	// or "and" between them, then the project in parentheses and a colon,
	// either of which may be missing.
	digestHead = regexp.MustCompile(`^\*\*[^*]+\*\*(?:\s*(?:,|and\b|&)\s*\*\*[^*]+\*\*)*\s*(?:\(([^()]*)\))?\s*:?\s*`)
	digestName = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	// digestField is the label a line under an item starts with, bold or
	// not. "Waiting since" needs no colon, as older digests wrote it.
	digestField = regexp.MustCompile(`(?i)^(?:\*\*)?(do|why|since)(?:\*\*)?\s*:(?:\*\*)?\s*|^(waiting since)\b:?\s*`)
)

// digestBullet is the text of a list item, and whether t is one.
func digestBullet(t string) (string, bool) {
	for _, m := range []string{"- ", "* ", "+ ", "• "} {
		if strings.HasPrefix(t, m) {
			return strings.TrimSpace(t[len(m):]), true
		}
	}
	return "", false
}

// digestHeading is the title of a Markdown heading, and whether t is one.
func digestHeading(t string) (string, bool) {
	rest := strings.TrimLeft(t, "#")
	if rest == t || (rest != "" && rest[0] != ' ') {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// ParseDigest reads a digest's Markdown. A bullet at the left edge starts
// an item, and the indented bullets under it are its fields: "Do:",
// "Why:" and "Since:" by their labels, and anything else a note. A line
// with one of those labels is a field of the item above it wherever it
// stands, as a model does not always indent. Any other line that is no
// bullet continues the one before it, and after a blank line stands as
// text of its own.
//
// An item written as one paragraph, as digests were before items had a
// line for what to do, gives its first sentence as the issue and the
// rest as a note, so its start can still be told from its detail.
func ParseDigest(md string) DigestDoc {
	var doc DigestDoc
	var sec *DigestSection
	var item *DigestItem
	// last is the text a line that is no bullet continues, nil after a
	// blank line or a heading.
	var last *string
	section := func(title string) {
		doc.Sections = append(doc.Sections, DigestSection{Title: title})
		sec, item, last = &doc.Sections[len(doc.Sections)-1], nil, nil
	}
	add := func(it DigestItem) {
		if sec == nil {
			section("")
		}
		sec.Items = append(sec.Items, it)
		item = &sec.Items[len(sec.Items)-1]
		last = &item.Issue
	}
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimSpace(line)
		indented := t != "" && (line[0] == ' ' || line[0] == '\t')
		body, bullet := digestBullet(t)
		if !bullet {
			body = t
		}
		title, heading := digestHeading(t)
		switch {
		case t == "":
			last = nil
		case heading && strings.HasPrefix(t, "# ") && doc.Stamp == "" && len(doc.Sections) == 0:
			doc.Stamp = title
		case heading:
			section(title)
		case item != nil && (bullet && indented || digestField.MatchString(body)):
			last = item.field(body)
		case bullet:
			add(digestHeadOf(body))
		case last != nil:
			*last = strings.TrimSpace(*last + " " + t)
		default:
			add(DigestItem{Issue: t})
		}
	}
	for i := range doc.Sections {
		for j := range doc.Sections[i].Items {
			it := &doc.Sections[i].Items[j]
			it.Since = strings.TrimRight(it.Since, ". ")
			if len(it.Names) == 0 || it.Do != "" || it.Why != "" || len(it.Notes) > 0 {
				continue
			}
			if first, rest := firstSentence(it.Issue); rest != "" {
				it.Issue, it.Notes = first, []string{rest}
			}
		}
	}
	return doc
}

// digestHeadOf reads an item's first line: its names and project, when it
// starts with them, and the issue after.
func digestHeadOf(body string) DigestItem {
	loc := digestHead.FindStringSubmatchIndex(body)
	if loc == nil {
		return DigestItem{Issue: body}
	}
	it := DigestItem{Issue: body[loc[1]:]}
	head := body[:loc[1]]
	if loc[2] >= 0 {
		it.Project = strings.TrimSpace(body[loc[2]:loc[3]])
		head = body[:loc[2]]
	}
	for _, m := range digestName.FindAllStringSubmatch(head, -1) {
		if name := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(m[1]), ":")); name != "" {
			it.Names = append(it.Names, name)
		}
	}
	return it
}

// field files a line under the item by its label, and returns where a
// line continuing it goes.
func (it *DigestItem) field(body string) *string {
	loc := digestField.FindStringSubmatchIndex(body)
	if loc == nil {
		it.Notes = append(it.Notes, body)
		return &it.Notes[len(it.Notes)-1]
	}
	label, text := "since", body[loc[1]:]
	if loc[2] >= 0 {
		label = strings.ToLower(body[loc[2]:loc[3]])
	}
	to := &it.Since
	switch label {
	case "do":
		to = &it.Do
	case "why":
		to = &it.Why
	}
	*to = strings.TrimSpace(*to + " " + text)
	return to
}

// firstSentence cuts s after its first sentence: at a full stop, question
// mark or exclamation mark outside a code span that a space and then a
// capital, a digit or a code span follows. rest is "" when s is one
// sentence.
func firstSentence(s string) (first, rest string) {
	code := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '`':
			code = !code
		case '.', '?', '!':
			if code || i+2 >= len(s) || s[i+1] != ' ' {
				continue
			}
			if r, _ := utf8.DecodeRuneInString(s[i+2:]); unicode.IsUpper(r) || unicode.IsDigit(r) || r == '`' {
				return s[:i+1], strings.TrimSpace(s[i+2:])
			}
		}
	}
	return s, ""
}
