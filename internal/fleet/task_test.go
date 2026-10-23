package fleet

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestIssuesStarts(t *testing.T) {
	const cwd = "/work/app"
	r := func(project, dir string, id int) TaskRef { return TaskRef{Project: project, Dir: dir, ID: id} }
	for _, c := range []struct {
		name, cmd string
		want      []TaskRef
	}{
		{"plain", "issues start 3", []TaskRef{r("", cwd, 3)}},
		{"-C absolute", "issues -C /other/proj start 4", []TaskRef{r("", "/other/proj", 4)}},
		{"-C relative", "issues -C ../b start 4", []TaskRef{r("", "/work/b", 4)}},
		{"--dir space", "issues --dir /p start 5", []TaskRef{r("", "/p", 5)}},
		{"--dir=", "issues --dir=/p/q/ start 5", []TaskRef{r("", "/p/q", 5)}},
		{"--project", "issues --project overview start 6", []TaskRef{r("overview", cwd, 6)}},
		{"--project=", "issues --project=ov start 6", []TaskRef{r("ov", cwd, 6)}},
		{"valueless flags", "issues --json --all --edn start 7", []TaskRef{r("", cwd, 7)}},
		{"config flag", "issues --config /c.edn start 7", []TaskRef{r("", cwd, 7)}},
		{"--config=", "issues --config=/c.edn start 7", []TaskRef{r("", cwd, 7)}},
		{"flags after start", "issues start 8 --project=p -C /d", []TaskRef{r("p", "/d", 8)}},
		{"flags before id", "issues start --dir /d 8", []TaskRef{r("", "/d", 8)}},
		{"quoted dir with spaces", `issues -C "/my dir/x" start 9`, []TaskRef{r("", "/my dir/x", 9)}},
		{"single quoted dir", `issues -C '/my dir' start 9`, []TaskRef{r("", "/my dir", 9)}},
		{"escaped space", `issues -C /my\ dir start 9`, []TaskRef{r("", "/my dir", 9)}},
		{"escaped quote in double", `issues -C "/a\"b" start 9`, []TaskRef{r("", `/a"b`, 9)}},
		{"separator in quotes", `issues -C "/a;b&&c" start 1`, []TaskRef{r("", "/a;b&&c", 1)}},
		{"quoted text is not a command", `echo "x && issues start 3"`, nil},
		{"cd relative", "cd x && issues start 2", []TaskRef{r("", "/work/app/x", 2)}},
		{"cd absolute", "cd /elsewhere && issues start 2", []TaskRef{r("", "/elsewhere", 2)}},
		{"cd then -C relative", "cd /a; issues -C b start 2", []TaskRef{r("", "/a/b", 2)}},
		{"cd home makes the directory unknown", "cd ~/x && issues start 2", nil},
		{"cd home then absolute dir", "cd ~/x && issues --dir /d start 2", []TaskRef{r("", "/d", 2)}},
		{"cd home then relative dir", "cd ~/x && issues --dir d start 2", nil},
		{"cd with no argument", "cd && issues start 2", nil},
		{"cd -", "cd - && issues start 2", nil},
		{"cd variable", "cd $HOME/x; issues start 2", nil},
		{"cd unknown stays unknown", "cd ~/x; cd /a; issues start 2", nil},
		{"cd unknown covers later commands only", "issues start 1; cd ~; issues start 2", []TaskRef{r("", cwd, 1)}},
		{"pushd another dir", "pushd /other && issues start 5", []TaskRef{r("", "/other", 5)}},
		{"pushd relative", "pushd sub; issues start 5", []TaskRef{r("", "/work/app/sub", 5)}},
		{"pushd with no argument", "pushd && issues start 5", nil},
		{"pushd +n", "pushd +1 && issues start 5", nil},
		{"popd", "pushd /other && popd && issues start 5", nil},
		{"brace group", "{ cd /other; issues start 5; }", nil},
		{"brace group glued", "{cd /other; issues start 5; }", nil},
		{"subshell", "(cd /other; issues start 5 )", nil},
		{"subshell spaced", "( cd /other; issues start 5 )", nil},
		{"after a subshell", "(cd /other); issues start 5", nil},
		{"group then absolute dir", "(cd /other); issues -C /d start 5", []TaskRef{r("", "/d", 5)}},
		{"comment hides what follows", "echo hi # note && issues start 5", nil},
		{"comment ends at the line", "echo hi # note\nissues start 5", []TaskRef{r("", cwd, 5)}},
		{"comment after a start", "issues start 5 # take it", []TaskRef{r("", cwd, 5)}},
		{"hash inside a word", "echo a#b && issues start 5", []TaskRef{r("", cwd, 5)}},
		{"quoted hash", `echo "# x" && issues start 5`, []TaskRef{r("", cwd, 5)}},
		{"comment line", "# issues start 4\nissues start 5", []TaskRef{r("", cwd, 5)}},
		{"tilde dir dropped", "issues -C ~/x start 2", nil},
		{"tilde dir long flag", "issues --dir ~/code/other start 5", nil},
		{"variable dir dropped", "issues --dir $HOME/x start 5", nil},
		{"substitution dir dropped", "issues --dir=$(pwd) start 5", nil},
		{"backtick dir dropped", "issues --dir `pwd` start 5", nil},
		{"empty dir dropped", `issues --dir "" start 5`, nil},
		{"dropped ref does not hide the next", "issues --dir ~/x start 5; issues start 6", []TaskRef{r("", cwd, 6)}},
		{"help", "issues start --help 5", nil},
		{"short help", "issues start -h 5", nil},
		{"help before start", "issues --help start 5", nil},
		{"help after id", "issues start 5 -h", nil},
		{"unknown flag after start", "issues start --bogus 5", nil},
		{"unknown flag after id", "issues start 5 --bogus", nil},
		{"force", "issues start --force 5", []TaskRef{r("", cwd, 5)}},
		{"command substitution is opaque", "$(issues start 3)", nil},
		{"substitution as argument", "echo $(issues start 3)", nil},
		{"substitution in quotes", `echo "$(issues start 3)"`, nil},
		{"nested substitution", "echo $(echo $(x) ; issues start 3) && issues start 4", []TaskRef{r("", cwd, 4)}},
		{"backticks are opaque", "echo `issues start 3`", nil},
		{"substitution as id", "issues start $(echo 3)", nil},
		{"quoted echo", `echo "issues start 3"`, nil},
		{"assignment prefix", "x=1 issues start 3", []TaskRef{r("", cwd, 3)}},
		{"heredoc body", "cat > notes.md <<'EOF'\nissues start 5\nEOF", nil},
		{"heredoc body unquoted delimiter", "git commit -F - <<EOF\nsubject\n\nissues start 7 would link\nEOF", nil},
		{"heredoc double quoted delimiter", "cat <<\"EOF\"\nissues start 5\nEOF", nil},
		{"heredoc escaped delimiter", "cat <<\\EOF\nissues start 5\nEOF", nil},
		{"heredoc dash strips tabs", "cat <<-EOF\n\tissues start 5\n\tEOF\nissues start 6", []TaskRef{r("", cwd, 6)}},
		{"heredoc plain delimiter needs no indent", "cat <<EOF\n\tEOF\nissues start 5\nEOF\nissues start 6", []TaskRef{r("", cwd, 6)}},
		{"heredoc then next command", "cat <<EOF\nx\nEOF\nissues start 6", []TaskRef{r("", cwd, 6)}},
		{"same line after heredoc", "cat <<EOF && issues start 3\nissues start 5\nEOF", []TaskRef{r("", cwd, 3)}},
		{"heredoc never closed", "cat <<EOF\nissues start 5\nissues start 6", nil},
		{"here-string is not a heredoc", "cat <<< foo\nissues start 5", []TaskRef{r("", cwd, 5)}},
		{"two heredocs", "cat <<A <<B\nissues start 1\nA\nissues start 2\nB\nissues start 3", []TaskRef{r("", cwd, 3)}},
		{"env assignment", "FOO=1 BAR='a b' issues start 2", []TaskRef{r("", cwd, 2)}},
		{"absolute binary", "/usr/local/bin/issues start 2", []TaskRef{r("", cwd, 2)}},
		{"relative binary", "./bin/issues start 2", []TaskRef{r("", cwd, 2)}},
		{"multiple", "issues start 1 && issues start 2; issues -C /z start 3", []TaskRef{r("", cwd, 1), r("", cwd, 2), r("", "/z", 3)}},
		{"newline and pipe", "echo hi |\nissues start 4 | cat", []TaskRef{r("", cwd, 4)}},
		{"background", "issues start 4 &", []TaskRef{r("", cwd, 4)}},
		{"or", "false || issues start 4", []TaskRef{r("", cwd, 4)}},
		{"show", "issues show 3", nil},
		{"no id", "issues start", nil},
		{"non-integer id", "issues start abc", nil},
		{"zero id", "issues start 0", nil},
		{"negative id", "issues start -3", nil},
		{"signed id", "issues start +3", nil},
		{"huge id", "issues start 99999999999999999999999", nil},
		{"echo", "echo issues start 3", nil},
		{"prefix name", "myissues start 3", nil},
		{"unknown flag before", "issues --bogus start 3", nil},
		{"value flag without value", "issues -C", nil},
		{"other subcommand", "issues done 3", nil},
		{"empty", "", nil},
		{"unterminated quote", `issues -C "/a start 3`, nil},
	} {
		got := IssuesStarts(c.cmd, cwd)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: IssuesStarts(%q) = %+v, want %+v", c.name, c.cmd, got, c.want)
		}
	}
	if got := IssuesStarts("issues start 3", ""); !reflect.DeepEqual(got, []TaskRef{{ID: 3}}) {
		t.Errorf("no cwd: %+v", got)
	}
	if got := IssuesStarts("issues -C rel start 3", ""); got != nil {
		t.Errorf("relative dir with no cwd: %+v", got)
	}
	if got := IssuesStarts("issues -C rel start 3", "rel/cwd"); got != nil {
		t.Errorf("relative dir with a relative cwd: %+v", got)
	}
}

func TestOpeningTask(t *testing.T) {
	const cwd = "/work/app"
	for _, c := range []struct {
		prompt string
		want   TaskRef
		ok     bool
	}{
		{"Work on overview#6: Show tasks", TaskRef{Project: "overview", Dir: cwd, ID: 6}, true},
		{"  \n Work on a.b_c-d#12", TaskRef{Project: "a.b_c-d", Dir: cwd, ID: 12}, true},
		{"Work on x#1 now", TaskRef{Project: "x", Dir: cwd, ID: 1}, true},
		{"Work on x#1.", TaskRef{Project: "x", Dir: cwd, ID: 1}, true},
		{"Work on x#1\nmore", TaskRef{Project: "x", Dir: cwd, ID: 1}, true},
		{"Work on x#1 more", TaskRef{Project: "x", Dir: cwd, ID: 1}, true},
		{"Work on x#0", TaskRef{}, false},
		{"Work on x#", TaskRef{}, false},
		{"Work on x#1a", TaskRef{}, false},
		{"Work on x#1,", TaskRef{}, false},
		{"Work on #1", TaskRef{}, false},
		{"Work on -x#1", TaskRef{Project: "-x", Dir: cwd, ID: 1}, true},
		{"Work on my_app+2@x#3: y", TaskRef{Project: "my_app+2@x", Dir: cwd, ID: 3}, true},
		{"Work on café#4", TaskRef{Project: "café", Dir: cwd, ID: 4}, true},
		{"Work on a#b#1", TaskRef{}, false},
		{"Work on x 1", TaskRef{}, false},
		{"please Work on x#1", TaskRef{}, false},
		{"work on x#1", TaskRef{}, false},
		{"Work on  x#1", TaskRef{}, false},
		{"Work on x#99999999999999999999999", TaskRef{}, false},
		{"What about x#1?", TaskRef{}, false},
		{"", TaskRef{}, false},
	} {
		got, ok := OpeningTask(c.prompt, cwd)
		if ok != c.ok || got != c.want {
			t.Errorf("OpeningTask(%q) = %+v, %v; want %+v, %v", c.prompt, got, ok, c.want, c.ok)
		}
	}
}

func bashEvent(cmd string) HookEvent {
	in, _ := json.Marshal(map[string]string{"command": cmd})
	return HookEvent{Name: "PostToolUse", ToolName: "Bash", ToolInput: in}
}

func TestFoldTasks(t *testing.T) {
	at := func(m int) time.Time { return time.Unix(1_700_000_000+int64(m)*60, 0).UTC() }
	a, b := TaskRef{Dir: "/w", ID: 1, Linked: at(0)}, TaskRef{Dir: "/w", ID: 2, Linked: at(1)}
	if got := FoldTasks([]TaskRef{a, b}); !reflect.DeepEqual(got, []TaskRef{a, b}) {
		t.Fatalf("got %+v", got)
	}
	// Linking the first again moves it to the end, and it keeps the
	// time it was first linked.
	again := a
	again.Linked = at(5)
	if got := FoldTasks([]TaskRef{a, b, again}); !reflect.DeepEqual(got, []TaskRef{b, a}) {
		t.Fatalf("relink: %+v", got)
	}
	// Eight at most, the newest kept.
	var lines []TaskRef
	for i := 1; i <= 12; i++ {
		lines = append(lines, TaskRef{ID: i})
	}
	if cur := FoldTasks(lines); len(cur) != maxTasks || cur[0].ID != 5 || cur[7].ID != 12 {
		t.Fatalf("kept %+v", cur)
	}
}

func TestTasksToAppend(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	a, b := TaskRef{Dir: "/w", ID: 1}, TaskRef{Dir: "/w", ID: 2}
	got := TasksToAppend(nil, []TaskRef{a, b}, now)
	if len(got) != 2 || !got[0].Same(a) || !got[1].Same(b) || !got[0].Linked.Equal(now) {
		t.Fatalf("got %+v", got)
	}
	cur := FoldTasks(got)
	if add := TasksToAppend(cur, []TaskRef{b}, now.Add(time.Hour)); len(add) != 0 {
		t.Fatalf("relinking the last ref appended %+v", add)
	}
	add := TasksToAppend(cur, []TaskRef{a}, now.Add(time.Hour))
	if len(add) != 1 || !add[0].Linked.Equal(now) {
		t.Fatalf("relinking a appended %+v, want its first time", add)
	}
	if add := TasksToAppend(cur, nil, now); len(add) != 0 {
		t.Fatalf("no refs appended %+v", add)
	}
}

func TestParseTasksSkipsBadLines(t *testing.T) {
	got := ParseTasks([]byte("{\"dir\":\"/w\",\"id\":1}\n{\"dir\":\"/w\",\"id\n\n{\"id\":2,\"linked\":\"2026-10-23T10:00:00Z\"}\n"))
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 || got[1].Linked.IsZero() {
		t.Fatalf("got %+v", got)
	}
}

// Links that arrive together, from a session and its subagents, each
// append a line, and none drops another. Past compactAt lines the file is
// rewritten to what it folds to.
func TestLinkTasksAppendsAndCompacts(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	now := time.Unix(1_700_000_000, 0).UTC()
	ev := func(id int) HookEvent { return withID(bashEvent(fmt.Sprint("issues start ", id))) }
	h.linkTasks(ev(1), "/w", now)
	h.linkTasks(ev(2), "/w", now.Add(time.Minute))
	h.linkTasks(ev(2), "/w", now.Add(2*time.Minute))
	if lines := h.readTasks("s1"); len(lines) != 2 {
		t.Fatalf("lines %+v", lines)
	}
	for i := 0; i < compactAt; i++ {
		h.linkTasks(ev(1+i%2), "/w", now.Add(time.Duration(i)*time.Hour))
	}
	lines := h.readTasks("s1")
	if len(lines) > compactAt || len(lines) < 2 {
		t.Fatalf("%d lines after compacting", len(lines))
	}
	got := h.loadTasks("s1")
	if len(got) != 2 || !got[0].Linked.Equal(now) || !got[1].Linked.Equal(now.Add(time.Minute)) {
		t.Fatalf("tasks %+v", got)
	}
}

func withID(ev HookEvent) HookEvent {
	ev.SessionID = "s1"
	return ev
}

// refs is rs without their link times.
func refs(rs []TaskRef) []TaskRef {
	out := make([]TaskRef, len(rs))
	for i, r := range rs {
		r.Linked = time.Time{}
		out[i] = r
	}
	return out
}

func TestEventTasks(t *testing.T) {
	got := EventTasks(bashEvent("issues start 1; issues start 2"), "/w")
	if !reflect.DeepEqual(got, []TaskRef{{Dir: "/w", ID: 1}, {Dir: "/w", ID: 2}}) {
		t.Fatalf("got %+v", got)
	}
	// The event's cwd wins over the record's.
	ev := bashEvent("issues start 5")
	ev.Cwd = "/ev"
	if got := EventTasks(ev, "/w"); len(got) != 1 || got[0].Dir != "/ev" {
		t.Fatalf("dir %+v", got)
	}
	// Only Bash, only PostToolUse, only valid input.
	for _, ev := range []HookEvent{
		{Name: "PostToolUse", ToolName: "Read", ToolInput: bashEvent("issues start 1").ToolInput},
		{Name: "PreToolUse", ToolName: "Bash", ToolInput: bashEvent("issues start 1").ToolInput},
		{Name: "PostToolUse", ToolName: "Bash", ToolInput: json.RawMessage(`"issues start 1"`)},
		{Name: "PostToolUse", ToolName: "Bash"},
		{Name: "UserPromptSubmit", Prompt: "run issues start 1"},
	} {
		if got := EventTasks(ev, "/w"); len(got) != 0 {
			t.Errorf("%+v linked %+v", ev, got)
		}
	}
	got = EventTasks(HookEvent{Name: "UserPromptSubmit", Cwd: "/w", Prompt: "Work on p#2: x"}, "")
	if !reflect.DeepEqual(got, []TaskRef{{Project: "p", Dir: "/w", ID: 2}}) {
		t.Fatalf("prompt link: %+v", got)
	}
}

func hookPayload(t *testing.T, path string, fields map[string]any) *strings.Reader {
	t.Helper()
	fields["session_id"] = "s1"
	fields["transcript_path"] = path
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return strings.NewReader(string(b))
}

func TestHookLinksTasksFromSubagentsAndWritesOnlyOnChange(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	path := filepath.Join(h.Dir, "projects", "-x", "s1.jsonl")
	Hook(hookPayload(t, path, map[string]any{"hook_event_name": "UserPromptSubmit", "cwd": "/x", "prompt": "go"}))
	file := h.sessionFile("s1")
	// Pin the file to an old time so a rewrite shows up in the mtime.
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	unchanged := func(why string) {
		t.Helper()
		fi, err := os.Stat(file)
		if err != nil || !fi.ModTime().Equal(old) {
			t.Fatalf("%s: the record was rewritten", why)
		}
	}

	bash := func(cmd string, agent string) map[string]any {
		return map[string]any{
			"hook_event_name": "PostToolUse", "cwd": "/x", "agent_id": agent,
			"tool_name": "Bash", "tool_input": map[string]string{"command": cmd},
		}
	}
	Hook(hookPayload(t, path, bash("go test ./...", "sub")))
	unchanged("a Bash event that starts nothing")
	Hook(hookPayload(t, path, bash("go test ./...", "")))
	unchanged("a main-session Bash event that starts nothing")

	Hook(hookPayload(t, path, bash("issues start 7", "sub")))
	got := h.loadSession("s1")
	if tasks := h.loadTasks("s1"); !reflect.DeepEqual(refs(tasks), []TaskRef{{Dir: "/x", ID: 7}}) || tasks[0].Linked.IsZero() {
		t.Fatalf("subagent start not linked: %+v", tasks)
	}
	if got.State != Working || got.LastPrompt != "go" {
		t.Fatalf("a subagent event changed the state: %+v", got)
	}
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	Hook(hookPayload(t, path, bash("issues start 7", "sub")))
	unchanged("the same event twice")
	Hook(hookPayload(t, path, bash("issues start 7", "")))
	unchanged("the same start from the main session")

	tasksFile := h.tasksFile("s1")
	tf, err := os.Stat(tasksFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tasksFile, old, old); err != nil {
		t.Fatal(err)
	}
	Hook(hookPayload(t, path, bash("issues start 7", "sub")))
	if fi, _ := os.Stat(tasksFile); !fi.ModTime().Equal(old) {
		t.Fatalf("relinking rewrote the tasks file (%v)", tf.ModTime())
	}

	// A subagent of a session with no record makes none.
	Hook(strings.NewReader(`{"hook_event_name":"PostToolUse","session_id":"nobody","agent_id":"a","cwd":"/x","tool_name":"Bash","tool_input":{"command":"issues start 1"},"transcript_path":"` + filepath.Join(h.Dir, "projects", "-x", "nobody.jsonl") + `"}`))
	if _, ok := h.Sessions()["nobody"]; ok {
		t.Fatal("a subagent created a session record")
	}
	if _, err := os.Stat(h.tasksFile("nobody")); err == nil {
		t.Fatal("a subagent created a tasks file for an unknown session")
	}
}

func TestHookSubagentLinkNeverRewritesTheSessionRecord(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	path := filepath.Join(h.Dir, "projects", "-x", "s1.jsonl")
	Hook(hookPayload(t, path, map[string]any{"hook_event_name": "UserPromptSubmit", "cwd": "/x", "prompt": "go"}))
	before, err := os.ReadFile(h.sessionFile("s1"))
	if err != nil {
		t.Fatal(err)
	}
	Hook(hookPayload(t, path, map[string]any{
		"hook_event_name": "PostToolUse", "cwd": "/x", "agent_id": "sub",
		"tool_name": "Bash", "tool_input": map[string]string{"command": "issues start 7"},
	}))
	after, err := os.ReadFile(h.sessionFile("s1"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("record changed:\n%s\n%s", before, after)
	}
	if got := h.loadTasks("s1"); !reflect.DeepEqual(refs(got), []TaskRef{{Dir: "/x", ID: 7}}) {
		t.Fatalf("tasks %+v", got)
	}
	// Sessions ignores the tasks file, and the rows carry its tasks.
	if len(h.Sessions()) != 1 {
		t.Fatalf("sessions: %+v", h.Sessions())
	}
	// A main-session link goes to the same file, and the session's end
	// removes both files.
	Hook(hookPayload(t, path, map[string]any{
		"hook_event_name": "PostToolUse", "cwd": "/x",
		"tool_name": "Bash", "tool_input": map[string]string{"command": "issues start 8"},
	}))
	if got := h.loadTasks("s1"); len(got) != 2 || got[1].ID != 8 {
		t.Fatalf("tasks %+v", got)
	}
	Hook(hookPayload(t, path, map[string]any{"hook_event_name": "SessionEnd", "cwd": "/x"}))
	for _, f := range []string{h.sessionFile("s1"), h.tasksFile("s1")} {
		if _, err := os.Stat(f); err == nil {
			t.Fatalf("%s survived SessionEnd", f)
		}
	}
}
