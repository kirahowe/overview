package tui

import (
	"strconv"

	tea "github.com/charmbracelet/bubbletea"

	"overview/internal/fleet"
	"overview/internal/review"
	"overview/internal/tmux"
)

// A session's review runs as overview review view in a split above the
// session's own pane, marked with the session's ID so the listing can
// tell it, and so v finds it again rather than opening another.

// reviewShare is how tall a review starts, of the session's window.
const reviewShare = "45%"

// reviewOpenedMsg answers openReview's Cmd for session id.
type reviewOpenedMsg struct {
	id   string
	pane tmux.Pane
	err  error
}

// reviewPane is the review open for a session, "" for none.
func reviewPane(panes []tmux.Pane, sessionID string) string {
	for _, p := range panes {
		if p.Review == sessionID && sessionID != "" {
			return p.ID
		}
	}
	return ""
}

// reviewCommand is the command line of a session's review.
func reviewCommand(exe, store, target string, r fleet.Row, flags []string) []string {
	cmd := []string{exe, "review", "view", "--store", store, "--dir", r.Cwd, "--target", target,
		"--title", r.Name, "--transcript", r.Transcript}
	if r.StartedAt > 0 {
		cmd = append(cmd, "--started", strconv.FormatInt(r.StartedAt/1000, 10))
	}
	return append(cmd, flags...)
}

// openReview moves the keyboard into a session's review, opening one
// above the session when it has none.
func (m model) openReview(r fleet.Row) (model, tea.Cmd) {
	if m.mux == nil {
		return m.say("reviews open only inside overview's tmux"), nil
	}
	if p := reviewPane(m.panes, r.SessionID); p != "" {
		m.entering = p
		return m, nil
	}
	if m.opening[r.SessionID] {
		return m.say("the review of " + r.Name + " is opening"), nil
	}
	target := m.paneOf(r)
	if target == "" {
		return m.say("a review opens above a session that runs in overview's tmux"), nil
	}
	store, err := review.PathFor(m.home.ReviewsDir(), r.SessionID)
	if err != nil {
		return m.say(err.Error()), nil
	}
	cmd := reviewCommand(m.exe, store, target, r, m.reviewArgs)
	mux, id := m.mux, r.SessionID
	m.spawns++
	m.opening = withKey(m.opening, id, true)
	return m.say("opening the review of " + r.Name + "…"), func() tea.Msg {
		p, err := mux.Split(target, r.Cwd, reviewShare, nil, cmd...)
		if err != nil {
			return reviewOpenedMsg{id, tmux.Pane{}, err}
		}
		p.Review = id
		return reviewOpenedMsg{id, p, mux.SetPaneOption(p.ID, tmux.ReviewOption, id)}
	}
}

// withKey is a copy of mp with k set to v, or removed when v is false,
// so a map an older model holds never changes under it.
func withKey(mp map[string]bool, k string, v bool) map[string]bool {
	out := make(map[string]bool, len(mp)+1)
	for kk, vv := range mp {
		out[kk] = vv
	}
	if v {
		out[k] = true
	} else {
		delete(out, k)
	}
	return out
}

// readReviews sums up the review of each of rows' sessions that has one.
func readReviews(home fleet.Home, rows []fleet.Row) map[string]review.Summary {
	out := map[string]review.Summary{}
	for _, r := range rows {
		path, err := review.PathFor(home.ReviewsDir(), r.SessionID)
		if err != nil {
			continue
		}
		es, err := review.Store{Path: path}.Load()
		if err != nil || len(es) == 0 {
			continue
		}
		out[r.SessionID] = review.Summarise(review.Fold(es))
	}
	return out
}

// reviewBadge is what a session's row says about its review: how many
// notes are open, and that a reply waits when one does.
func reviewBadge(s review.Summary) string {
	switch {
	case s.Waiting > 0:
		return "✎" + strconv.Itoa(s.Open) + " reply"
	case s.Open > 0:
		return "✎" + strconv.Itoa(s.Open)
	}
	return ""
}
