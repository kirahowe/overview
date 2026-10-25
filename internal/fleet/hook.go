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
	var ev HookEvent
	if json.Unmarshal(payload, &ev) != nil || ev.SessionID == "" {
		return
	}
	// Subagent hooks say nothing about the session's own state.
	if ev.AgentID != "" {
		return
	}
	home, ok := hookHome(ev)
	if !ok {
		return
	}
	prev := home.loadSession(ev.SessionID)
	now := time.Now().UTC().Truncate(time.Second)
	next, changed := Apply(prev, ev, now)
	if next.State == Ended {
		os.Remove(home.sessionFile(ev.SessionID))
		return
	}
	if changed {
		_ = home.saveSession(next)
	}
}

// hookHome is the config directory of the session that fired the hook,
// read from the payload's transcript path, since transcripts live in
// <home>/projects/<project>/.
func hookHome(ev HookEvent) (Home, bool) {
	projects := filepath.Dir(filepath.Dir(ev.Transcript))
	if !filepath.IsAbs(projects) || filepath.Base(projects) != "projects" {
		return Home{}, false
	}
	return Home{Dir: filepath.Dir(projects)}, true
}
