package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLines(t testing.TB, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendLines(t testing.TB, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
}

func userPrompt(at, cwd, prompt string) string {
	return fmt.Sprintf(`{"type":"user","isSidechain":false,"timestamp":%q,"cwd":%q,"message":{"role":"user","content":%q}}`, at, cwd, prompt)
}

func bashCall(at, cwd, id, command string) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"cwd":%q,"message":{"content":[{"type":"tool_use","id":%q,"name":"Bash","input":{"command":%q}}]}}`, at, cwd, id, command)
}

func bashResult(at, id string, isErr bool) string {
	return fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":[{"tool_use_id":%q,"type":"tool_result","content":"ok","is_error":%v}]}}`, at, id, isErr)
}

func reply(at, text string) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":%q}]}}`, at, text)
}

// limitHit is the line Claude Code writes when it refuses a turn because
// the account is out of usage, cut to the fields that matter here.
func limitHit(at string, resets int64) string {
	return fmt.Sprintf(`{"parentUuid":"p","isSidechain":false,"type":"assistant","timestamp":%q,"message":{"model":"<synthetic>","role":"assistant","type":"message","content":[{"type":"text","text":"You've hit your Fable limit · resets Nov 17 at 3pm (America/Halifax)"}]},"apiError":"usage_limit_reached","apiErrorParams":{"rate_limit_info":{"status":"rejected","resetsAt":%d,"rateLimitType":"seven_day_overage_included"}},"error":"rate_limit","isApiErrorMessage":true}`, at, resets)
}

const limitText = "You've hit your Fable limit · resets Nov 17 at 3pm (America/Halifax)"

func ts(s string) time.Time {
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return at
}

func TestFactsFromARealisticTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects", "-w-relay", "s-1.jsonl")
	writeLines(t, path,
		`{"type":"permission-mode","permissionMode":"default"}`,
		userPrompt("2026-10-23T17:44:47.677Z", "/w/relay", "Work on relay#54: Let roles keep cards"),
		reply("2026-10-23T17:45:00.000Z", "On it."),
		bashCall("2026-10-23T17:46:00.000Z", "/w/relay", "toolu_1", "cd ~/code/x && issues start 6"),
		bashResult("2026-10-23T17:46:02.000Z", "toolu_1", false),
		bashCall("2026-10-23T17:47:00.000Z", "/w/relay", "toolu_2", "issues start 7"),
		bashResult("2026-10-23T17:47:02.000Z", "toolu_2", true),
		bashCall("2026-10-23T17:48:00.000Z", "/w/relay", "toolu_3", `echo "issues start 8"`),
		bashResult("2026-10-23T17:48:02.000Z", "toolu_3", false),
		bashCall("2026-10-23T17:49:00.000Z", "/w/relay", "toolu_4", "cd /w/other && issues --dir . unblock 64 --on 63 && issues --dir . start 64"),
		bashResult("2026-10-23T17:49:05.000Z", "toolu_4", false),
		`{"type":"ai-title","aiTitle":"Roles keep cards"}`,
		`{"type":"last-prompt","lastPrompt":"Work on relay#54:   Let roles\nkeep cards"}`,
	)
	got, ok := NewTranscripts().Facts(path)
	if !ok {
		t.Fatal("no facts")
	}
	if got.Title != "Roles keep cards" || got.LastPrompt != "Work on relay#54: Let roles keep cards" {
		t.Fatalf("title and prompt: %+v", got)
	}
	if got.LastReply != "On it." || !got.ReplyAt.Equal(ts("2026-10-23T17:45:00Z")) {
		t.Fatalf("reply: %q at %v", got.LastReply, got.ReplyAt)
	}
	if got.Modified.IsZero() {
		t.Fatal("no modification time")
	}
}

func TestANewPromptClearsTheReplyAndSidechainsAndMetaDoNot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeLines(t, path,
		userPrompt("2026-10-23T10:00:00Z", "/w", "hello"),
		reply("2026-10-23T10:00:05Z", "Hi."),
		`{"type":"assistant","isSidechain":true,"timestamp":"2026-10-23T10:00:06Z","message":{"content":[{"type":"text","text":"agent chatter"}]}}`,
		`{"type":"user","isMeta":true,"timestamp":"2026-10-23T10:00:07Z","message":{"content":"<local-command-caveat>"}}`,
		`{"type":"user","timestamp":"2026-10-23T10:00:08Z","message":{"content":[{"tool_use_id":"x","type":"tool_result","content":"ok"}]}}`,
	)
	idx := NewTranscripts()
	if f, _ := idx.Facts(path); f.LastReply != "Hi." {
		t.Fatalf("reply %q", f.LastReply)
	}
	appendLines(t, path, userPrompt("2026-10-23T10:01:00Z", "/w", "and then?"))
	if f, _ := idx.Facts(path); f.LastReply != "" || !f.ReplyAt.IsZero() {
		t.Fatalf("a new prompt left the reply %q", f.LastReply)
	}
}

// A prompt sent while out of usage is refused too, so only something the
// session says lifts the limit.
func TestAUsageLimitHoldsUntilTheSessionSaysSomethingElse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeLines(t, path,
		userPrompt("2026-11-15T16:19:20Z", "/w", "go on"),
		limitHit("2026-11-15T16:19:24Z", 1794942000),
	)
	idx := NewTranscripts()
	want := Limit{Text: limitText, At: ts("2026-11-15T16:19:24Z"), Resets: time.Unix(1794942000, 0)}
	if f, _ := idx.Facts(path); f.Limit != want {
		t.Fatalf("limit %+v, want %+v", f.Limit, want)
	}
	appendLines(t, path,
		userPrompt("2026-11-15T16:30:00Z", "/w", "and now?"),
		`{"type":"assistant","isSidechain":true,"timestamp":"2026-11-15T16:30:01Z","message":{"content":[{"type":"text","text":"agent chatter"}]}}`,
	)
	if f, _ := idx.Facts(path); f.Limit != want {
		t.Fatalf("a prompt or a subagent lifted the limit: %+v", f.Limit)
	}
	appendLines(t, path, reply("2026-11-17T19:00:05Z", "Back at it."))
	if f, _ := idx.Facts(path); f.Limit != (Limit{}) {
		t.Fatalf("a reply left the limit %+v", f.Limit)
	}
}

func TestAnUnfinishedLineWaitsForItsEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	line := userPrompt("2026-10-23T10:00:00Z", "/w", "Work on p#3: x")
	if err := os.WriteFile(path, []byte(line[:40]), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := NewTranscripts()
	if f, _ := idx.Facts(path); f.LastReply != "" {
		t.Fatalf("read half a line: %+v", f)
	}
	appendLines(t, path, line[40:], reply("2026-10-23T10:00:05Z", "Done."))
	if f, _ := idx.Facts(path); f.LastReply != "Done." {
		t.Fatalf("facts %+v", f)
	}
}

func TestAMissingTranscriptHasNoFacts(t *testing.T) {
	idx := NewTranscripts()
	if _, ok := idx.Facts(filepath.Join(t.TempDir(), "nope.jsonl")); ok {
		t.Fatal("facts for a missing file")
	}
	if _, ok := idx.Facts(""); ok {
		t.Fatal("facts for no path")
	}
	var nilIdx *Transcripts
	if _, ok := nilIdx.Facts("x"); ok {
		t.Fatal("facts from a nil index")
	}
}

func TestReadingOnlyWhatWasAppended(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeLines(t, path, userPrompt("2026-10-23T10:00:00Z", "/w", "Work on p#1"))
	idx := NewTranscripts()
	idx.Facts(path)
	s := idx.sessions[path]
	size, _ := os.Stat(path)
	if s.off != size.Size() || len(s.tail) != 0 {
		t.Fatalf("read to %d of %d, tail %q", s.off, size.Size(), s.tail)
	}
	appendLines(t, path, reply("2026-10-23T10:00:05Z", "ok"))
	idx.Facts(path)
	if size, _ = os.Stat(path); s.off != size.Size() {
		t.Fatalf("read to %d of %d", s.off, size.Size())
	}
}

func TestRowsTakeTheirFactsFromTheTranscript(t *testing.T) {
	home := Home{Dir: t.TempDir(), User: "/u"}
	path := filepath.Join(home.Dir, "projects", "-w-a", "s-1.jsonl")
	writeLines(t, path,
		userPrompt("2026-10-23T10:00:00Z", "/w/a", "Work on p#4: x"),
		bashCall("2026-10-23T10:00:01Z", "/w/a", "toolu_1", "cd ~/code/x && issues start 6 && issues --dir $W start 7"),
		bashResult("2026-10-23T10:00:02Z", "toolu_1", false),
		reply("2026-10-23T10:00:05Z", "Done?"),
		`{"type":"ai-title","aiTitle":"The title"}`,
		`{"type":"last-prompt","lastPrompt":"x"}`,
	)
	script := filepath.Join(t.TempDir(), "claude")
	os.WriteFile(script, []byte(`#!/bin/sh
echo '[{"pid":1,"cwd":"/w/a","kind":"interactive","startedAt":1000,"sessionId":"s-1","name":"a","status":"idle"},{"pid":2,"cwd":"/w/b","kind":"interactive","startedAt":2000,"sessionId":"s-2","name":"b","status":"busy"}]'
`), 0o755)
	home.Claude = script
	rows, err := home.Rows(NewTranscripts())
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows %+v, %v", rows, err)
	}
	r := rows[0]
	if r.Title != "The title" || r.LastPrompt != "x" || r.LastReply != "Done?" || !r.LastReplyAt.Equal(ts("2026-10-23T10:00:05Z")) {
		t.Fatalf("row %+v", r)
	}
	if r.Hooked || r.Since.IsZero() {
		t.Fatalf("hooked %v since %v", r.Hooked, r.Since)
	}
	if rows[1].Title != "" || !rows[1].Since.Equal(time.UnixMilli(2000)) {
		t.Fatalf("a session with no transcript: %+v", rows[1])
	}
}

// A session out of usage shows as blocked by the limit, whether Claude
// Code still holds its menu open or it was told to stop and wait, until
// the limit resets.
func TestRowsShowASessionOutOfUsageAsBlocked(t *testing.T) {
	home := Home{Dir: t.TempDir(), User: "/u"}
	future := time.Now().Add(48 * time.Hour).Unix()
	past := time.Now().Add(-time.Hour).Unix()
	for id, resets := range map[string]int64{"menu": future, "stopped": future, "reset": past} {
		writeLines(t, filepath.Join(home.Dir, "projects", "-w", id+".jsonl"),
			userPrompt("2026-11-15T16:19:20Z", "/w", "go on"),
			limitHit("2026-11-15T16:19:24Z", resets),
		)
	}
	script := filepath.Join(t.TempDir(), "claude")
	os.WriteFile(script, []byte(`#!/bin/sh
echo '[{"pid":1,"cwd":"/w","kind":"interactive","startedAt":1000,"sessionId":"menu","name":"a","status":"waiting","waitingFor":"dialog open"},{"pid":2,"cwd":"/w","kind":"interactive","startedAt":1000,"sessionId":"stopped","name":"b","status":"idle"},{"pid":3,"cwd":"/w","kind":"interactive","startedAt":1000,"sessionId":"reset","name":"c","status":"idle"}]'
`), 0o755)
	home.Claude = script
	rows, err := home.Rows(NewTranscripts())
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows %+v, %v", rows, err)
	}
	at := ts("2026-11-15T16:19:24Z")
	for i, want := range []struct {
		state  State
		reason string
	}{{Blocked, limitText}, {Blocked, limitText}, {Idle, ""}} {
		r := rows[i]
		if r.State != want.state || r.Reason != want.reason || r.WaitingFor != map[int]string{0: "dialog open"}[i] {
			t.Errorf("%s: %s %q, want %s %q", r.SessionID, r.State, r.Reason, want.state, want.reason)
		}
		if want.state == Blocked && !r.Since.Equal(at) {
			t.Errorf("%s: blocked since %v, want the limit's %v", r.SessionID, r.Since, at)
		}
	}
}

// synthetic builds about size bytes of transcript the way a long session
// looks: mostly large tool results and progress records, with prompts,
// and replies among them.
func synthetic(size int) []byte {
	var b strings.Builder
	pad := strings.Repeat("lorem ipsum dolor sit amet ", 300)
	for i := 0; b.Len() < size; i++ {
		at := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second).Format("2006-01-02T15:04:05.000Z")
		id := fmt.Sprintf("toolu_%d", i)
		cmd := "go test ./..."
		b.WriteString(userPrompt(at, "/w/a", fmt.Sprintf("prompt %d %s", i, pad[:200])) + "\n")
		b.WriteString(bashCall(at, "/w/a", id, cmd) + "\n")
		fmt.Fprintf(&b, `{"type":"user","timestamp":%q,"message":{"content":[{"tool_use_id":%q,"type":"tool_result","content":%q,"is_error":false}]}}`+"\n", at, id, pad)
		fmt.Fprintf(&b, `{"type":"progress","timestamp":%q,"data":{"type":"hook_progress","output":%q}}`+"\n", at, pad)
		b.WriteString(reply(at, "reply "+pad[:100]) + "\n")
	}
	return []byte(b.String())
}

// BenchmarkFirstRead is the cost of reading a 5MB transcript for the
// first time, which is what each session costs when the view starts.
func BenchmarkFirstRead(b *testing.B) {
	path := filepath.Join(b.TempDir(), "s.jsonl")
	data := synthetic(5 << 20)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for range b.N {
		f, ok := NewTranscripts().Facts(path)
		if !ok || f.LastReply == "" {
			b.Fatalf("facts %+v", f)
		}
	}
}

// BenchmarkAppendRead is the cost of a refresh after one more turn.
func BenchmarkAppendRead(b *testing.B) {
	path := filepath.Join(b.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, synthetic(5<<20), 0o644); err != nil {
		b.Fatal(err)
	}
	idx := NewTranscripts()
	idx.Facts(path)
	extra := synthetic(20 << 10)
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		appendLines(b, path, strings.TrimSuffix(string(extra), "\n"))
		b.StartTimer()
		idx.Facts(path)
	}
}

func TestRowsForgetSessionsThatAreGone(t *testing.T) {
	home := Home{Dir: t.TempDir()}
	a := filepath.Join(home.Dir, "projects", "-w", "s-1.jsonl")
	b := filepath.Join(home.Dir, "projects", "-w", "s-2.jsonl")
	writeLines(t, a, userPrompt("2026-10-23T10:00:00Z", "/w", "Work on p#1"))
	writeLines(t, b, userPrompt("2026-10-23T10:00:00Z", "/w", "Work on p#2"))
	idx := NewTranscripts()
	if idx.Find(home, "s-1") != a || idx.Find(home, "s-2") != b {
		t.Fatal("transcripts not found")
	}
	idx.Facts(a)
	idx.Facts(b)
	// The path is remembered: it is returned without a search.
	moved := filepath.Join(home.Dir, "projects", "-v", "s-1.jsonl")
	writeLines(t, moved, "{}")
	if idx.Find(home, "s-1") != a {
		t.Fatal("searched again for a path that still exists")
	}
	os.Remove(a)
	if idx.Find(home, "s-1") != moved {
		t.Fatal("did not search again after the file went")
	}
	idx.Retain([]string{b})
	if _, ok := idx.sessions[b]; !ok || len(idx.sessions) != 1 {
		t.Fatalf("sessions kept: %v", idx.sessions)
	}
	if len(idx.found) != 1 || idx.found["s-2"] != b {
		t.Fatalf("locations kept: %v", idx.found)
	}
	idx.Retain(nil)
	if len(idx.sessions) != 0 || len(idx.found) != 0 {
		t.Fatal("not emptied")
	}
}
