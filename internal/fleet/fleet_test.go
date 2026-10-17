package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestApplyFollowsATurn(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	steps := []struct {
		ev      HookEvent
		want    State
		changed bool
	}{
		{HookEvent{Name: "SessionStart"}, Idle, true},
		{HookEvent{Name: "UserPromptSubmit", Prompt: "fix  the\nparser"}, Working, true},
		{HookEvent{Name: "PostToolUse"}, Working, false},
		{HookEvent{Name: "Notification", NotificationType: "permission_prompt", Message: "Claude needs your permission"}, Blocked, true},
		{HookEvent{Name: "PostToolUse"}, Working, true},
		{HookEvent{Name: "Stop"}, Idle, true},
		{HookEvent{Name: "Notification", NotificationType: "idle_prompt"}, Idle, false},
		{HookEvent{Name: "SessionEnd"}, Ended, true},
	}
	var s Session
	for i, step := range steps {
		step.ev.SessionID = "abc"
		now := t0.Add(time.Duration(i) * time.Minute)
		next, changed := Apply(s, step.ev, now)
		if next.State != step.want || changed != step.changed {
			t.Fatalf("step %d (%s): got %s changed=%v, want %s changed=%v",
				i, step.ev.Name, next.State, changed, step.want, step.changed)
		}
		if changed && next.State != s.State && !next.Since.Equal(now) {
			t.Fatalf("step %d: Since not moved on a state change", i)
		}
		if next.State != Blocked && next.Reason != "" {
			t.Fatalf("step %d: reason %q outlived the block", i, next.Reason)
		}
		s = next
	}
	if s.LastPrompt != "fix the parser" {
		t.Fatalf("prompt not flattened: %q", s.LastPrompt)
	}
}

func TestHookWritesOnlyUnderItsOwnHome(t *testing.T) {
	kira, seeq := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", kira)
	t.Setenv("OVERVIEW_SKIP", "")
	t.Setenv("OVERVIEW_LABEL", "reviewer")
	Hook(strings.NewReader(`{"hook_event_name":"UserPromptSubmit","session_id":"s1","cwd":"/x","prompt":"hello"}`))
	Hook(strings.NewReader(`{"hook_event_name":"Stop","session_id":"s1","agent_id":"sub"}`))

	got := Home(kira).Sessions()["s1"]
	if got.State != Working || got.Label != "reviewer" || got.LastPrompt != "hello" {
		t.Fatalf("unexpected record: %+v", got)
	}
	if n := len(Home(seeq).Sessions()); n != 0 {
		t.Fatalf("other home has %d sessions", n)
	}

	Hook(strings.NewReader(`{"hook_event_name":"SessionEnd","session_id":"s1"}`))
	if _, ok := Home(kira).Sessions()["s1"]; ok {
		t.Fatal("record kept after the session ended")
	}
}

func TestResolveHome(t *testing.T) {
	user := t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if _, err := ResolveHome(""); err == nil {
		t.Fatal("expected an error with no flag and no CLAUDE_CONFIG_DIR")
	}
	dir := filepath.Join(user, ".claude-kira")
	os.Mkdir(dir, 0o755)
	for _, in := range []string{"kira", "~/.claude-kira", dir} {
		if h, err := ResolveHome(in); err != nil || string(h) != dir {
			t.Fatalf("ResolveHome(%q) = %q, %v", in, h, err)
		}
	}
	if _, err := ResolveHome("seeq"); err == nil {
		t.Fatal("expected an error for a missing directory")
	}
}

const transcript = `{"type":"user","timestamp":"2026-10-07T10:00:00Z","cwd":"/p/app","message":{"content":"old prompt"}}
{"type":"assistant","timestamp":"2026-10-07T10:01:00Z","message":{"content":[{"type":"text","text":"old reply"}]}}
{"type":"user","timestamp":"2026-10-07T12:00:00Z","message":{"content":[{"type":"text","text":"add a test"},{"type":"image"}]}}
{"type":"user","isMeta":true,"timestamp":"2026-10-07T12:00:00Z","message":{"content":[{"type":"text","text":"[Image: source]"}]}}
{"type":"assistant","timestamp":"2026-10-07T12:00:05Z","message":{"content":[{"type":"thinking","thinking":"hm"}]}}
{"type":"assistant","timestamp":"2026-10-07T12:00:10Z","message":{"content":[{"type":"text","text":"Looking."},{"type":"tool_use"}]}}
{"type":"user","timestamp":"2026-10-07T12:00:11Z","message":{"content":[{"type":"tool_result","content":"ok"}]}}
{"type":"assistant","isSidechain":true,"timestamp":"2026-10-07T12:00:12Z","message":{"content":[{"type":"text","text":"subagent chatter"}]}}
{"type":"assistant","timestamp":"2026-10-07T12:05:00Z","message":{"content":[{"type":"text","text":"Test added and passing."}]}}
{"type":"ai-title","aiTitle":"Add a test"}
{"type":"last-prompt","lastPrompt":"add a test"}
`

func TestReadThreadKeepsOnlyTurnsInTheWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess1234.jsonl")
	os.WriteFile(path, []byte(transcript), 0o644)
	since := time.Date(2026, 10, 7, 11, 0, 0, 0, time.UTC)

	th, err := readThread(path, since)
	if err != nil {
		t.Fatal(err)
	}
	if th.SessionID != "sess1234" || th.Cwd != "/p/app" || th.Title != "Add a test" {
		t.Fatalf("unexpected thread: %+v", th)
	}
	if len(th.Turns) != 1 || th.Turns[0].Prompt != "add a test" || th.Turns[0].Reply != "Test added and passing." {
		t.Fatalf("unexpected turns: %+v", th.Turns)
	}

	m, ok := transcriptMeta(path)
	if !ok || m.Title != "Add a test" || m.LastPrompt != "add a test" || m.LastReply != "Test added and passing." {
		t.Fatalf("unexpected meta: %+v", m)
	}
	if turns := RecentTurns(path, 5); len(turns) != 2 || turns[0].Prompt != "old prompt" {
		t.Fatalf("unexpected recent turns: %+v", turns)
	}

	out := extract([]Thread{th}, []Row{{Live: Live{SessionID: "sess1234", Name: "app-1a"}, State: Blocked, Reason: "needs permission"}}, since, since.Add(2*time.Hour))
	for _, want := range []string{"Session: app-1a", "State: blocked (needs permission)", "Prompt: add a test", "Last reply: Test added and passing."} {
		if !strings.Contains(out, want) {
			t.Fatalf("extract lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "old prompt") || strings.Contains(out, "subagent") {
		t.Fatalf("extract leaked text from outside the window or a subagent:\n%s", out)
	}
}

func TestPublishMovesTheCursorToTheExtractNotPastIt(t *testing.T) {
	home := Home(t.TempDir())
	fake := filepath.Join(t.TempDir(), "claude")
	os.WriteFile(fake, []byte("#!/bin/sh\necho '[]'\n"), 0o755)
	t.Setenv("OVERVIEW_CLAUDE", fake)
	t.Setenv("OVERVIEW_ORCHESTRATOR_DIR", "/p/orchestrator")

	if _, _, quiet, err := home.Activity(); err != nil || !quiet {
		t.Fatalf("empty home: quiet=%v err=%v", quiet, err)
	}

	write := func(project, cwd string) {
		dir := filepath.Join(string(home), "projects", project)
		os.MkdirAll(dir, 0o755)
		now := time.Now().UTC().Add(time.Second).Format(time.RFC3339)
		line := `{"type":"user","timestamp":"` + now + `","cwd":"` + cwd + `","message":{"content":"ship it"}}` + "\n"
		os.WriteFile(filepath.Join(dir, "sess5678.jsonl"), []byte(line), 0o644)
	}
	write("orch", "/p/orchestrator")
	if _, _, quiet, _ := home.Activity(); !quiet {
		t.Fatal("the orchestrator's own transcript counted as activity")
	}

	time.Sleep(1100 * time.Millisecond)
	write("app", "/p/app")
	text, _, quiet, err := home.Activity()
	if err != nil || quiet || !strings.Contains(text, "Prompt: ship it") {
		t.Fatalf("quiet=%v err=%v text=%q", quiet, err, text)
	}
	looked := readTime(home.pendingFile())
	if !home.LastDigestAt().Before(looked) {
		t.Fatal("cursor moved before anything was published")
	}

	d, err := home.Publish("## Finished\n- **app** (app): shipped\n")
	if err != nil {
		t.Fatal(err)
	}
	if !home.LastDigestAt().Equal(looked) {
		t.Fatalf("cursor at %v, want the extract time %v", home.LastDigestAt(), looked)
	}
	if got, _, ok := home.LatestDigest(); !ok || got != d.Text || !strings.Contains(got, "shipped") {
		t.Fatalf("latest digest: %q", got)
	}
	if _, err := home.Publish("  "); err == nil {
		t.Fatal("an empty digest was accepted")
	}
}

func TestOrchestratorCommandLine(t *testing.T) {
	for every, want := range map[time.Duration]string{
		15 * time.Minute: "*/15 * * * *", time.Hour: "7 * * * *", 3 * time.Hour: "7 */3 * * *",
	} {
		if got, err := cronFor(every); err != nil || got != want {
			t.Fatalf("cronFor(%v) = %q, %v", every, got, err)
		}
	}
	if _, err := cronFor(time.Minute); err == nil {
		t.Fatal("a one minute tick was accepted")
	}
	args, err := Home("/h/.claude-kira").orchestratorArgs(time.Hour, "sonnet", "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--bg", "--name overview-orchestrator", "--permission-mode dontAsk",
		`"Bash(overview *)"`, `"Read(//h/.claude-kira/projects/**)"`, "`7 * * * *`"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("command line lacks %s:\n%s", want, joined)
		}
	}
}

func TestHealthTellsASilentOrchestratorFromAHealthyOne(t *testing.T) {
	home := Home(t.TempDir())
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	started := now.Add(-5 * time.Hour)
	live := []Live{{Name: OrchestratorName, SessionID: "abc-1", Status: "idle", StartedAt: started.UnixMilli()}}
	home.saveOrchestratorState(orchestratorState{SessionID: "abc-1", Every: time.Hour, StartedAt: started})

	if h := home.health(nil, now); h.Running || h.Stale {
		t.Fatalf("no session: %+v", h)
	}
	writeTime(home.heartbeatFile(), now.Add(-30*time.Minute))
	if h := home.health(live, now); !h.Running || h.Stale {
		t.Fatalf("ticked half an hour ago: %+v", h)
	}
	writeTime(home.heartbeatFile(), now.Add(-3*time.Hour))
	if h := home.health(live, now); !h.Stale {
		t.Fatalf("last tick three hours ago on an hourly schedule: %+v", h)
	}
	// A session restarted a minute ago has not had time to tick yet.
	live[0].StartedAt = now.Add(-time.Minute).UnixMilli()
	if h := home.health(live, now); h.Stale {
		t.Fatalf("just restarted: %+v", h)
	}

	args, _ := home.orchestratorArgs(time.Hour, "sonnet", "abc-1")
	if j := strings.Join(args, " "); !strings.Contains(j, "--bg --resume abc-1") || !strings.Contains(j, "erased your tick schedule") {
		t.Fatalf("resume command line: %s", j)
	}
}

func TestNeedsYou(t *testing.T) {
	for _, c := range []struct {
		row  Row
		want bool
	}{
		{Row{State: Blocked}, true},
		{Row{State: Idle, LastReply: "Done. Should I push it?"}, true},
		{Row{State: Idle, LastReply: "Want me to **commit**?**"}, true},
		{Row{State: Idle, LastReply: "Committed and pushed."}, false},
		{Row{State: Working, LastReply: "Should I push it?"}, false},
	} {
		if got := c.row.NeedsYou(); got != c.want {
			t.Errorf("%+v: NeedsYou() = %v, want %v", c.row, got, c.want)
		}
	}
	if st, _, _ := liveState(Live{Status: "idle", BgState: "blocked"}, Session{}); st != Blocked {
		t.Errorf("a background session Claude Code reports blocked came out %s", st)
	}
}

// liveState joins Claude Code's view of a session with the hook's record.
// Each case is one step of the precedence: a recorded block, then Claude
// Code's own block, then busy or idle, keeping the record's time only when
// the record agrees.
func TestLiveStatePrecedence(t *testing.T) {
	at := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name   string
		live   Live
		rec    Session
		state  State
		since  time.Time
		reason string
	}{
		{"recorded block beats busy", Live{Status: "busy"}, Session{State: Blocked, Since: at, Reason: "Bash"}, Blocked, at, "Bash"},
		{"recorded block beats idle", Live{Status: "idle", BgState: "idle"}, Session{State: Blocked, Since: at, Reason: "r"}, Blocked, at, "r"},
		{"Claude Code's block", Live{Status: "idle", BgState: "blocked"}, Session{State: Idle, Since: at}, Blocked, time.Time{}, ""},
		{"busy with a working record", Live{Status: "busy"}, Session{State: Working, Since: at}, Working, at, ""},
		{"busy with an idle record", Live{Status: "busy"}, Session{State: Idle, Since: at}, Working, time.Time{}, ""},
		{"no status counts as busy", Live{}, Session{}, Working, time.Time{}, ""},
		{"idle overrides a stale working record", Live{Status: "idle"}, Session{State: Working, Since: at}, Idle, time.Time{}, ""},
		{"idle with an idle record", Live{Status: "idle"}, Session{State: Idle, Since: at}, Idle, at, ""},
		{"idle with an ended record", Live{Status: "idle"}, Session{State: Ended, Since: at}, Idle, time.Time{}, ""},
	} {
		st, since, reason := liveState(c.live, c.rec)
		if st != c.state || !since.Equal(c.since) || reason != c.reason {
			t.Errorf("%s: got %s %v %q, want %s %v %q", c.name, st, since, reason, c.state, c.since, c.reason)
		}
	}
}

func TestSaveSessionCleansUpWhenRenameFails(t *testing.T) {
	h := Home(filepath.Join(t.TempDir(), "home"))
	// A directory where the record belongs makes the rename fail.
	if err := os.MkdirAll(h.sessionFile("s1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := h.saveSession(Session{ID: "s1"}); err == nil {
		t.Fatal("expected the rename to fail")
	}
	left, _ := filepath.Glob(filepath.Join(h.sessionsDir(), ".tmp-*"))
	if len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}
