package fleet

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OrchestratorName is the session name the orchestrator runs under. It is how
// the view and the stop command find it among the live sessions.
const OrchestratorName = "overview-orchestrator"

// OrchestratorDir is the directory the orchestrator session runs in. Its
// AGENTS.md holds the orchestrator's instructions, so it has to be the one in
// this repository: by default the orchestrator/ beside the bin/ that holds
// the real binary.
func OrchestratorDir() string {
	if d := os.Getenv("OVERVIEW_ORCHESTRATOR_DIR"); d != "" {
		return d
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return filepath.Join(filepath.Dir(filepath.Dir(exe)), "orchestrator")
}

// Orchestrator returns the running orchestrator session, if there is one.
func (h Home) Orchestrator() (Live, bool, error) {
	live, err := h.Live()
	if err != nil {
		return Live{}, false, err
	}
	for _, l := range live {
		if l.Name == OrchestratorName {
			return l, true, nil
		}
	}
	return Live{}, false, nil
}

// orchestratorState is what start records so that a later ensure can tell a
// silent orchestrator from a healthy one, and bring back the same
// conversation.
type orchestratorState struct {
	SessionID string        `json:"session_id"`
	Every     time.Duration `json:"every"`
	Model     string        `json:"model"`
	StartedAt time.Time     `json:"started_at"`
}

func (h Home) orchestratorFile() string { return filepath.Join(h.dataDir(), "orchestrator.json") }
func (h Home) heartbeatFile() string    { return filepath.Join(h.dataDir(), "orchestrator-heartbeat") }

func (h Home) orchestratorState() (orchestratorState, bool) {
	var st orchestratorState
	b, err := os.ReadFile(h.orchestratorFile())
	return st, err == nil && json.Unmarshal(b, &st) == nil
}

func (h Home) saveOrchestratorState(st orchestratorState) error {
	if err := os.MkdirAll(h.dataDir(), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(st)
	return os.WriteFile(h.orchestratorFile(), b, 0o644)
}

// Health says whether the orchestrator is doing its job, which is more than
// whether its session exists. Its tick schedule lives in the session's memory
// and is lost when the process restarts, and a session in that state looks
// alive while never waking again.
type Health struct {
	Running bool
	// Stale is set when the session is running but has gone longer than its
	// interval, plus some slack, without looking at the fleet.
	Stale     bool
	LastCheck time.Time
	Live      Live
}

// staleSlack covers a tick that starts late or takes a while to finish.
const staleSlack = 15 * time.Minute

func (h Home) health(live []Live, now time.Time) Health {
	var out Health
	for _, l := range live {
		if l.Name == OrchestratorName {
			out.Running, out.Live = true, l
		}
	}
	out.LastCheck = readTime(h.heartbeatFile())
	if !out.Running {
		return out
	}
	st, known := h.orchestratorState()
	if !known {
		st.Every = time.Hour
	}
	// Before the first tick, count from when the session started.
	last := out.LastCheck
	for _, t := range []time.Time{st.StartedAt, time.UnixMilli(out.Live.StartedAt)} {
		if t.After(last) {
			last = t
		}
	}
	out.Stale = now.Sub(last) > st.Every+staleSlack
	return out
}

// OrchestratorHealth reports on the orchestrator from the live listing.
func (h Home) OrchestratorHealth() (Health, error) {
	live, err := h.Live()
	if err != nil {
		return Health{}, err
	}
	return h.health(live, time.Now()), nil
}

// HealthOf is OrchestratorHealth for a caller that already has the rows.
func (h Home) HealthOf(rows []Row) Health {
	live := make([]Live, len(rows))
	for i, r := range rows {
		live[i] = r.Live
	}
	return h.health(live, time.Now())
}

// cronFor turns a tick interval into a cron expression. Whole hours fire at
// seven past, to stay off the minute everyone else's hourly jobs use.
func cronFor(every time.Duration) (string, error) {
	mins := int(every.Minutes())
	switch {
	case mins < 5:
		return "", errors.New("the tick interval must be at least 5m")
	case mins < 60:
		return fmt.Sprintf("*/%d * * * *", mins), nil
	case mins == 60:
		return "7 * * * *", nil
	default:
		return fmt.Sprintf("7 */%d * * *", mins/60), nil
	}
}

// orchestratorArgs is the command line that starts the orchestrator as a
// background session, or brings back the conversation `resume` names. It may
// run the overview command, schedule its own ticks, read this home's
// transcripts and write its draft digest. With dontAsk as the permission
// mode, anything else is refused without a prompt, since nobody is attached
// to answer one.
func (h Home) orchestratorArgs(every time.Duration, model, resume string) ([]string, error) {
	cron, err := cronFor(every)
	if err != nil {
		return nil, err
	}
	settings, _ := json.Marshal(map[string]any{"permissions": map[string]any{"allow": []string{
		"Bash(overview *)", "CronCreate", "CronList", "CronDelete",
		"Read(/" + string(h) + "/projects/**)", "Read(/" + string(h) + "/overview/**)",
		"Edit(/" + OrchestratorDir() + "/digest.md)",
	}}})
	prompt := fmt.Sprintf("Start orchestrating. Your tick schedule is the cron expression `%s`. "+
		"Schedule it as AGENTS.md describes, then run the first tick now.", cron)
	args := []string{"--bg"}
	if resume != "" {
		prompt = fmt.Sprintf("You were restarted, and the restart erased your tick schedule. "+
			"It is the cron expression `%s`. Schedule it again, then run a tick now.", cron)
		args = append(args, "--resume", resume)
	}
	return append(args, "--name", OrchestratorName, "--model", model,
		"--permission-mode", "dontAsk", "--settings", string(settings), prompt), nil
}

func (h Home) launch(st orchestratorState, resume string) (string, error) {
	dir := OrchestratorDir()
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err != nil {
		return "", fmt.Errorf("no orchestrator instructions at %s (set OVERVIEW_ORCHESTRATOR_DIR)", dir)
	}
	args, err := h.orchestratorArgs(st.Every, st.Model, resume)
	if err != nil {
		return "", err
	}
	cmd := h.claude(args...)
	// The orchestrator is a session like any other and should show in the
	// view, so it does not get the OVERVIEW_SKIP that h.claude sets.
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+string(h))
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	// The listing is the only place the new session's id shows up, and it
	// can take a moment to appear there.
	st.StartedAt, st.SessionID = time.Now(), resume
	for i := 0; i < 20 && resume == ""; i++ {
		if l, running, _ := h.Orchestrator(); running {
			st.SessionID = l.SessionID
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	return strings.TrimSpace(string(out)), h.saveOrchestratorState(st)
}

// StartOrchestrator launches the orchestrator unless one is already running,
// and returns what Claude Code printed.
func (h Home) StartOrchestrator(every time.Duration, model string) (string, error) {
	if _, running, err := h.Orchestrator(); err != nil {
		return "", err
	} else if running {
		return "", errors.New("the orchestrator is already running: claude attach " + OrchestratorName)
	}
	return h.launch(orchestratorState{Every: every, Model: model}, "")
}

// EnsureOrchestrator is the watchdog, meant to be run on a timer from
// outside Claude Code. It restarts an orchestrator that has exited or gone
// silent, resuming the same conversation so that it keeps its memory of
// earlier digests. It never starts one that was not started by hand first,
// and stop makes it stand down.
func (h Home) EnsureOrchestrator(every time.Duration, model string) (string, error) {
	hl, err := h.OrchestratorHealth()
	if err != nil {
		return "", err
	}
	st, known := h.orchestratorState()
	switch {
	case !known && !hl.Running:
		return "not set up: run overview orchestrator start", nil
	case !known:
		// Started before this record existed. Adopt it as it is.
		st = orchestratorState{SessionID: hl.Live.SessionID, Every: every, Model: model,
			StartedAt: time.UnixMilli(hl.Live.StartedAt)}
		return "adopted the running orchestrator", h.saveOrchestratorState(st)
	case hl.Running && !hl.Stale:
		return "ok", nil
	case hl.Running && hl.Live.Status != "idle":
		return "overdue but busy, left alone", nil
	case hl.Running:
		if err := h.stopSession(hl.Live); err != nil {
			return "", err
		}
	}
	if _, err := h.launch(st, st.SessionID); err != nil {
		return "", err
	}
	if hl.Running {
		return "restarted: it had not looked at the fleet since " + hl.LastCheck.Local().Format("Mon 15:04"), nil
	}
	return "restarted: it was not running", nil
}

func (h Home) stopSession(l Live) error {
	// Claude Code's short id for a session is the first block of its id.
	short, _, _ := strings.Cut(l.SessionID, "-")
	if out, err := h.claude("stop", short).CombinedOutput(); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// StopOrchestrator stops the orchestrator session and tells the watchdog to
// leave it stopped. Its conversation is kept.
func (h Home) StopOrchestrator() error {
	l, running, err := h.Orchestrator()
	if err != nil {
		return err
	}
	os.Remove(h.orchestratorFile())
	if !running {
		return errors.New("the orchestrator is not running")
	}
	return h.stopSession(l)
}
