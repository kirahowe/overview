package fleet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The starts the reader missed in three weeks of real transcripts, each as
// its session wrote it, with the user's home as /u.
func TestIssuesStartsAsSessionsWriteThem(t *testing.T) {
	for _, c := range []struct {
		name, cwd, cmd string
		want           []TaskRef
	}{
		{"a directory held in a variable", "/u/code/projects/horizon/.claude/worktrees/explore-viz",
			`rm crates/viz/tests/grammar.proptest-regressions && git status --short && W=$PWD && issues --dir $W start 59 >/dev/null && node tools/factory/cli.ts record add --issue 59 --event agent --note "Step 1: chart_holding in crates/viz."`,
			[]TaskRef{{ID: 59}}},
		{"a sweep from a worktree", "/u/code/projects/horizon",
			`cd /u/code/projects/horizon/.claude/worktrees/sweep && grep -n '"deep"\|"gate"\|"factory"' package.json; W=$PWD; issues --dir $W done 2 && issues --dir $W drop 18 && issues --dir $W done 19 && issues --dir $W start 10 && issues --dir $W start 29 && issues --dir $W set 16 status ready; issues --dir $W doctor --json | head -30; git status --short | head -30`,
			[]TaskRef{{ID: 10}, {ID: 29}}},
		{"a function for issues", "/u/code/projects/relay",
			`i() { issues --dir . "$@" >/dev/null || echo "FAILED: $*"; }; i set 94 status ready; i start 94; i set 96 status ready; i start 96; i set 93 status ready; i start 93; i set 98 status ready; i block 95 --on 94; i block 97 --on 96; i block 97 --on 98; issues --dir . doctor --json | head -12`,
			[]TaskRef{{Dir: "/u/code/projects/relay", ID: 94}, {Dir: "/u/code/projects/relay", ID: 96}, {Dir: "/u/code/projects/relay", ID: 93}}},
		// zsh does not split $I into words, so this ran nothing: the
		// session went on to the function above.
		{"a variable for issues", "/u/code/projects/relay",
			`set -e; I="issues --dir ."; $I set 94 status ready >/dev/null; $I start 94 >/dev/null; $I doctor --json | head -20`,
			nil},
		{"a workspace under the home", "/u/code/projects/relay",
			`cd ~/code/projects/.cards/relay && issues --dir . start 55 >/dev/null && issues --dir . details 55 && cat "$(issues --dir . details 55)"`,
			[]TaskRef{{Dir: "~/code/projects/.cards/relay", ID: 55}}},
		{"a directory beside a workspace under the home", "/u/code/projects/relay",
			`cd ~/code/projects/.next/t35 && issues --dir relay start 35 2>&1 | tail -1; grep -E '^[✓✗]' test.log | sort -rn | head -25; sysctl -n hw.ncpu`,
			[]TaskRef{{Dir: "~/code/projects/.next/t35/relay", ID: 35}}},
		{"after a loop", "/u/code/projects/relay",
			`cd ~/code/projects/.next/relay-board && for i in 73 74 75 77; do issues --dir . review $i >/dev/null; done; issues --dir . start 79 >/dev/null 2>&1; issues --dir . doctor --json 2>&1 | tail -3`,
			[]TaskRef{{ID: 79}}},
	} {
		if got := IssuesStarts(c.cmd, c.cwd); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	got := HomeDirs([]TaskRef{{Dir: "~/code/x", ID: 1}, {Dir: "~", ID: 2}, {Dir: "/w/a", ID: 3}, {ID: 4}, {Project: "p", Dir: "~/y", ID: 5}}, "/u")
	want := []TaskRef{{Dir: "/u/code/x", ID: 1}, {Dir: "/u", ID: 2}, {Dir: "/w/a", ID: 3}, {ID: 4}, {Project: "p", Dir: "/u/y", ID: 5}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("HomeDirs %+v, want %+v", got, want)
	}
	// With no home to put it under, a directory written from ~ is unknown.
	if got := HomeDirs([]TaskRef{{Dir: "~/code/x", ID: 1}, {Dir: "/w/a", ID: 3}}, ""); !reflect.DeepEqual(got, []TaskRef{{ID: 1}, {Dir: "/w/a", ID: 3}}) {
		t.Errorf("HomeDirs with no home: %+v", got)
	}
	if got := HomeDirs(nil, "/u"); got != nil {
		t.Errorf("HomeDirs of nothing: %+v", got)
	}
}

func TestIssuesStarts(t *testing.T) {
	const cwd = "/work/app"
	r := func(project, dir string, id int) TaskRef { return TaskRef{Project: project, Dir: dir, ID: id} }
	// nowhere is a start the reader could not place: the issue, and no
	// directory.
	nowhere := func(id int) []TaskRef { return []TaskRef{{ID: id}} }
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
		{"cd home", "cd ~/x && issues start 2", []TaskRef{r("", "~/x", 2)}},
		{"cd home alone", "cd ~; issues start 2", []TaskRef{r("", "~", 2)}},
		{"cd home then absolute dir", "cd ~/x && issues --dir /d start 2", []TaskRef{r("", "/d", 2)}},
		{"cd home then relative dir", "cd ~/x && issues --dir d start 2", []TaskRef{r("", "~/x/d", 2)}},
		{"cd home then up", "cd ~/x/y && cd .. && issues start 2", []TaskRef{r("", "~/x", 2)}},
		{"cd out of home", "cd ~/x && cd ../.. && issues start 2", nowhere(2)},
		{"cd another user's home", "cd ~sam/x && issues start 2", nowhere(2)},
		{"cd with no argument goes home", "cd && issues start 2", []TaskRef{r("", "~", 2)}},
		{"cd with no argument then relative", "cd; cd code/x; issues start 2", []TaskRef{r("", "~/code/x", 2)}},
		{"cd -", "cd - && issues start 2", nowhere(2)},
		{"cd variable", "cd $HOME/x; issues start 2", nowhere(2)},
		{"cd unknown stays unknown", "cd $X; cd /a; issues start 2", nowhere(2)},
		{"cd unknown covers later commands only", "issues start 1; cd -; issues start 2", []TaskRef{r("", cwd, 1), {ID: 2}}},
		{"pushd another dir", "pushd /other && issues start 5", []TaskRef{r("", "/other", 5)}},
		{"pushd relative", "pushd sub; issues start 5", []TaskRef{r("", "/work/app/sub", 5)}},
		{"pushd home", "pushd ~/x && issues start 5", []TaskRef{r("", "~/x", 5)}},
		{"pushd with no argument", "pushd && issues start 5", nowhere(5)},
		{"pushd +n", "pushd +1 && issues start 5", nowhere(5)},
		{"popd", "pushd /other && popd && issues start 5", nowhere(5)},
		{"brace group", "{ cd /other; issues start 5; }", nowhere(5)},
		{"brace group glued", "{cd /other; issues start 5; }", nowhere(5)},
		{"subshell", "(cd /other; issues start 5 )", nowhere(5)},
		{"subshell spaced", "( cd /other; issues start 5 )", nowhere(5)},
		{"after a subshell", "(cd /other); issues start 5", nowhere(5)},
		{"group then absolute dir", "(cd /other); issues -C /d start 5", []TaskRef{r("", "/d", 5)}},
		{"group then home dir", "(cd /other); issues -C ~/d start 5", []TaskRef{r("", "~/d", 5)}},
		{"group then relative dir", "(cd /other); issues -C d start 5", nowhere(5)},
		{"group then a project", "(cd /other); issues --project p start 5", []TaskRef{r("p", "", 5)}},
		{"cd in an if", "if true; then cd ../b; fi; issues start 3", nowhere(3)},
		{"cd in a for", "for d in ../b; do cd $d; done; issues start 3", nowhere(3)},
		{"a loop with no cd", "for i in 1 2; do issues --dir . review $i; done; issues --dir . start 3", nowhere(3)},
		{"command cd", "command cd ../b && issues start 3", nowhere(3)},
		{"builtin cd", "builtin cd ../b && issues start 3", nowhere(3)},
		{"command cd then absolute dir", "command cd ../b && issues -C /d start 3", []TaskRef{r("", "/d", 3)}},
		{"eval cd", `eval "cd ../b"; issues start 3`, nowhere(3)},
		{"source script", "source env.sh; issues start 3", nowhere(3)},
		{"cd as a word", "env cd ../b; issues start 3", nowhere(3)},
		{"command issues", "command issues start 3", []TaskRef{r("", cwd, 3)}},
		{"exec issues", "exec issues start 3", []TaskRef{r("", cwd, 3)}},
		{"command issues after unknown cd", "cd -; command issues start 3", nowhere(3)},
		{"issues behind then", "if true; then issues start 3; fi", []TaskRef{r("", cwd, 3)}},
		{"issues behind do", "true; do issues start 3; done", []TaskRef{r("", cwd, 3)}},
		{"issues behind else", "false; else issues start 3; fi", []TaskRef{r("", cwd, 3)}},
		{"comment hides what follows", "echo hi # note && issues start 5", nil},
		{"comment ends at the line", "echo hi # note\nissues start 5", []TaskRef{r("", cwd, 5)}},
		{"comment after a start", "issues start 5 # take it", []TaskRef{r("", cwd, 5)}},
		{"hash inside a word", "echo a#b && issues start 5", []TaskRef{r("", cwd, 5)}},
		{"quoted hash", `echo "# x" && issues start 5`, []TaskRef{r("", cwd, 5)}},
		{"comment line", "# issues start 4\nissues start 5", []TaskRef{r("", cwd, 5)}},
		{"home dir", "issues -C ~/x start 2", []TaskRef{r("", "~/x", 2)}},
		{"home dir long flag", "issues --dir ~/code/other/ start 5", []TaskRef{r("", "~/code/other", 5)}},
		{"home itself", "issues --dir ~ start 5", []TaskRef{r("", "~", 5)}},
		{"another user's home dir", "issues --dir ~sam/x start 5", nowhere(5)},
		{"home dir with a variable", "issues --dir ~/$P start 5", nowhere(5)},
		{"variable dir", "issues --dir $HOME/x start 5", nowhere(5)},
		{"variable dir set from the directory", "W=$PWD; issues --dir $W start 5", nowhere(5)},
		{"variable dir keeps its project", "issues --dir $W --project p start 5", []TaskRef{r("p", "", 5)}},
		{"substitution dir", "issues --dir=$(pwd) start 5", nowhere(5)},
		{"backtick dir", "issues --dir `pwd` start 5", nowhere(5)},
		{"empty dir", `issues --dir "" start 5`, nowhere(5)},
		{"an unplaced ref does not hide the next", "issues --dir $W start 5; issues start 6", []TaskRef{{ID: 5}, r("", cwd, 6)}},
		{"variable project gives nothing", "issues --project $P start 5", nil},
		{"variable command", `I="issues --dir ."; $I start 5`, nil},
		{"function", `i() { issues --dir . "$@"; }; i start 5`, []TaskRef{r("", cwd, 5)}},
		{"function called twice", `i() { issues "$@" >/dev/null || echo "FAILED: $*"; }; i set 4 status ready; i start 4; i start 5`, []TaskRef{r("", cwd, 4), r("", cwd, 5)}},
		{"function glued head", `i(){ issues "$@"; }; i start 5`, []TaskRef{r("", cwd, 5)}},
		{"function spaced head", `i () { issues "$@"; }; i start 5`, []TaskRef{r("", cwd, 5)}},
		{"function keyword", `function i { issues "$@"; }; i start 5`, []TaskRef{r("", cwd, 5)}},
		{"function keyword and parens", `function i() { issues "$@"; }; i start 5`, []TaskRef{r("", cwd, 5)}},
		{"function over lines", "i() {\n  issues -C /d \"$@\"\n}\ni start 5", []TaskRef{r("", "/d", 5)}},
		{"function closed on its last command", `i() { issues "$@" }; i start 5`, []TaskRef{r("", cwd, 5)}},
		{"function with braced parameters", `i() { issues start "${1}"; }; i 5`, []TaskRef{r("", cwd, 5)}},
		{"function with a numbered parameter", `go() { issues -C "$2" start "$1"; }; go 5 /d`, []TaskRef{r("", "/d", 5)}},
		{"function with a missing parameter", `go() { issues start $1; }; go`, nil},
		{"function that takes no arguments", `go() { issues start 5; }; go`, []TaskRef{r("", cwd, 5)}},
		{"function never called", `go() { issues start 5; }`, nil},
		{"function never called, over lines", "go() {\n  issues start 5\n}\necho hi", nil},
		{"function defined after the call", `i start 5; i() { issues "$@"; }`, nil},
		{"function defined again", `i() { echo "$@"; }; i() { issues "$@"; }; i start 5`, []TaskRef{r("", cwd, 5)}},
		{"function that moves", `go() { cd /other; }; go; issues start 5`, []TaskRef{r("", "/other", 5)}},
		{"function that moves somewhere unknown", `go() { cd "$1"; }; go $X; issues start 5`, nowhere(5)},
		{"function that only wraps issues does not move", `i() { issues "$@"; }; i show 3; issues start 5`, []TaskRef{r("", cwd, 5)}},
		{"function named issues", `issues() { command issues -C /d "$@"; }; issues start 5`, []TaskRef{r("", "/d", 5)}},
		{"function calling a function", `a() { issues "$@"; }; b() { a start "$1"; }; b 5`, []TaskRef{r("", cwd, 5)}},
		{"function calling itself ends the reading", `a() { a "$@"; issues start 5; }; a; issues start 6`, nil},
		{"start before a function calling itself", `issues start 4; a() { a; }; a; issues start 6`, []TaskRef{r("", cwd, 4)}},
		{"function behind command is not called", `i() { issues "$@"; }; command i start 5`, nil},
		{"function with a group inside ends the reading", `go() { { cd /x; }; issues start 5; }; go; issues start 6`, nil},
		{"function never closed ends the reading", `go() { issues start 5; issues start 6`, nil},
		{"start before an unreadable function", `issues start 4; go() { { cd /x; }; }; issues start 6`, []TaskRef{r("", cwd, 4)}},
		{"a word ending in a brace is no function", `echo x{ ; issues start 5`, []TaskRef{r("", cwd, 5)}},
		{"an argument that looks like a function head", `grep -n 'main(){' a.js; issues start 5`, []TaskRef{r("", cwd, 5)}},
		{"a quoted function head", `echo 'f() {'; issues start 5`, []TaskRef{r("", cwd, 5)}},
		{"a function head split over arguments", `grep 'run()' '{' a.sh; issues start 5`, []TaskRef{r("", cwd, 5)}},
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
	if got := IssuesStarts("issues -C rel start 3", ""); !reflect.DeepEqual(got, nowhere(3)) {
		t.Errorf("relative dir with no cwd: %+v", got)
	}
	if got := IssuesStarts("issues -C rel start 3", "rel/cwd"); !reflect.DeepEqual(got, nowhere(3)) {
		t.Errorf("relative dir with a relative cwd: %+v", got)
	}
	if got := IssuesStarts("issues start 3", "rel/cwd"); !reflect.DeepEqual(got, nowhere(3)) {
		t.Errorf("a relative cwd: %+v", got)
	}
	if got := IssuesStarts("cd /a && issues start 3", ""); !reflect.DeepEqual(got, []TaskRef{{Dir: "/a", ID: 3}}) {
		t.Errorf("an absolute cd with no cwd: %+v", got)
	}
	if got := IssuesStarts("cd x && issues start 3", "~/code"); !reflect.DeepEqual(got, []TaskRef{{Dir: "~/code/x", ID: 3}}) {
		t.Errorf("a cwd written from home: %+v", got)
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

// refs is rs without their link times.
func refs(rs []TaskRef) []TaskRef {
	out := make([]TaskRef, len(rs))
	for i, r := range rs {
		r.Linked = time.Time{}
		out[i] = r
	}
	return out
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

func TestHookWritesOnlyOnChangeAndNothingForSubagents(t *testing.T) {
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
	Hook(hookPayload(t, path, bash("issues start 7", "sub")))
	unchanged("a subagent's tool call")
	Hook(hookPayload(t, path, bash("issues start 7", "")))
	unchanged("a tool call that changes no state")
	Hook(hookPayload(t, path, map[string]any{"hook_event_name": "UserPromptSubmit", "cwd": "/x", "prompt": "another"}))
	unchanged("a second prompt in the same state")

	// The hook writes the record and nothing beside it.
	entries, _ := os.ReadDir(h.sessionsDir())
	if len(entries) != 1 || entries[0].Name() != "s1.json" {
		t.Fatalf("sessions dir holds %v", entries)
	}
	// A subagent of a session with no record makes none.
	Hook(strings.NewReader(`{"hook_event_name":"PostToolUse","session_id":"nobody","agent_id":"a","cwd":"/x","transcript_path":"` + filepath.Join(h.Dir, "projects", "-x", "nobody.jsonl") + `"}`))
	if _, ok := h.Sessions()["nobody"]; ok {
		t.Fatal("a subagent created a session record")
	}
	Hook(hookPayload(t, path, map[string]any{"hook_event_name": "SessionEnd", "cwd": "/x"}))
	if _, err := os.Stat(file); err == nil {
		t.Fatal("record survived SessionEnd")
	}
}
