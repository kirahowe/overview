package fleet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type State string

const (
	Working State = "working"
	// Blocked means the session cannot continue until someone answers it,
	// usually a permission prompt.
	Blocked State = "blocked"
	Idle    State = "idle"
	Ended   State = "ended"
)

// Session is what the hooks have recorded about one session. It is the only
// thing the hook writes, one small file per session.
type Session struct {
	ID         string    `json:"session_id"`
	Cwd        string    `json:"cwd,omitempty"`
	Label      string    `json:"label,omitempty"`
	State      State     `json:"state"`
	Since      time.Time `json:"since"`
	Reason     string    `json:"reason,omitempty"`
	LastPrompt string    `json:"last_prompt,omitempty"`
	Transcript string    `json:"transcript,omitempty"`
}

// HookEvent is the part of a Claude Code hook payload that overview uses.
type HookEvent struct {
	Name             string `json:"hook_event_name"`
	SessionID        string `json:"session_id"`
	Cwd              string `json:"cwd"`
	Transcript       string `json:"transcript_path"`
	Prompt           string `json:"prompt"`
	NotificationType string `json:"notification_type"`
	Message          string `json:"message"`
	AgentID          string `json:"agent_id"`
}

// Apply folds one hook event into a session's record and reports whether
// anything changed. Tool events arrive on every tool call, so the common case
// has to come back unchanged and cost no write.
func Apply(s Session, ev HookEvent, now time.Time) (Session, bool) {
	next := s
	next.ID = ev.SessionID
	if ev.Cwd != "" {
		next.Cwd = ev.Cwd
	}
	if ev.Transcript != "" {
		next.Transcript = ev.Transcript
	}
	switch ev.Name {
	case "SessionStart":
		if s.State == "" || s.State == Ended {
			next.State = Idle
		}
	case "UserPromptSubmit":
		next.State = Working
		next.LastPrompt = clip(ev.Prompt, 300)
	case "PreToolUse", "PostToolUse":
		// A tool running again is the only sign that a prompt was answered.
		if s.State == Blocked {
			next.State = Working
		}
	case "Notification":
		switch ev.NotificationType {
		case "idle_prompt", "auth_success":
		default:
			next.State = Blocked
			next.Reason = clip(ev.Message, 200)
		}
	case "Stop":
		next.State = Idle
	case "SessionEnd":
		next.State = Ended
	default:
		return s, false
	}
	if next.State != s.State {
		next.Since = now
	}
	if next.State != Blocked {
		next.Reason = ""
	}
	return next, next != s
}

// clip flattens whitespace and cuts s to at most n runes.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func (h Home) sessionFile(id string) string {
	return filepath.Join(h.sessionsDir(), filepath.Base(id)+".json")
}

func (h Home) loadSession(id string) Session {
	var s Session
	if b, err := os.ReadFile(h.sessionFile(id)); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// saveSession replaces the record with a rename, so a reader never sees half
// a file.
func (h Home) saveSession(s Session) error {
	if err := os.MkdirAll(h.sessionsDir(), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(h.sessionsDir(), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	if err := os.Rename(tmp.Name(), h.sessionFile(s.ID)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// Sessions returns every recorded session by id.
func (h Home) Sessions() map[string]Session {
	out := map[string]Session{}
	files, _ := filepath.Glob(filepath.Join(h.sessionsDir(), "*.json"))
	for _, f := range files {
		var s Session
		if b, err := os.ReadFile(f); err == nil && json.Unmarshal(b, &s) == nil && s.ID != "" {
			out[s.ID] = s
		}
	}
	return out
}
