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
			got.Reason != s.Reason || got.Transcript != s.Transcript ||
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
		prefix := rapid.SampledFrom([]string{"", "FOO=1 ", "A='x y' B=2 ", "echo hi && ", "true;\n", "x | ", "if true; then ", "true; do ", "false; else ", "command ", "builtin ", "exec "}).Draw(t, "prefix")
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
			prefix = rapid.SampledFrom([]string{"popd; ", "pushd ~/x && ", "(cd /q); ", "{ cd /q; } && ", "( cd /q; ", "{cd /q; ", "if true; then cd /q; fi; ", "for d in /q; do cd $d; done; ", "command cd /q && ", "builtin cd /q && ", "while true; do break; done; "}).Draw(t, "group") + prefix
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

// A generated transcript: its lines, each ending in a newline, and the
// links the two link shapes make in it, in the order their results arrive.
type genTr struct {
	lines []string
	want  []TaskRef
}

func (g genTr) bytes() []byte { return []byte(strings.Join(g.lines, "")) }

var trBase = time.Date(2026, 10, 23, 17, 0, 0, 0, time.UTC)

func trLine(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}

// genTranscript draws a transcript in the shape Claude Code writes it,
// with prompts, replies, titles, Bash calls and their results (answered
// in any order, some of them errors), lines of kinds the fold ignores,
// and sidechain and meta lines.
func genTranscript() *rapid.Generator[genTr] {
	type call struct {
		id   string
		refs []TaskRef
	}
	return rapid.Custom(func(t *rapid.T) genTr {
		var g genTr
		var open []call
		n := rapid.IntRange(0, 40).Draw(t, "n")
		for i := 0; i < n; i++ {
			at := trBase.Add(time.Duration(i) * time.Second).Format("2006-01-02T15:04:05.000Z")
			cwd := rapid.SampledFrom([]string{"/w/a", "/w/b"}).Draw(t, "cwd")
			num := rapid.IntRange(1, 9).Draw(t, "num")
			ns := strconv.Itoa(num)
			var line string
			switch rapid.IntRange(0, 11).Draw(t, "kind") {
			case 0, 1: // an opening prompt, as a string or as text blocks
				var content any = "Work on p#" + ns + ": " + text().Draw(t, "title")
				if rapid.Bool().Draw(t, "blocks") {
					content = []any{map[string]any{"type": "text", "text": content}}
				}
				line = trLine(map[string]any{"type": "user", "timestamp": at, "cwd": cwd, "message": map[string]any{"role": "user", "content": content}})
				g.want = append(g.want, TaskRef{Project: "p", Dir: cwd, ID: num, Linked: mustTime(at)})
			case 2: // prompts that only mention an issue, or are not the user's
				prompt := rapid.SampledFrom([]string{"please work on p#" + ns, "Work on p#" + ns + "x", text().Draw(t, "text"), "issues start " + ns}).Draw(t, "prompt")
				line = trLine(map[string]any{"type": "user", "timestamp": at, "cwd": cwd, "message": map[string]any{"content": prompt}})
			case 3: // an opening prompt in a sidechain or a meta line
				v := map[string]any{"type": "user", "timestamp": at, "cwd": cwd, "message": map[string]any{"content": "Work on p#" + ns}}
				v[rapid.SampledFrom([]string{"isSidechain", "isMeta"}).Draw(t, "flag")] = true
				line = trLine(v)
			case 4, 5: // Bash calls
				cmd, refs := "", []TaskRef(nil)
				switch rapid.IntRange(0, 5).Draw(t, "cmd") {
				case 0:
					cmd, refs = "issues start "+ns, []TaskRef{{Dir: cwd, ID: num}}
				case 1:
					cmd, refs = "cd /z && issues start "+ns, []TaskRef{{Dir: "/z", ID: num}}
				case 2:
					cmd, refs = "issues -C /y start "+ns, []TaskRef{{Dir: "/y", ID: num}}
				case 3:
					cmd = `echo "issues start ` + ns + `"`
				case 4:
					cmd = "cat <<EOF\nissues start " + ns + "\nEOF"
				default:
					cmd = "ls -la # issues start " + ns
				}
				id := "toolu_" + strconv.Itoa(i)
				line = trLine(map[string]any{"type": "assistant", "timestamp": at, "cwd": cwd, "message": map[string]any{"content": []any{
					map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": cmd}},
				}}})
				open = append(open, call{id, refs})
			case 6, 7: // a result, in any order
				if len(open) == 0 {
					continue
				}
				k := rapid.IntRange(0, len(open)-1).Draw(t, "which")
				c := open[k]
				open = slices.Delete(open, k, k+1)
				isErr := rapid.Bool().Draw(t, "error")
				line = trLine(map[string]any{"type": "user", "timestamp": at, "message": map[string]any{"content": []any{
					map[string]any{"tool_use_id": c.id, "type": "tool_result", "content": "ok", "is_error": isErr},
				}}})
				if !isErr {
					for _, r := range c.refs {
						r.Linked = mustTime(at)
						g.want = append(g.want, r)
					}
				}
			case 8:
				line = trLine(map[string]any{"type": "assistant", "timestamp": at, "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": text().Draw(t, "reply")}}}})
			case 9:
				line = trLine(map[string]any{"type": rapid.SampledFrom([]string{"ai-title", "last-prompt"}).Draw(t, "meta"),
					"aiTitle": text().Draw(t, "title"), "lastPrompt": text().Draw(t, "last")})
			case 10: // a kind of line the fold ignores
				line = trLine(map[string]any{"type": "progress", "timestamp": at, "data": text().Draw(t, "data")})
			default:
				line = "not json at all\n"
			}
			g.lines = append(g.lines, line)
		}
		return g
	})
}

func mustTime(s string) time.Time {
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return at
}

// factsOf is what a file's lines have added up to, without the file.
func factsOf(f *tfile) Facts {
	g := f.fold
	return Facts{Title: g.title, LastPrompt: g.lastPrompt, LastReply: g.reply, ReplyAt: g.replyAt, Links: FoldTasks(g.links)}
}

// sameFacts compares facts, taking times by their instant and not by their
// representation, and leaving out the modification time.
func sameFacts(a, b Facts) bool {
	a.Modified, b.Modified = time.Time{}, time.Time{}
	if !a.ReplyAt.Equal(b.ReplyAt) || len(a.Links) != len(b.Links) {
		return false
	}
	for i := range a.Links {
		if !a.Links[i].Same(b.Links[i]) || !a.Links[i].Linked.Equal(b.Links[i].Linked) {
			return false
		}
	}
	return a.Title == b.Title && a.LastPrompt == b.LastPrompt && a.LastReply == b.LastReply
}

// splits cuts b into pieces at drawn points, which fall mid-line as often
// as not.
func splits(t *rapid.T, b []byte) [][]byte {
	var cuts []int
	for range rapid.IntRange(0, 6).Draw(t, "cuts") {
		cuts = append(cuts, rapid.IntRange(0, len(b)).Draw(t, "cut"))
	}
	slices.Sort(cuts)
	var out [][]byte
	prev := 0
	for _, c := range cuts {
		out = append(out, b[prev:c])
		prev = c
	}
	return append(out, b[prev:])
}

// Folding a transcript in pieces, cut anywhere, gives the facts that
// folding it whole gives.
func TestPropFoldChunksEqualWhole(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		g := genTranscript().Draw(t, "transcript")
		whole := newTfile(false)
		whole.feed(g.bytes())
		parts := newTfile(false)
		for _, p := range splits(t, g.bytes()) {
			parts.feed(p)
		}
		if !sameFacts(factsOf(whole), factsOf(parts)) {
			t.Fatalf("chunked:\n%+v\nwhole:\n%+v", factsOf(parts), factsOf(whole))
		}
	})
}

// Links come from the two shapes and no others: what the fold links is
// what the generator knows it wrote as a prompt or as a Bash call whose
// result was not an error. A command that only mentions `issues start`,
// an errored result, a sidechain or meta prompt link nothing.
func TestPropLinksComeOnlyFromTheTwoShapes(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		g := genTranscript().Draw(t, "transcript")
		f := newTfile(false)
		f.feed(g.bytes())
		got := factsOf(f)
		want := Facts{Links: FoldTasks(g.want)}
		if !sameFacts(Facts{Links: got.Links}, want) {
			t.Fatalf("links %+v, want %+v", got.Links, want.Links)
		}
	})
}

// writeAtomically replaces path with a new file, as an editor that
// renames over the old one does.
func writeAtomically(t *rapid.T, path string, b []byte) {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func freshFacts(t *rapid.T, path string) Facts {
	f, ok := NewTranscripts().Facts(path)
	if !ok {
		t.Fatalf("no facts for %s", path)
	}
	return f
}

// Reading the file again after each append, cut anywhere, gives the facts
// of reading the final file once.
func TestPropAppendingBetweenReadsEqualsReadingOnce(t *testing.T) {
	root, n := t.TempDir(), 0
	rapid.Check(t, func(t *rapid.T) {
		n++
		path := filepath.Join(root, fmt.Sprintf("s%d.jsonl", n))
		g := genTranscript().Draw(t, "transcript")
		idx := NewTranscripts()
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		for _, p := range splits(t, g.bytes()) {
			f.Write(p)
			if _, ok := idx.Facts(path); !ok {
				t.Fatal("no facts")
			}
		}
		got, _ := idx.Facts(path)
		if want := freshFacts(t, path); !sameFacts(got, want) {
			t.Fatalf("incremental:\n%+v\nonce:\n%+v", got, want)
		}
	})
}

// A file that shrank, or was replaced, gives the facts of its new content.
func TestPropShrunkOrReplacedFileIsReadAgain(t *testing.T) {
	root, n := t.TempDir(), 0
	rapid.Check(t, func(t *rapid.T) {
		n++
		path := filepath.Join(root, fmt.Sprintf("s%d.jsonl", n))
		a := genTranscript().Draw(t, "first").bytes()
		b := genTranscript().Draw(t, "second").bytes()
		if err := os.WriteFile(path, a, 0o644); err != nil {
			t.Fatal(err)
		}
		idx := NewTranscripts()
		idx.Facts(path)
		// Truncating in place is seen when the file got shorter than what
		// was read. Any other rewrite comes as a new file.
		if len(b) < len(a) && rapid.Bool().Draw(t, "in place") {
			if err := os.WriteFile(path, b, 0o644); err != nil {
				t.Fatal(err)
			}
		} else {
			writeAtomically(t, path, b)
		}
		got, _ := idx.Facts(path)
		if want := freshFacts(t, path); !sameFacts(got, want) {
			t.Fatalf("after the rewrite:\n%+v\nnew content read once:\n%+v", got, want)
		}
	})
}

// Subagent transcripts link their parent only through Bash calls. Their
// prompts, sidechain or not, link nothing.
func TestPropSubagentPromptsNeverLink(t *testing.T) {
	root, n := t.TempDir(), 0
	rapid.Check(t, func(t *rapid.T) {
		n++
		path := filepath.Join(root, fmt.Sprintf("s%d.jsonl", n))
		g := genTranscript().Draw(t, "main")
		if err := os.WriteFile(path, g.bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		before := freshFacts(t, path)

		dir := filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		var sub strings.Builder
		for i, p := range rapid.SliceOfN(rapid.IntRange(1, 9), 1, 6).Draw(t, "prompts") {
			sub.WriteString(trLine(map[string]any{"type": "user", "isSidechain": rapid.Bool().Draw(t, "sidechain"),
				"timestamp": trBase.Add(time.Duration(i) * time.Second).Format(time.RFC3339), "cwd": "/w/a",
				"message": map[string]any{"role": "user", "content": "Work on p#" + strconv.Itoa(p) + ": x"}}))
		}
		if err := os.WriteFile(filepath.Join(dir, "agent-1.jsonl"), []byte(sub.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		if after := freshFacts(t, path); !sameFacts(before, after) {
			t.Fatalf("subagent prompts changed the facts:\n%+v\n%+v", before, after)
		}
	})
}
