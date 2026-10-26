package tui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"overview/internal/issues"
)

// verdictPrompt is m's box, giving an issue a verdict: a closes it as
// done, c sends it back with a note and starts a session on the change,
// and r drops it. It asks for the verdict first and then for a note, so
// two keys and enter stand between m and an issue that has gone. c is
// offered only for an issue in review, the one that has work to send
// back. The zero value is no box.
type verdictPrompt struct {
	root  string // the tracker's root, where the issues CLI runs
	ref   string
	issue issues.Issue
	kind  string // "" while the verdict is being chosen, else issues.Approve, Changes or Reject
	note  string
}

func (v verdictPrompt) open() bool { return v.root != "" }

// verdictMsg is how a verdict given through the issues CLI went.
type verdictMsg struct {
	v   verdictPrompt
	err error
}

// verdictKind is the verdict key k chooses for an issue with status, ""
// for none.
func verdictKind(k, status string) string {
	switch {
	case k == "a":
		return issues.Approve
	case k == "c" && status == issues.Review:
		return issues.Changes
	case k == "r":
		return issues.Reject
	}
	return ""
}

// verdictWord is what a verdict is called for an issue with status: in
// review it is a verdict on finished work, and anywhere else it only
// closes the issue.
func verdictWord(kind, status string) string {
	review := status == issues.Review
	switch {
	case kind == issues.Approve && review:
		return "approve"
	case kind == issues.Approve:
		return "done"
	case kind == issues.Changes:
		return "send back"
	case review:
		return "reject"
	}
	return "drop"
}

// verdictKeys translates a key while the box is open. Choosing, a verdict's
// key picks it and any other key cancels, as the stop box does. Typing the
// note, enter gives the verdict and esc cancels.
func (m model) verdictKeys(msg tea.KeyMsg) []action {
	v := m.verdict
	if v.kind == "" {
		if kind := verdictKind(msg.String(), v.issue.Status); kind != "" {
			return []action{{do: verdictPick, text: kind}}
		}
		return do(verdictCancel)
	}
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		return do(verdictCancel)
	case tea.KeyEnter:
		return do(verdictSend)
	case tea.KeyBackspace:
		return do(verdictBack)
	case tea.KeyRunes, tea.KeySpace:
		return []action{{do: verdictType, text: printable(string(msg.Runes))}}
	}
	return nil
}

// markIssue opens the box for the issue under the issues card's cursor,
// while the keyboard is in that card, or else for the selected
// workstream's.
func (m model) markIssue(e entry, ok bool) model {
	if m.issues == nil {
		return m.say("overview was started without the issues tool")
	}
	switch {
	case m.inIssues():
		t, i, found := m.picked()
		if !found {
			return m.say("no issue to mark")
		}
		m.verdict = verdictPrompt{root: t.root, ref: m.issueRef(t.root, i), issue: i}
	case ok && e.stream != nil:
		m.verdict = verdictPrompt{root: e.stream.Root, ref: e.stream.Ref(), issue: e.stream.Issue}
	default:
		return m.say("m marks a task's issue: select a task, or an issue in the cockpit")
	}
	return m
}

// issueRef names issue i of the tracker at root as the CLI does.
func (m model) issueRef(root string, i issues.Issue) string {
	if i.Ref != "" {
		return i.Ref
	}
	return m.world().IssuesID(root) + "#" + strconv.Itoa(i.ID)
}

// sendVerdict gives the verdict through the issues CLI, in a Cmd. Changes
// needs a note, and the box stays open until it has one.
func (m model) sendVerdict() (model, tea.Cmd) {
	v := m.verdict
	v.note = strings.TrimSpace(v.note)
	if v.kind == issues.Changes && v.note == "" {
		return m.say("say what to change: the note goes to the session that makes it"), nil
	}
	m.verdict = verdictPrompt{}
	run := m.issues
	return m.say("marking " + v.ref + "…"), func() tea.Msg {
		return verdictMsg{v: v, err: issues.Verdict(run, v.root, v.issue.ID, v.kind, v.note)}
	}
}

// landVerdict reports a verdict and reads the trackers again, so the
// issue moves in the list. Changes then starts a session on the issue
// with the note, as enter in the issues card does, once the cockpit has
// closed.
func (m model) landVerdict(msg verdictMsg) (model, tea.Cmd) {
	v := msg.v
	if msg.err != nil {
		return m.say("couldn't mark " + v.ref + ": " + msg.err.Error()), nil
	}
	if v.kind != issues.Changes {
		word := map[string]string{"approve": "approved", "done": "closed", "reject": "rejected", "drop": "dropped"}
		return m.say(word[verdictWord(v.kind, v.issue.Status)] + " " + v.ref), m.refresh
	}
	var out tea.Cmd
	if m.cockpit.open {
		m, out = m.closeCockpit()
	}
	m, cmd := m.startChanges(v.root, v.issue, v.note)
	return m, tea.Batch(out, cmd, m.refresh)
}

// changesPrompt is the opening prompt of a session on changes asked for
// in issue i: the opening that links it to the issue, and the note.
func changesPrompt(projectID string, i issues.Issue, note string) string {
	return opening(projectID, i) + "\n\nChanges requested in review: " + note
}

// verdictBox is the box m opens.
func (m model) verdictBox() []string {
	v := m.verdict
	w := min(m.w-4, 60)
	in := w - 4
	title := bold.Foreground(pal.Text).Render(ansi.Truncate(v.ref+" "+v.issue.Title, in, "…"))
	status := v.issue.Status
	if v.kind == "" {
		body := []string{title, dim.Render(status), ""}
		keys := []string{fg(pal.Accent).Render("a") + dim.Render(" "+verdictWord(issues.Approve, status))}
		if status == issues.Review {
			keys = append(keys, fg(pal.Accent).Render("c")+dim.Render(" "+verdictWord(issues.Changes, status)))
		}
		keys = append(keys, red.Render("r")+dim.Render(" "+verdictWord(issues.Reject, status)))
		body = append(body, strings.Join(keys, "   "), dim.Render("any other key cancels"))
		return box("mark issue", "", body, w, len(body)+2, true)
	}
	what := map[string]string{
		issues.Approve: "Closes it as done.",
		issues.Changes: "Sends it back to ready and starts a session on it with the note.",
		issues.Reject:  "Drops it.",
	}[v.kind]
	label := "note, optional: "
	if v.kind == issues.Changes {
		label = "what to change: "
		what += " The note also goes in its details log."
	} else {
		what += " A note goes in its details log."
	}
	body := []string{title}
	body = append(body, wrap(dim.Render(what), in)...)
	body = append(body, "", fitLine(dim.Render(label), v.note, in))
	word := verdictWord(v.kind, status)
	body = append(body, "", fg(pal.Accent).Render("enter")+dim.Render(" "+word+"   ")+fg(pal.Accent).Render("esc")+dim.Render(" cancel"))
	return box(word+" "+v.ref, "", body, w, len(body)+2, true)
}

// fitLine is label, styled already, then typed text with a caret, keeping
// the text's end when the line is too short for all of it.
func fitLine(label, text string, w int) string {
	room := max(w-ansi.StringWidth(label)-1, 1)
	return label + fg(pal.Text).Render(keepEnd(text, room)) + fg(pal.Accent).Render("▏")
}
