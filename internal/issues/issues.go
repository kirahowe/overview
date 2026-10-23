// Package issues reads the issues of a project from the issues CLI. The
// parsing is pure; the one effect is the Runner that executes the binary,
// which tests replace.
package issues

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// The statuses an issue moves through.
const (
	Inbox      = "inbox"
	Ready      = "ready"
	InProgress = "in-progress"
	Review     = "review"
	Blocked    = "blocked"
	Done       = "done"
	Dropped    = "dropped"
)

// Open reports whether an issue with this status is still to be worked.
func Open(status string) bool { return status != Done && status != Dropped }

// Issue is one issue of a project. Details is the Markdown of its details
// file, which only Show fills in.
type Issue struct {
	ID        int
	Ref       string // project#id
	Project   string
	Title     string
	Status    string
	Priority  string // p0 to p3
	Type      string
	Tags      []string
	BlockedBy []int
	Details   string
}

// Runner runs the issues binary with args and returns what it printed.
type Runner func(args ...string) ([]byte, error)

// Timeout is how long Exec lets one run of the binary take before it is
// killed.
const Timeout = 3 * time.Second

// Exec is the Runner that runs the issues binary at path, for at most
// Timeout. A failed run returns an error that carries what the binary
// said on stderr.
func Exec(path string) Runner { return execWithin(path, Timeout) }

// execWithin is Exec with a timeout of its own.
func execWithin(path string, timeout time.Duration) Runner {
	return func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, path, args...)
		// A child the binary started may hold its output open after it is
		// killed: stop waiting for it soon after.
		cmd.WaitDelay = timeout / 10
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				err = fmt.Errorf("timed out after %v", timeout)
			}
			if msg := strings.TrimSpace(errb.String()); msg != "" {
				return nil, fmt.Errorf("issues %s: %w: %s", strings.Join(args, " "), err, msg)
			}
			return nil, fmt.Errorf("issues %s: %w", strings.Join(args, " "), err)
		}
		return out.Bytes(), nil
	}
}

// List returns the open issues of the project at root.
func List(run Runner, root string) ([]Issue, error) {
	out, err := run("--dir", root, "list", "--json")
	if err != nil {
		return nil, err
	}
	return ParseList(out)
}

// Show returns issue id of the project at root, with its details.
func Show(run Runner, root string, id int) (Issue, error) {
	out, err := run("--dir", root, "show", strconv.Itoa(id), "--json")
	if err != nil {
		return Issue{}, err
	}
	return ParseShow(out)
}

// wire is an issue as the CLI prints it. Fields it prints that overview
// does not use are ignored.
type wire struct {
	ID          int      `json:"id"`
	Ref         string   `json:"ref"`
	Project     string   `json:"project"`
	Title       string   `json:"title"`
	Status      string   `json:"status"`
	Priority    string   `json:"priority"`
	Type        string   `json:"type"`
	Tags        []string `json:"tags"`
	BlockedBy   []int    `json:"blocked-by"`
	DetailsText string   `json:"details-text"`
}

// issue is the issue w describes, its text cleaned: whatever the files
// hold, a field is shown on one line, and nothing in it reaches the
// terminal as a control character.
func (w wire) issue() Issue {
	var tags []string
	for _, t := range w.Tags {
		tags = append(tags, OneLine(t))
	}
	return Issue{ID: w.ID, Ref: OneLine(w.Ref), Project: OneLine(w.Project), Title: OneLine(w.Title), Status: OneLine(w.Status),
		Priority: OneLine(w.Priority), Type: OneLine(w.Type), Tags: tags, BlockedBy: w.BlockedBy, Details: Text(w.DetailsText)}
}

// OneLine is s on one line: control characters, newlines among them, are
// removed, and each run of white space is one space, with none at either
// end.
func OneLine(s string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)), " ")
}

// Text is s with its lines kept: a tab is a space, and every other control
// character but the newline is removed.
func Text(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
}

// ParseList reads the output of list --json.
func ParseList(b []byte) ([]Issue, error) {
	var doc struct {
		Issues []wire `json:"issues"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("issues list: %w", err)
	}
	out := make([]Issue, len(doc.Issues))
	for i, w := range doc.Issues {
		out[i] = w.issue()
	}
	return out, nil
}

// ParseShow reads the output of show --json.
func ParseShow(b []byte) (Issue, error) {
	var w wire
	if err := json.Unmarshal(b, &w); err != nil {
		return Issue{}, fmt.Errorf("issues show: %w", err)
	}
	return w.issue(), nil
}

// ProjectID extracts the :id string from the content of a project.edn. It
// is not an EDN parser: it skips strings and comments, so an :id inside a
// value does not count, and takes the first :id key that has a string.
func ProjectID(edn string) (string, bool) {
	for i := 0; i < len(edn); {
		switch c := edn[i]; {
		case c == '"':
			_, i = readString(edn, i)
		case c == ';':
			for i < len(edn) && edn[i] != '\n' {
				i++
			}
		case c == ':':
			j := i + 1
			for j < len(edn) && !strings.ContainsRune(" \t\r\n,{}[]()\";", rune(edn[j])) {
				j++
			}
			if edn[i:j] != ":id" {
				i = j
				continue
			}
			for j < len(edn) && strings.ContainsRune(" \t\r\n,", rune(edn[j])) {
				j++
			}
			if j < len(edn) && edn[j] == '"' {
				id, _ := readString(edn, j)
				return id, true
			}
			i = j
		default:
			i++
		}
	}
	return "", false
}

// readString reads the string literal that opens at s[i] and returns its
// value and the index after it. An unterminated string runs to the end.
func readString(s string, i int) (string, int) {
	var b strings.Builder
	for i++; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			return b.String(), i + 1
		case '\\':
			if i++; i >= len(s) {
				return b.String(), i
			}
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(s[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), i
}

// Criterion is one checklist item of an issue's acceptance criteria.
type Criterion struct {
	Text string
	Done bool
}

var (
	headingRe = regexp.MustCompile(`^(#{1,6})[ \t]+(.*?)[ \t]*#*[ \t]*$`)
	itemRe    = regexp.MustCompile(`^[ \t]*[-*+][ \t]+\[([ xX])\][ \t]*(.*)$`)
	bulletRe  = regexp.MustCompile(`^[ \t]*([-*+]|\d+[.)])[ \t]`)
)

// Criteria returns the checklist items under the "Acceptance criteria"
// heading of details, of any level and case. The section ends at the next
// heading of the same or a higher level. A line that follows an item and
// is neither blank, a heading nor another list item continues it.
func Criteria(details string) []Criterion {
	var out []Criterion
	level, last := 0, -1 // the section's heading level, 0 outside one; the open item
	for _, line := range strings.Split(details, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := headingRe.FindStringSubmatch(line); m != nil {
			last = -1
			switch {
			case strings.EqualFold(strings.TrimSpace(m[2]), "acceptance criteria"):
				level = len(m[1])
			case level > 0 && len(m[1]) <= level:
				level = 0
			}
			continue
		}
		if level == 0 {
			continue
		}
		switch m := itemRe.FindStringSubmatch(line); {
		case m != nil:
			out = append(out, Criterion{Text: strings.TrimSpace(m[2]), Done: m[1] != " "})
			last = len(out) - 1
		case strings.TrimSpace(line) == "" || bulletRe.MatchString(line):
			last = -1
		case last >= 0:
			out[last].Text = strings.TrimSpace(out[last].Text + " " + strings.TrimSpace(line))
		}
	}
	return out
}

// Rank orders statuses for display, lowest first: what waits on you,
// then what is moving, then what is not started, then what is finished.
func Rank(status string) int {
	switch status {
	case Review:
		return 0
	case InProgress:
		return 1
	case Blocked:
		return 2
	case Ready:
		return 3
	case Inbox:
		return 4
	case Done:
		return 5
	case Dropped:
		return 6
	}
	return 7
}

// PriorityRank is the number in p0 to p3, and 4 for anything else.
func PriorityRank(p string) int {
	if len(p) == 2 && p[0] == 'p' && p[1] >= '0' && p[1] <= '3' {
		return int(p[1] - '0')
	}
	return 4
}

// HasTracker reports whether the project at root has a .issues directory,
// as exists, os.Stat outside tests, tells.
func HasTracker(exists func(string) bool, root string) bool {
	return exists(filepath.Join(root, ".issues"))
}
