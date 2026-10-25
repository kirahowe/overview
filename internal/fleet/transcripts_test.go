package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	want := []TaskRef{
		{Project: "relay", Dir: "/w/relay", ID: 54, Linked: ts("2026-10-23T17:44:47.677Z")},
		{Dir: "/w/other", ID: 64, Linked: ts("2026-10-23T17:49:05Z")},
	}
	if !reflect.DeepEqual(got.Links, want) {
		t.Fatalf("links %+v, want %+v", got.Links, want)
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

func TestAnUnfinishedLineWaitsForItsEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	line := userPrompt("2026-10-23T10:00:00Z", "/w", "Work on p#3: x")
	if err := os.WriteFile(path, []byte(line[:40]), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := NewTranscripts()
	if f, _ := idx.Facts(path); len(f.Links) != 0 {
		t.Fatalf("linked from half a line: %+v", f.Links)
	}
	appendLines(t, path, line[40:])
	if f, _ := idx.Facts(path); len(f.Links) != 1 || f.Links[0].ID != 3 {
		t.Fatalf("links %+v", f.Links)
	}
}

func TestSubagentsLinkTheirParentInTimeOrder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "projects", "-w")
	path := filepath.Join(dir, "s-1.jsonl")
	subs := filepath.Join(dir, "s-1", "subagents")
	writeLines(t, path,
		bashCall("2026-10-23T10:00:00Z", "/w/a", "m1", "issues start 1"),
		bashResult("2026-10-23T10:00:01Z", "m1", false),
	)
	idx := NewTranscripts()
	if f, _ := idx.Facts(path); len(f.Links) != 1 {
		t.Fatalf("links %+v", f.Links)
	}
	// A subagent appears after the first read. Its prompt links nothing,
	// its Bash call links the parent, and an errored call links nothing.
	sidechain := func(s string) string { return strings.Replace(s, `{"type"`, `{"isSidechain":true,"type"`, 1) }
	writeLines(t, filepath.Join(subs, "agent-a1.jsonl"),
		sidechain(userPrompt("2026-10-23T10:00:02Z", "/w/a", "Work on p#9: sub")),
		sidechain(bashCall("2026-10-23T10:00:03Z", "/w/b", "a1", "issues start 2")),
		sidechain(bashResult("2026-10-23T10:00:05Z", "a1", false)),
		sidechain(bashCall("2026-10-23T10:00:06Z", "/w/b", "a2", "issues start 3")),
		sidechain(bashResult("2026-10-23T10:00:07Z", "a2", true)),
	)
	// The main session links 1 again after the subagent linked 2.
	appendLines(t, path,
		bashCall("2026-10-23T10:00:08Z", "/w/a", "m2", "issues start 1"),
		bashResult("2026-10-23T10:00:09Z", "m2", false),
	)
	f, _ := idx.Facts(path)
	want := []TaskRef{
		{Dir: "/w/b", ID: 2, Linked: ts("2026-10-23T10:00:05Z")},
		{Dir: "/w/a", ID: 1, Linked: ts("2026-10-23T10:00:01Z")},
	}
	if !reflect.DeepEqual(f.Links, want) {
		t.Fatalf("links %+v, want %+v", f.Links, want)
	}
	// A subagent's own words are not the session's.
	if f.LastReply != "" || f.Title != "" {
		t.Fatalf("facts leaked from a subagent: %+v", f)
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
	s := idx.sessions[path].main
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
	home := Home{Dir: t.TempDir()}
	path := filepath.Join(home.Dir, "projects", "-w-a", "s-1.jsonl")
	writeLines(t, path,
		userPrompt("2026-10-23T10:00:00Z", "/w/a", "Work on p#4: x"),
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
	if len(r.Tasks) != 1 || r.Tasks[0].Project != "p" || r.Tasks[0].ID != 4 {
		t.Fatalf("tasks %+v", r.Tasks)
	}
	if r.Hooked || r.Since.IsZero() {
		t.Fatalf("hooked %v since %v", r.Hooked, r.Since)
	}
	if rows[1].Tasks != nil || rows[1].Title != "" || !rows[1].Since.Equal(time.UnixMilli(2000)) {
		t.Fatalf("a session with no transcript: %+v", rows[1])
	}
}

// synthetic builds about size bytes of transcript the way a long session
// looks: mostly large tool results and progress records, with prompts,
// replies and a few issue links among them.
func synthetic(size int) []byte {
	var b strings.Builder
	pad := strings.Repeat("lorem ipsum dolor sit amet ", 300)
	for i := 0; b.Len() < size; i++ {
		at := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second).Format("2006-01-02T15:04:05.000Z")
		id := fmt.Sprintf("toolu_%d", i)
		cmd := "go test ./..."
		if i%50 == 0 {
			cmd = fmt.Sprintf("cd /w/a && issues start %d", i/50%9+1)
		}
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
		if !ok || len(f.Links) == 0 {
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
