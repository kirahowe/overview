package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Live is one running session as Claude Code itself lists it.
type Live struct {
	PID int `json:"pid"`
	// ID is the short id that attach, logs and stop take. Only background
	// sessions have one.
	ID        string `json:"id"`
	Cwd       string `json:"cwd"`
	Kind      string `json:"kind"`
	StartedAt int64  `json:"startedAt"`
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	// WaitingFor is what a session whose status is waiting waits on, such
	// as "dialog open" while a prompt or a menu waits on an answer.
	WaitingFor string `json:"waitingFor"`
	// BgState is Claude Code's own working, blocked or idle for a background
	// session. Interactive sessions leave it empty.
	BgState string `json:"state"`
}

// Background reports whether the session runs detached, which is what lets
// the view attach to it or stop it.
func (l Live) Background() bool { return l.Kind == "background" }

// ClaudeEnv is the one variable overview sets for a claude it starts. It
// is Claude Code's own interface for choosing a config directory, and it
// reaches only that child, so it is the exception to overview passing its
// settings as arguments.
func (h Home) ClaudeEnv() string { return "CLAUDE_CONFIG_DIR=" + h.Dir }

// claude runs h.Claude against this home.
func (h Home) claude(args ...string) *exec.Cmd {
	cmd := exec.Command(h.Claude, args...)
	cmd.Env = append(os.Environ(), h.ClaudeEnv())
	return cmd
}

// liveWithin is how long Live gives `claude agents --json`, which answers
// in about a tenth of a second. One that has not by then is stuck, as it
// is while the keychain it reads does not answer, and is killed, so that
// whoever asked can ask again.
const liveWithin = 30 * time.Second

// Live asks Claude Code which sessions are running under this home.
// `claude agents --json` is the supported listing; the files behind it are
// internal and change between versions.
func (h Home) Live() ([]Live, error) { return h.liveIn(liveWithin) }

// liveIn is Live with a time limit of its own.
func (h Home) liveIn(limit time.Duration) ([]Live, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.Claude, "agents", "--json")
	cmd.Env = append(os.Environ(), h.ClaudeEnv())
	// What claude started may hold its output open after it is killed:
	// stop waiting for that soon after.
	cmd.WaitDelay = limit / 10
	out, err := cmd.Output()
	if ctx.Err() != nil {
		err = fmt.Errorf("timed out after %v", limit)
	}
	if err != nil {
		return nil, fmt.Errorf("claude agents --json: %w", err)
	}
	var live []Live
	if err := json.Unmarshal(out, &live); err != nil {
		return nil, fmt.Errorf("claude agents --json: %w", err)
	}
	return live, nil
}

// Row is one session in the view: the live listing joined with what the hook
// recorded and what its transcripts say.
type Row struct {
	Live
	State      State
	Since      time.Time
	Reason     string
	Title      string
	LastPrompt string
	// LastReply is the last thing the session said, empty while it works on
	// a prompt it has not answered yet.
	LastReply   string
	LastReplyAt time.Time
	Transcript  string
	// Hooked is false for a session that has not reported through the hook,
	// which leaves only Claude Code's own busy or idle to go on.
	Hooked bool
}

// Rows builds the view's rows, ordered by directory and then name. t reads
// the transcripts, and keeps what it read for the next call.
func (h Home) Rows(t *Transcripts) ([]Row, error) {
	live, err := h.Live()
	if err != nil {
		return nil, err
	}
	recorded := h.Sessions()
	now := time.Now()
	rows := make([]Row, 0, len(live))
	var paths []string
	for _, l := range live {
		r := Row{Live: l}
		rec, ok := recorded[l.SessionID]
		r.Hooked = ok
		r.State, r.Since, r.Reason = liveState(l, rec)
		path := rec.Transcript
		if path == "" {
			path = t.Find(h, l.SessionID)
		}
		r.Transcript = path
		if f, ok := t.Facts(path); ok {
			r.Title, r.LastPrompt = f.Title, f.LastPrompt
			r.LastReply, r.LastReplyAt = f.LastReply, f.ReplyAt
			r.State, r.Since, r.Reason = limited(r.State, r.Since, r.Reason, f.Limit, now)
			if r.Since.IsZero() {
				r.Since = f.Modified
			}
		}
		if r.Since.IsZero() {
			r.Since = time.UnixMilli(l.StartedAt)
		}
		rows = append(rows, r)
		if r.Transcript != "" {
			paths = append(paths, r.Transcript)
		}
	}
	t.Retain(paths)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Cwd != rows[j].Cwd {
			return rows[i].Cwd < rows[j].Cwd
		}
		return rows[i].Name < rows[j].Name
	})
	return rows, nil
}

// liveState settles a session's state from two sources. Claude Code's busy,
// waiting or idle is current but coarse: waiting means a dialog, such as a
// permission prompt or the menu shown when usage runs out, waits on an
// answer. The hook record knows about a pending prompt,
// but it goes stale when a turn is interrupted, because no Stop hook fires
// then.
func liveState(l Live, rec Session) (State, time.Time, string) {
	busy := l.Status != "idle"
	switch {
	case rec.State == Blocked:
		return Blocked, rec.Since, rec.Reason
	case l.BgState == "blocked", l.Status == "waiting":
		return Blocked, time.Time{}, ""
	case busy && rec.State == Working:
		return Working, rec.Since, ""
	case busy:
		return Working, time.Time{}, ""
	case rec.State == Idle:
		return Idle, rec.Since, ""
	default:
		return Idle, time.Time{}, ""
	}
}

// limited folds a usage limit into a session's state. A session out of
// usage stops whether or not Claude Code still shows the menu that says
// so, so an idle one is blocked until the limit resets, and a blocked one
// is blocked by the limit. A working one has got past it.
func limited(st State, since time.Time, reason string, lim Limit, now time.Time) (State, time.Time, string) {
	switch {
	case lim.Text == "" || st == Working:
		return st, since, reason
	case st == Blocked && reason == "":
		if since.IsZero() {
			since = lim.At
		}
		return Blocked, since, lim.Text
	case st == Idle && lim.Active(now):
		return Blocked, lim.At, lim.Text
	}
	return st, since, reason
}

// Asking reports whether an idle session ended its last reply with a
// question, which means it is waiting on an answer rather than done.
func (r Row) Asking() bool {
	if r.State != Idle {
		return false
	}
	reply := strings.TrimRight(r.LastReply, " \n\t*_`)")
	return strings.HasSuffix(reply, "?")
}

// Label is what the list calls the session: the title Claude Code gave
// it, or its name until it has one.
func (r Row) Label() string {
	if r.Title != "" {
		return r.Title
	}
	return r.Name
}

// NeedsYou is true for a session that cannot go on without its owner.
func (r Row) NeedsYou() bool { return r.State == Blocked || r.Asking() }

// AttachArgs is the command line that opens a background session in the
// terminal. The session is resumed in place if it had stopped, and keeps
// its hooks. Like NewArgs, it only builds the line, which a pane runs
// with ClaudeEnv, and looks nothing up.
func (h Home) AttachArgs(l Live) []string { return []string{h.Claude, "attach", l.ID} }

// NewArgs is the command line that starts a new interactive session,
// which joins the fleet, with prompt as its first message when one is
// given.
func (h Home) NewArgs(prompt ...string) []string { return append([]string{h.Claude}, prompt...) }

// Stop stops a background session. Its conversation is kept.
func (h Home) Stop(l Live) error { return h.stopSession(l) }
