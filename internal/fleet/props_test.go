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

// A limit only ever turns a session blocked, never working or idle, and
// leaves a working one and any reason already given alone.
func TestPropLimited(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		base := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
		moment := func(label string) time.Time {
			if rapid.Bool().Draw(t, label+"-zero") {
				return time.Time{}
			}
			return base.Add(time.Duration(rapid.IntRange(0, 100).Draw(t, label)) * time.Minute)
		}
		st := rapid.SampledFrom([]State{Working, Blocked, Idle}).Draw(t, "state")
		since := moment("since")
		reason := ""
		if st == Blocked {
			reason = rapid.SampledFrom([]string{"", "Bash"}).Draw(t, "reason")
		}
		var lim Limit
		if rapid.Bool().Draw(t, "limit") {
			lim = Limit{Text: "out", At: base.Add(time.Minute), Resets: moment("resets")}
		}
		now := moment("now")
		got, at, why := limited(st, since, reason, lim, now)
		switch {
		case got != st && !(st == Idle && got == Blocked):
			t.Fatalf("a limit turned %s into %s", st, got)
		case st == Working && (at != since || why != reason):
			t.Fatal("a working session changed")
		case reason != "" && why != reason:
			t.Fatalf("reason %q replaced by %q", reason, why)
		case got == Blocked && why == "":
			if reason != "" || lim.Text != "" {
				t.Fatal("a block lost its reason")
			}
		case got != Blocked && why != "":
			t.Fatalf("reason %q on a %s session", why, got)
		case !at.Equal(since) && !at.Equal(lim.At):
			t.Fatalf("since %v is neither the state's %v nor the limit's %v", at, since, lim.At)
		}
		if st == Idle && got == Blocked != lim.Active(now) {
			t.Fatalf("an idle session %s with the limit active=%v", got, lim.Active(now))
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

// A generated transcript: its lines, each ending in a newline.
type genTr struct {
	lines []string
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
// with prompts, replies, titles, Bash calls and their results, lines of
// kinds the fold ignores, and sidechain and meta lines.
func genTranscript() *rapid.Generator[genTr] {
	return rapid.Custom(func(t *rapid.T) genTr {
		var g genTr
		n := rapid.IntRange(0, 40).Draw(t, "n")
		for i := 0; i < n; i++ {
			at := trBase.Add(time.Duration(i) * time.Second).Format("2006-01-02T15:04:05.000Z")
			var line string
			switch rapid.IntRange(0, 8).Draw(t, "kind") {
			case 0, 1: // a prompt, as a string or as text blocks
				var content any = text().Draw(t, "prompt")
				if rapid.Bool().Draw(t, "blocks") {
					content = []any{map[string]any{"type": "text", "text": content}}
				}
				line = trLine(map[string]any{"type": "user", "timestamp": at, "message": map[string]any{"role": "user", "content": content}})
			case 2: // a prompt in a sidechain or a meta line
				v := map[string]any{"type": "user", "timestamp": at, "message": map[string]any{"content": text().Draw(t, "aside")}}
				v[rapid.SampledFrom([]string{"isSidechain", "isMeta"}).Draw(t, "flag")] = true
				line = trLine(v)
			case 3: // a Bash call
				id := "toolu_" + strconv.Itoa(i)
				line = trLine(map[string]any{"type": "assistant", "timestamp": at, "message": map[string]any{"content": []any{
					map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": text().Draw(t, "cmd")}},
				}}})
			case 4: // a result
				line = trLine(map[string]any{"type": "user", "timestamp": at, "message": map[string]any{"content": []any{
					map[string]any{"tool_use_id": "toolu_0", "type": "tool_result", "content": "ok", "is_error": rapid.Bool().Draw(t, "error")},
				}}})
			case 5:
				line = trLine(map[string]any{"type": "assistant", "timestamp": at, "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": text().Draw(t, "reply")}}}})
			case 6:
				line = trLine(map[string]any{"type": rapid.SampledFrom([]string{"ai-title", "last-prompt"}).Draw(t, "meta"),
					"aiTitle": text().Draw(t, "title"), "lastPrompt": text().Draw(t, "last")})
			case 7: // a kind of line the fold ignores
				line = trLine(map[string]any{"type": "progress", "timestamp": at, "data": text().Draw(t, "data")})
			default:
				line = "not json at all\n"
			}
			g.lines = append(g.lines, line)
		}
		return g
	})
}

// factsOf is what a file's lines have added up to, without the file.
func factsOf(f *tfile) Facts {
	g := f.fold
	return Facts{Title: g.title, LastPrompt: g.lastPrompt, LastReply: g.reply, ReplyAt: g.replyAt}
}

// sameFacts compares facts, taking times by their instant and not by their
// representation, and leaving out the modification time.
func sameFacts(a, b Facts) bool {
	a.Modified, b.Modified = time.Time{}, time.Time{}
	if !a.ReplyAt.Equal(b.ReplyAt) {
		return false
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
		whole := &tfile{}
		whole.feed(g.bytes())
		parts := &tfile{}
		for _, p := range splits(t, g.bytes()) {
			parts.feed(p)
		}
		if !sameFacts(factsOf(whole), factsOf(parts)) {
			t.Fatalf("chunked:\n%+v\nwhole:\n%+v", factsOf(parts), factsOf(whole))
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

// digestWords are the runs of letters and digits in s.
func digestWords(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// digestText is everything a parsed digest holds, a field to a line.
func digestText(doc DigestDoc) string {
	parts := []string{doc.Stamp}
	for _, s := range doc.Sections {
		parts = append(parts, s.Title)
		for _, it := range s.Items {
			parts = append(parts, it.Names...)
			parts = append(parts, it.Project, it.Issue, it.Do, it.Why, it.Since)
			parts = append(parts, it.Notes...)
		}
	}
	return strings.Join(parts, "\n")
}

// genDigestSource draws text shaped like a digest and unlike one: lines
// with the markers, labels and names a digest has, in any order and at
// any indent, and now and then any text at all.
func genDigestSource() *rapid.Generator[string] {
	word := rapid.OneOf(
		rapid.SampledFrom([]string{"do", "Do:", "why:", "Since:", "since", "waiting", "and", "**a-1**", "(p)",
			"`x. Y`", "Done.", "Then", "#", "-", "*", "**", ":", "héllo", "日本語"}),
		rapid.StringMatching(`[a-zA-Z0-9.:?!]{1,8}`),
	)
	line := rapid.Custom(func(t *rapid.T) string {
		lead := rapid.SampledFrom([]string{"", "", "- ", "* ", "  - ", "    ", "# ", "## ", "### ", "\t- ", "• "}).Draw(t, "lead")
		head := rapid.SampledFrom([]string{"", "", "**n-1** (proj): ", "**a**, **b** and **c** (p q): ", "**n**: ",
			"Do: ", "**Do:** ", "Why: ", "Since: ", "Waiting since "}).Draw(t, "head")
		return lead + head + strings.Join(rapid.SliceOfN(word, 0, 8).Draw(t, "words"), " ")
	})
	lines := rapid.Custom(func(t *rapid.T) string {
		return strings.Join(rapid.SliceOfN(line, 0, 12).Draw(t, "lines"), "\n")
	})
	return rapid.OneOf(lines, lines, text())
}

// Parsing a digest loses nothing and makes nothing up. Every word of the
// result is a word of the source, and the only words of the source that
// are missing from it are the labels a field starts with and the "and"
// between an item's names.
func TestPropParseDigestKeepsEveryWord(t *testing.T) {
	dropped := []string{"do", "why", "since", "waiting", "and"}
	rapid.Check(t, func(t *rapid.T) {
		md := genDigestSource().Draw(t, "md")
		left := map[string]int{}
		for _, w := range digestWords(md) {
			left[w]++
		}
		for _, w := range digestWords(digestText(ParseDigest(md))) {
			if left[w]--; left[w] < 0 {
				t.Fatalf("the digest has %q more often than its source %q", w, md)
			}
		}
		for w, n := range left {
			if n > 0 && !slices.Contains(dropped, strings.ToLower(w)) {
				t.Fatalf("%q is in the source %q and not in the digest", w, md)
			}
		}
	})
}

// formatDigest writes doc as the instructions ask a digest to be
// written: a bullet to an item, with its fields in indented bullets
// under it, and a paragraph for text that belongs to no session.
func formatDigest(doc DigestDoc) string {
	var b strings.Builder
	if doc.Stamp != "" {
		fmt.Fprintf(&b, "# %s\n\n", doc.Stamp)
	}
	for _, s := range doc.Sections {
		if s.Title != "" {
			fmt.Fprintf(&b, "## %s\n", s.Title)
		}
		for _, it := range s.Items {
			if len(it.Names) == 0 {
				fmt.Fprintf(&b, "\n%s\n\n", it.Issue)
				continue
			}
			b.WriteString("- **" + strings.Join(it.Names, "**, **") + "**")
			if it.Project != "" {
				b.WriteString(" (" + it.Project + ")")
			}
			b.WriteString(": " + it.Issue + "\n")
			for _, f := range [][2]string{{"Do", it.Do}, {"Why", it.Why}, {"Since", it.Since}} {
				if f[1] != "" {
					fmt.Fprintf(&b, "  - %s: %s\n", f[0], f[1])
				}
			}
			for _, n := range it.Notes {
				b.WriteString("  - " + n + "\n")
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// genDigestDoc draws a digest of plain words, which read the same
// however they are laid out.
func genDigestDoc() *rapid.Generator[DigestDoc] {
	// No sentence ends in these, and none is the label of a field.
	words := func(lo, hi int) *rapid.Generator[string] {
		return rapid.Custom(func(t *rapid.T) string {
			ws := rapid.SliceOfN(rapid.StringMatching(`[a-z]{1,8}`), lo, hi).Draw(t, "words")
			if len(ws) > 0 && ws[0] == "waiting" {
				ws[0] = "waited"
			}
			return strings.Join(ws, " ")
		})
	}
	item := rapid.Custom(func(t *rapid.T) DigestItem {
		if rapid.IntRange(0, 5).Draw(t, "plain") == 0 {
			return DigestItem{Issue: words(1, 6).Draw(t, "text")}
		}
		it := DigestItem{
			Names:   rapid.SliceOfN(rapid.StringMatching(`[a-z0-9][a-z0-9-]{0,7}`), 1, 3).Draw(t, "names"),
			Project: words(0, 2).Draw(t, "project"),
			Issue:   words(0, 8).Draw(t, "issue"),
			Do:      words(0, 6).Draw(t, "do"),
			Why:     words(0, 6).Draw(t, "why"),
			Since:   words(0, 3).Draw(t, "since"),
		}
		if notes := rapid.SliceOfN(words(1, 5), 0, 2).Draw(t, "notes"); len(notes) > 0 {
			it.Notes = notes
		}
		return it
	})
	return rapid.Custom(func(t *rapid.T) DigestDoc {
		doc := DigestDoc{Stamp: words(0, 4).Draw(t, "stamp")}
		for i, n := 0, rapid.IntRange(0, 4).Draw(t, "sections"); i < n; i++ {
			s := DigestSection{Title: words(1, 3).Draw(t, "title")}
			if i == 0 && rapid.Bool().Draw(t, "untitled") {
				s.Title = ""
			}
			if items := rapid.SliceOfN(item, 0, 5).Draw(t, "items"); len(items) > 0 {
				s.Items = items
			}
			// Text before the first heading has no section without it.
			if s.Title != "" || len(s.Items) > 0 {
				doc.Sections = append(doc.Sections, s)
			}
		}
		return doc
	})
}

// A digest written as the instructions ask reads back as it was: every
// name, project, issue, field and note where it was put.
func TestPropParseDigestReadsWhatTheInstructionsAskFor(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		doc := genDigestDoc().Draw(t, "doc")
		md := formatDigest(doc)
		if got := ParseDigest(md); !reflect.DeepEqual(got, doc) {
			t.Fatalf("wrote\n%s\nread  %+v\nwant  %+v", md, got, doc)
		}
	})
}
