package fleet

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Hook handles one Claude Code hook call: it reads the payload from r and
// updates that session's record. It never reports failure to the caller's
// exit status, because a failing hook would get in the way of the session it
// is only meant to watch.
func Hook(r io.Reader) {
	// Read the payload before any early return. Claude Code writes it to the
	// hook's stdin and would hit a closed pipe otherwise.
	payload, _ := io.ReadAll(r)
	if os.Getenv("OVERVIEW_SKIP") != "" {
		return
	}
	var ev HookEvent
	if json.Unmarshal(payload, &ev) != nil || ev.SessionID == "" {
		return
	}
	// Subagent hooks carry an agent id. They say nothing about the session's
	// own state.
	if ev.AgentID != "" {
		return
	}
	home := hookHome(ev)
	if home == "" {
		return
	}
	prev := home.loadSession(ev.SessionID)
	next, changed := Apply(prev, ev, time.Now().UTC().Truncate(time.Second))
	if next.State == Ended {
		os.Remove(home.sessionFile(ev.SessionID))
		return
	}
	if label := hookLabel(); label != next.Label {
		next.Label, changed = label, true
	}
	if changed {
		_ = home.saveSession(next)
	}
}

// hookHome is the config directory of the session that fired the hook. The
// session's own environment names it. A transcript path is the fallback,
// since transcripts live in <home>/projects/<project>/.
func hookHome(ev HookEvent) Home {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return Home(dir)
	}
	projects := filepath.Dir(filepath.Dir(ev.Transcript))
	if filepath.Base(projects) == "projects" {
		return Home(filepath.Dir(projects))
	}
	return ""
}

// hookLabel names who the session is working as, when its launcher says so.
// staff sets STAFF_AGENT for every member it starts.
func hookLabel() string {
	if l := os.Getenv("OVERVIEW_LABEL"); l != "" {
		return l
	}
	if id := os.Getenv("STAFF_AGENT"); id != "" {
		return "staff/" + id
	}
	return ""
}
