package fleet

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"time"
)

// Live is one running session as Claude Code itself lists it.
type Live struct {
	PID       int    `json:"pid"`
	Cwd       string `json:"cwd"`
	Kind      string `json:"kind"`
	StartedAt int64  `json:"startedAt"`
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Status    string `json:"status"`
}

func claudeBin() string {
	if b := os.Getenv("OVERVIEW_CLAUDE"); b != "" {
		return b
	}
	return "claude"
}

func (h Home) claude(args ...string) *exec.Cmd {
	cmd := exec.Command(claudeBin(), args...)
	// OVERVIEW_SKIP keeps overview's own Claude runs out of the fleet.
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+string(h), "OVERVIEW_SKIP=1")
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

// Row is one session in the view: the live listing joined with what the hooks
// recorded and what its transcript says.
type Row struct {
	Live
	State      State
	Since      time.Time
	Reason     string
	Label      string
	Title      string
	LastPrompt string
	// Hooked is false for a session that has not reported through the hook,
	// which leaves only Claude Code's own busy or idle to go on.
	Hooked bool
}

// Rows builds the view's rows, ordered by directory and then name.
func (h Home) Rows() ([]Row, error) {
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
		r.Label, r.LastPrompt = rec.Label, rec.LastPrompt
		r.State, r.Since, r.Reason = liveState(l, rec)
		path := rec.Transcript
		if path == "" {
			path = h.FindTranscript(l.SessionID)
		}
		if meta, ok := transcriptMeta(path); ok {
			r.Title = meta.Title
			if r.LastPrompt == "" {
				r.LastPrompt = meta.LastPrompt
			}
			if r.Since.IsZero() {
				r.Since = meta.Modified
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
