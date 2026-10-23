package fleet

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"pgregory.net/rapid"
)

// text draws strings that include the awkward cases: empty, unicode,
// whitespace runs and newlines.
func text() *rapid.Generator[string] {
	return rapid.OneOf(
		rapid.String(),
		rapid.StringMatching(`[ \t\n\r]*[a-z]{0,6}([ \t\n]+[a-z]{1,6}){0,8}[ \n]*`),
		rapid.SampledFrom([]string{"", " ", "\n", "héllo wörld", "日本語のテキスト", "emoji 🙂🙂🙂", "a b\u0085c"}),
	)
}

func genTime() *rapid.Generator[time.Time] {
	return rapid.Custom(func(t *rapid.T) time.Time {
		if rapid.Bool().Draw(t, "zero") {
			return time.Time{}
		}
		// Whole seconds in [year 1, year 9999], the range JSON can carry.
		sec := rapid.Int64Range(-62135596800, 253402300799).Draw(t, "sec")
		return time.Unix(sec, int64(rapid.IntRange(0, 999999999).Draw(t, "ns"))).UTC()
	})
}

var validStates = []State{Working, Blocked, Idle, Ended}

func genState() *rapid.Generator[State] {
	return rapid.SampledFrom(append([]State{""}, validStates...))
}

func genSession() *rapid.Generator[Session] {
	return rapid.Custom(func(t *rapid.T) Session {
		return Session{
			ID:         text().Draw(t, "id"),
			Cwd:        text().Draw(t, "cwd"),
			State:      genState().Draw(t, "state"),
			Since:      genTime().Draw(t, "since"),
			Reason:     text().Draw(t, "reason"),
			LastPrompt: text().Draw(t, "prompt"),
			Transcript: text().Draw(t, "transcript"),
		}
	})
}

var eventNames = []string{
	"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
	"Notification", "Stop", "SessionEnd", "SubagentStop", "",
}

func genEvent() *rapid.Generator[HookEvent] {
	return rapid.Custom(func(t *rapid.T) HookEvent {
		return HookEvent{
			Name:             rapid.SampledFrom(eventNames).Draw(t, "name"),
			SessionID:        text().Draw(t, "sid"),
			Cwd:              text().Draw(t, "cwd"),
			Transcript:       text().Draw(t, "transcript"),
			Prompt:           text().Draw(t, "prompt"),
			NotificationType: rapid.SampledFrom([]string{"", "idle_prompt", "auth_success", "permission_prompt"}).Draw(t, "ntype"),
			Message:          text().Draw(t, "message"),
		}
	})
}

func knownEvent(name string) bool {
	switch name {
	case "SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Notification", "Stop", "SessionEnd":
		return true
	}
	return false
}

func TestPropApplyInvariants(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := genSession().Draw(t, "start")
		now := genTime().Draw(t, "now")
		for i, ev := range rapid.SliceOfN(genEvent(), 1, 12).Draw(t, "events") {
			next, changed := Apply(s, ev, now)

			if changed != !reflect.DeepEqual(next, s) {
				t.Fatalf("event %d %q: changed=%v but next==prev is %v", i, ev.Name, changed, reflect.DeepEqual(next, s))
			}
			if !knownEvent(ev.Name) && changed {
				t.Fatalf("event %d: unknown event %q changed the session", i, ev.Name)
			}
			if next.Since != s.Since && next.State == s.State {
				t.Fatalf("event %d %q: Since moved without a state change", i, ev.Name)
			}
			if next.State != s.State && next.Since != now {
				t.Fatalf("event %d %q: state changed but Since is not now", i, ev.Name)
			}
			if next.State != s.State && !validState(next.State) {
				t.Fatalf("event %d %q: invalid state %q", i, ev.Name, next.State)
			}
			if knownEvent(ev.Name) && next.State != Blocked && next.Reason != "" {
				t.Fatalf("event %d %q: reason %q outside Blocked (%s)", i, ev.Name, next.Reason, next.State)
			}
			if utf8.RuneCountInString(next.LastPrompt) > 300 && next.LastPrompt != s.LastPrompt {
				t.Fatalf("event %d: prompt longer than 300 runes", i)
			}
			if next.State == Blocked && next.Reason != s.Reason && utf8.RuneCountInString(next.Reason) > 200 {
				t.Fatalf("event %d: reason longer than 200 runes", i)
			}
			if changed && next.ID != ev.SessionID {
				t.Fatalf("event %d: ID %q is not the event's %q", i, next.ID, ev.SessionID)
			}

			// Replaying the same event later is a no-op.
			again, changedAgain := Apply(next, ev, now.Add(time.Minute))
			if changedAgain || !reflect.DeepEqual(again, next) {
				t.Fatalf("event %d %q: second application changed %+v -> %+v", i, ev.Name, next, again)
			}
			s = next
		}
	})
}

func validState(s State) bool {
	for _, v := range validStates {
		if s == v {
			return true
		}
	}
	return false
}

func TestPropClip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := text().Draw(t, "s")
		n := rapid.IntRange(1, 50).Draw(t, "n")
		got := clip(s, n)

		if utf8.RuneCountInString(got) > n {
			t.Fatalf("clip(%q, %d) = %q is longer than %d runes", s, n, got, n)
		}
		for _, r := range got {
			if unicode.IsSpace(r) && r != ' ' {
				t.Fatalf("clip(%q, %d) = %q keeps whitespace %U", s, n, got, r)
			}
		}
		if got != strings.TrimSpace(got) || strings.Contains(got, "  ") {
			t.Fatalf("clip(%q, %d) = %q has stray spaces", s, n, got)
		}
		if clip(got, n) != got {
			t.Fatalf("clip is not idempotent: %q -> %q", got, clip(got, n))
		}
		flat := strings.Join(strings.Fields(s), " ")
		if utf8.RuneCountInString(flat) <= n {
			if got != flat {
				t.Fatalf("short input altered: %q -> %q", flat, got)
			}
		} else if utf8.RuneCountInString(got) != n || !strings.HasSuffix(got, "…") {
			t.Fatalf("long input %q should end in an ellipsis at %d runes, got %q", flat, n, got)
		}
	})
}

func genLive() *rapid.Generator[Live] {
	return rapid.Custom(func(t *rapid.T) Live {
		return Live{
			Status:  rapid.SampledFrom([]string{"", "idle", "busy", "working", "waiting"}).Draw(t, "status"),
			BgState: rapid.SampledFrom([]string{"", "working", "blocked", "idle"}).Draw(t, "bg"),
		}
	})
}

// Precedence is pinned by TestLiveStatePrecedence. This checks what holds
// for every input: the state is one of three, a reason only comes with a
// block, and the time is either the record's or unknown.
func TestPropLiveState(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		l, rec := genLive().Draw(t, "live"), genSession().Draw(t, "rec")
		st, since, reason := liveState(l, rec)
		if st != Working && st != Blocked && st != Idle {
			t.Fatalf("liveState returned %q", st)
		}
		if st != Blocked && reason != "" {
			t.Fatalf("reason %q on a %s row", reason, st)
		}
		if !since.IsZero() && !since.Equal(rec.Since) {
			t.Fatalf("since %v is neither zero nor the record's %v", since, rec.Since)
		}
	})
}

func TestPropAsking(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := Row{
			State:     genState().Draw(t, "state"),
			LastReply: text().Draw(t, "reply"),
		}
		if r.Asking() && r.State != Idle {
			t.Fatalf("a %s row cannot be asking", r.State)
		}
		if r.State == Working && r.NeedsYou() {
			t.Fatal("a working row needs nobody")
		}

		// A reply that ends in a question, then only closing markup, asks.
		tail := rapid.StringMatching("[ \n\t*_`)]*").Draw(t, "tail")
		q := Row{State: Idle, LastReply: r.LastReply + "?" + tail}
		if !q.Asking() || !q.NeedsYou() {
			t.Fatalf("idle reply %q ends in a question but is not asking", q.LastReply)
		}
		q.State = Working
		if q.Asking() {
			t.Fatal("working row asking")
		}
	})
}

// The table below pins edges the property reaches only by chance.
func TestAskingEdges(t *testing.T) {
	for _, c := range []struct {
		reply string
		want  bool
	}{
		{"", false},
		{"Shall I go on?", true},
		{"Shall I go on? ", true},
		{"**Shall I go on?**", true},
		{"(maybe?)", true},
		{"Done.", false},
		{"Why? Because.", false},
	} {
		if got := (Row{State: Idle, LastReply: c.reply}).Asking(); got != c.want {
			t.Errorf("Asking(%q) = %v, want %v", c.reply, got, c.want)
		}
	}
}

// ids that try to leave the sessions directory, plus arbitrary text.
func genID() *rapid.Generator[string] {
	return rapid.OneOf(
		text(),
		rapid.SampledFrom([]string{"", ".", "..", "/", "../x", "../../x", "a/b", "/abs/path", `..\x`, "x/..", "...", ".json"}),
		rapid.StringMatching(`(\.\./|/|\./)*[a-z]{0,5}(/\.\.)*`),
	)
}

// newHome gives each generated case its own home under root.
func newHome(root string, n *int) Home {
	*n++
	return Home{Dir: filepath.Join(root, fmt.Sprintf("case%d", *n), "home")}
}

func TestPropSessionRoundTrip(t *testing.T) {
	root, n := t.TempDir(), 0
	rapid.Check(t, func(t *rapid.T) {
		h := newHome(root, &n)
		s := genSession().Draw(t, "session")
		s.ID = strings.ReplaceAll(s.ID, "\x00", "")
		if err := h.saveSession(s); err != nil {
			t.Skipf("save refused id %q: %v", s.ID, err)
		}
		got := h.loadSession(s.ID)
		if got.ID != s.ID || got.Cwd != s.Cwd || got.State != s.State ||
			got.Reason != s.Reason || got.LastPrompt != s.LastPrompt || got.Transcript != s.Transcript ||
			!got.Since.Equal(s.Since) {
			t.Fatalf("round trip changed the session:\n saved  %+v\n loaded %+v", s, got)
		}
		if s.ID != "" {
			if listed, ok := h.Sessions()[s.ID]; !ok || listed.State != s.State {
				t.Fatalf("Sessions() lost %q", s.ID)
			}
		}
	})
}

func TestPropSessionIDStaysInsideSessionsDir(t *testing.T) {
	root, n := t.TempDir(), 0
	rapid.Check(t, func(t *rapid.T) {
		h := newHome(root, &n)
		parent := filepath.Dir(h.Dir)
		id := strings.ReplaceAll(genID().Draw(t, "id"), "\x00", "")

		if dir := filepath.Dir(h.sessionFile(id)); dir != h.sessionsDir() {
			t.Fatalf("sessionFile(%q) is in %s, not %s", id, dir, h.sessionsDir())
		}
		if err := os.MkdirAll(parent, 0o755); err != nil {
			t.Fatal(err)
		}
		_ = h.saveSession(Session{ID: id, State: Idle})
		_ = h.loadSession(id)

		// Nothing beside the home, and nothing inside it but files in the
		// sessions directory.
		entries, _ := os.ReadDir(parent)
		if len(entries) > 1 || (len(entries) == 1 && entries[0].Name() != "home") {
			t.Fatalf("id %q wrote next to the home: %v", id, entries)
		}
		err := filepath.WalkDir(h.Dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || p == h.Dir || p == h.dataDir() || p == h.sessionsDir() {
				return err
			}
			if d.IsDir() || filepath.Dir(p) != h.sessionsDir() {
				t.Fatalf("id %q created %s", id, p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

// quote wraps s so splitCommands reads it back as one word.
func quote(t *rapid.T, s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\"'\\;&|$`") && rapid.Bool().Draw(t, "bare") {
		return s
	}
	if !strings.Contains(s, "'") && rapid.Bool().Draw(t, "single") {
		return "'" + s + "'"
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func genDirWord() *rapid.Generator[string] {
	return rapid.StringMatching(`[a-z]{1,5}([ ;&|][a-z]{1,4}){0,2}`)
}

// A well-formed invocation, built from drawn parts, reads back as the ref
// it was built from.
func TestPropIssuesStartsRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cwd := "/" + rapid.StringMatching(`[a-z]{1,6}(/[a-z]{1,6}){0,2}`).Draw(t, "cwd")
		id := rapid.IntRange(1, 1_000_000).Draw(t, "id")
		want := TaskRef{Dir: cwd, ID: id}
		words := []string{"issues"}
		if rapid.Bool().Draw(t, "bin") {
			words[0] = "/opt/bin/issues"
		}
		if rapid.Bool().Draw(t, "json") {
			words = append(words, "--json")
		}
		if rapid.Bool().Draw(t, "project") {
			want.Project = rapid.StringMatching(`[a-z][a-z0-9.-]{0,8}`).Draw(t, "p")
			if rapid.Bool().Draw(t, "eq") {
				words = append(words, "--project="+want.Project)
			} else {
				words = append(words, "--project", quote(t, want.Project))
			}
		}
		if rapid.Bool().Draw(t, "dir") {
			rel := genDirWord().Draw(t, "dirword")
			abs := "/" + rel
			dir, flag := abs, rapid.SampledFrom([]string{"-C", "--dir"}).Draw(t, "flag")
			want.Dir = abs
			if rapid.Bool().Draw(t, "relative") {
				dir, want.Dir = rel, filepath.Join(cwd, rel)
			}
			words = append(words, flag, quote(t, dir))
		}
		words = append(words, "start", strconv.Itoa(id))
		prefix := rapid.SampledFrom([]string{"", "FOO=1 ", "A='x y' B=2 ", "echo hi && ", "true;\n", "x | "}).Draw(t, "prefix")
		suffix := rapid.SampledFrom([]string{"", " && echo done", " | cat", " &", "\necho x"}).Draw(t, "suffix")

		// Something before the start can move the directory or make it
		// unknown, or comment the start out.
		var dirAbs, dirRel bool
		for i, w := range words {
			if w == "-C" || w == "--dir" {
				dirAbs = strings.HasPrefix(words[i+1], "/") || strings.HasPrefix(words[i+1], "'/") || strings.HasPrefix(words[i+1], `"/`)
				dirRel = !dirAbs
			}
		}
		lost := false
		switch before := rapid.SampledFrom([]string{"", "pushd", "unknown", "comment"}).Draw(t, "before"); before {
		case "pushd":
			to := "/" + rapid.StringMatching(`[a-z]{1,6}`).Draw(t, "to")
			prefix = "pushd " + to + " && " + prefix
			switch {
			case dirRel:
				rel, _ := filepath.Rel(cwd, want.Dir)
				want.Dir = filepath.Join(to, rel)
			case !dirAbs:
				want.Dir = to
			}
		case "unknown":
			prefix = rapid.SampledFrom([]string{"popd; ", "pushd ~/x && ", "(cd /q); ", "{ cd /q; } && ", "( cd /q; ", "{cd /q; "}).Draw(t, "group") + prefix
			lost = !dirAbs
		case "comment":
			// The comment runs to the end of its line only.
			lost = !strings.Contains(prefix, "\n")
			prefix = "echo x # see " + prefix
		}

		cmd := prefix + strings.Join(words, " ") + suffix
		got := IssuesStarts(cmd, cwd)
		if lost {
			if len(got) != 0 {
				t.Fatalf("IssuesStarts(%q) = %+v, want nothing", cmd, got)
			}
			return
		}
		if len(got) != 1 || got[0] != want {
			t.Fatalf("IssuesStarts(%q) = %+v, want [%+v]", cmd, got, want)
		}
	})
}

func TestPropParsersNeverPanic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.OneOf(
			text(),
			rapid.StringMatching(`[a-z ='"\;&|\n$~/#.:0-9-]{0,40}`),
			rapid.SampledFrom([]string{`issues start 3`, `issues -C "`, `Work on a#1`, `\`, `'`, `"\`}),
		).Draw(t, "s")
		cwd := text().Draw(t, "cwd")
		for _, r := range IssuesStarts(s, cwd) {
			if r.ID <= 0 {
				t.Fatalf("IssuesStarts(%q) gave id %d", s, r.ID)
			}
		}
		if r, ok := OpeningTask(s, cwd); ok && (r.ID <= 0 || r.Project == "" || r.Dir != cwd) {
			t.Fatalf("OpeningTask(%q) = %+v", s, r)
		}
		_ = splitCommands(s)
	})
}

// OpeningTask accepts exactly the form overview writes.
func TestPropOpeningTaskRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		project := rapid.StringMatching(`[^\s#]{1,12}`).Filter(func(p string) bool {
			return !strings.ContainsFunc(p, unicode.IsSpace)
		}).Draw(t, "project")
		id := rapid.IntRange(1, 1_000_000).Draw(t, "id")
		lead := rapid.StringMatching(`[ \t\n]{0,3}`).Draw(t, "lead")
		tail := rapid.SampledFrom([]string{"", ": Title", " more", ".", "\nmore"}).Draw(t, "tail")
		cwd := "/" + rapid.StringMatching(`[a-z]{1,6}`).Draw(t, "cwd")
		prompt := lead + "Work on " + project + "#" + strconv.Itoa(id) + tail
		got, ok := OpeningTask(prompt, cwd)
		if !ok || got != (TaskRef{Project: project, Dir: cwd, ID: id}) {
			t.Fatalf("OpeningTask(%q) = %+v, %v", prompt, got, ok)
		}
	})
}

func genTaskEvent() *rapid.Generator[HookEvent] {
	return rapid.Custom(func(t *rapid.T) HookEvent {
		id := strconv.Itoa(rapid.IntRange(1, 12).Draw(t, "id"))
		switch rapid.IntRange(0, 2).Draw(t, "kind") {
		case 0:
			return HookEvent{Name: "UserPromptSubmit", Cwd: rapid.SampledFrom([]string{"", "/a", "/b"}).Draw(t, "cwd"),
				Prompt: "Work on " + rapid.SampledFrom([]string{"p", "q"}).Draw(t, "proj") + "#" + id}
		case 1:
			cmd := rapid.SampledFrom([]string{"issues start ", "cd /z && issues start ", "issues -C /y start ", "issues --project p start ", "ls "}).Draw(t, "cmd") + id
			in, _ := json.Marshal(map[string]string{"command": cmd})
			return HookEvent{Name: "PostToolUse", ToolName: "Bash", ToolInput: in, Cwd: rapid.SampledFrom([]string{"", "/a"}).Draw(t, "cwd")}
		}
		return genEvent().Draw(t, "any")
	})
}

func genRef() *rapid.Generator[TaskRef] {
	return rapid.Custom(func(t *rapid.T) TaskRef {
		r := TaskRef{Project: rapid.SampledFrom([]string{"", "p"}).Draw(t, "project"),
			Dir: rapid.SampledFrom([]string{"/a", "/b"}).Draw(t, "dir"), ID: rapid.IntRange(1, 12).Draw(t, "id")}
		if rapid.Bool().Draw(t, "timed") {
			r.Linked = time.Unix(1_700_000_000+int64(rapid.IntRange(0, 100).Draw(t, "at")), 0).UTC()
		}
		return r
	})
}

// Appending what TasksToAppend says, batch by batch, keeps the folded
// list short, each ref once, the last ref linked last, and a second
// append of the same batch adds nothing.
func TestPropTasksLog(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var lines []TaskRef
		now := time.Unix(1_800_000_000, 0).UTC()
		for i, batch := range rapid.SliceOfN(rapid.SliceOfN(genRef(), 0, 4), 1, 20).Draw(t, "batches") {
			lines = append(lines, TasksToAppend(FoldTasks(lines), batch, now)...)
			cur := FoldTasks(lines)
			if len(cur) > maxTasks {
				t.Fatalf("batch %d: %d tasks", i, len(cur))
			}
			for j, r := range cur {
				if slices.IndexFunc(cur, r.Same) != j {
					t.Fatalf("batch %d: duplicate %+v in %+v", i, r, cur)
				}
			}
			if len(batch) > 0 && !cur[len(cur)-1].Same(batch[len(batch)-1]) {
				t.Fatalf("batch %d: last ref %+v is not last in %+v", i, batch[len(batch)-1], cur)
			}
			if len(batch) > 0 {
				if again := TasksToAppend(cur, batch[len(batch)-1:], now); len(again) != 0 {
					t.Fatalf("batch %d: linking the last ref again appends %+v", i, again)
				}
			}
			// Folding what it folded to changes nothing.
			if !reflect.DeepEqual(FoldTasks(cur), cur) {
				t.Fatalf("batch %d: folding is not idempotent: %+v", i, cur)
			}
		}
	})
}

// Appending what TasksToAppend returns, then asking again with the same
// refs, returns nothing, and the file folds to what appending every ref
// would have folded to, ignoring times.
func TestPropTasksToAppendMinimal(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		base := rapid.SliceOfN(genRef(), 0, 12).Draw(t, "base")
		batch := rapid.SliceOfN(genRef(), 0, 12).Draw(t, "batch")
		now := time.Unix(1_800_000_000, 0).UTC()
		cur := FoldTasks(base)
		add := TasksToAppend(cur, batch, now)
		lines := append(slices.Clone(base), add...)
		if again := TasksToAppend(FoldTasks(lines), batch, now.Add(time.Hour)); len(again) != 0 {
			t.Fatalf("second call appends %+v", again)
		}
		all := FoldTasks(append(slices.Clone(base), batch...))
		if got := FoldTasks(lines); !reflect.DeepEqual(refs(got), refs(all)) {
			t.Fatalf("folds to %+v, appending every ref gives %+v", got, all)
		}
	})
}

// Two hooks that read the file at once and append their own links, in
// any interleaving of their lines, leave both links in it, and a ref
// linked before keeps its first time.
func TestPropConcurrentLinksKeepBoth(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		base := rapid.SliceOfN(genRef(), 0, 6).Draw(t, "base")
		cur := FoldTasks(base)
		a := rapid.SliceOfN(genRef(), 1, 3).Draw(t, "a")
		b := rapid.SliceOfN(genRef(), 1, 3).Draw(t, "b")
		now := time.Unix(1_800_000_000, 0).UTC()
		la, lb := TasksToAppend(cur, a, now), TasksToAppend(cur, b, now.Add(time.Second))
		var lines []TaskRef
		i, j := 0, 0
		for i < len(la) || j < len(lb) {
			if j == len(lb) || i < len(la) && rapid.Bool().Draw(t, "a first") {
				lines, i = append(lines, la[i]), i+1
			} else {
				lines, j = append(lines, lb[j]), j+1
			}
		}
		got := FoldTasks(append(slices.Clone(base), lines...))
		for _, r := range append(slices.Clone(a), b...) {
			k := slices.IndexFunc(got, r.Same)
			if k < 0 {
				t.Fatalf("%+v was dropped: %+v", r, got)
			}
			if c := slices.IndexFunc(cur, r.Same); c >= 0 && !cur[c].Linked.IsZero() && !got[k].Linked.Equal(cur[c].Linked) {
				t.Fatalf("%+v lost its first time %v: %+v", r, cur[c].Linked, got[k])
			}
		}
	})
}

// Events never change the state through linking: Apply does not know about
// tasks at all, and EventTasks is pure.
func TestPropEventTasksAndApply(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := Session{}
		now := time.Unix(1_700_000_000, 0).UTC()
		for i, ev := range rapid.SliceOfN(genTaskEvent(), 1, 30).Draw(t, "events") {
			ev.SessionID = "s"
			a, b := EventTasks(ev, s.Cwd), EventTasks(ev, s.Cwd)
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("event %d: EventTasks is not deterministic", i)
			}
			applied, changed := Apply(s, ev, now)
			if changed != (applied != s) {
				t.Fatalf("event %d: changed=%v for %+v -> %+v", i, changed, s, applied)
			}
			if _, again := Apply(applied, ev, now); again && knownEvent(ev.Name) {
				t.Fatalf("event %d: Apply is not idempotent", i)
			}
			s = applied
		}
	})
}
