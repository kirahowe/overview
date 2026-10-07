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
	t.Setenv("OVERVIEW_LABEL", "")
	t.Setenv("STAFF_AGENT", "cto")
	Hook(strings.NewReader(`{"hook_event_name":"UserPromptSubmit","session_id":"s1","cwd":"/x","prompt":"hello"}`))
	Hook(strings.NewReader(`{"hook_event_name":"Stop","session_id":"s1","agent_id":"sub"}`))

	got := Home(kira).Sessions()["s1"]
	if got.State != Working || got.Label != "staff/cto" || got.LastPrompt != "hello" {
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
	if !ok || m.Title != "Add a test" || m.LastPrompt != "add a test" {
		t.Fatalf("unexpected meta: %+v", m)
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
