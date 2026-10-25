package fleet

import (
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

// Live asks Claude Code which sessions are running under this home.
// `claude agents --json` is the supported listing; the files behind it are
// internal and change between versions.
func (h Home) Live() ([]Live, error) {
	out, err := h.claude("agents", "--json").Output()
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
	// Tasks are the issues its transcripts show it taking on, oldest first.
	Tasks []TaskRef
}

// Rows builds the view's rows, ordered by directory and then name. t reads
// the transcripts, and keeps what it read for the next call.
func (h Home) Rows(t *Transcripts) ([]Row, error) {
	live, err := h.Live()
	if err != nil {
		return nil, err
	}
	recorded := h.Sessions()
	rows := make([]Row, 0, len(live))
	for _, l := range live {
		r := Row{Live: l}
		rec, ok := recorded[l.SessionID]
		r.Hooked = ok
		r.State, r.Since, r.Reason = liveState(l, rec)
		path := rec.Transcript
		if path == "" {
			path = h.FindTranscript(l.SessionID)
		}
		r.Transcript = path
		if f, ok := t.Facts(path); ok {
			r.Title, r.LastPrompt, r.Tasks = f.Title, f.LastPrompt, f.Links
			r.LastReply, r.LastReplyAt = f.LastReply, f.ReplyAt
			if r.Since.IsZero() {
				r.Since = f.Modified
			}
		}
		if r.Since.IsZero() {
			r.Since = time.UnixMilli(l.StartedAt)
		}
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Cwd != rows[j].Cwd {
			return rows[i].Cwd < rows[j].Cwd
		}
		return rows[i].Name < rows[j].Name
	})
	return rows, nil
}

// liveState settles a session's state from two sources. Claude Code's busy or
// idle is current but coarse. The hook record knows about a pending prompt,
// but it goes stale when a turn is interrupted, because no Stop hook fires
// then.
func liveState(l Live, rec Session) (State, time.Time, string) {
	busy := l.Status != "idle"
	switch {
	case rec.State == Blocked:
		return Blocked, rec.Since, rec.Reason
	case l.BgState == "blocked":
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

// Asking reports whether an idle session ended its last reply with a
// question, which means it is waiting on an answer rather than done.
func (r Row) Asking() bool {
	if r.State != Idle {
		return false
	}
	reply := strings.TrimRight(r.LastReply, " \n\t*_`)")
	return strings.HasSuffix(reply, "?")
}

// NeedsYou is true for a session that cannot go on without its owner.
func (r Row) NeedsYou() bool { return r.State == Blocked || r.Asking() }

// AttachCmd opens a background session in the terminal. The session is
// resumed in place if it had stopped, and keeps its hooks.
func (h Home) AttachCmd(l Live) *exec.Cmd { return h.claude("attach", l.ID) }

// NewCmd starts a new interactive session, which joins the fleet.
func (h Home) NewCmd() *exec.Cmd { return h.claude() }

// NewCmdWith starts a new interactive session with prompt as its first
// message.
func (h Home) NewCmdWith(prompt string) *exec.Cmd { return h.claude(prompt) }

// Stop stops a background session. Its conversation is kept.
func (h Home) Stop(l Live) error { return h.stopSession(l) }
