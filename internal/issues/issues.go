// Package issues reads the issues of a project from the issues CLI. The
// parsing is pure; the one effect is the Runner that executes the binary,
// which tests replace.
package issues

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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

// Exec is the Runner that runs the issues binary at path. A failed run
// returns an error that carries what the binary said on stderr.
func Exec(path string) Runner {
	return func(args ...string) ([]byte, error) {
		cmd := exec.Command(path, args...)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
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

func (w wire) issue() Issue {
	return Issue{ID: w.ID, Ref: w.Ref, Project: w.Project, Title: w.Title, Status: w.Status,
		Priority: w.Priority, Type: w.Type, Tags: w.Tags, BlockedBy: w.BlockedBy, Details: w.DetailsText}
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
