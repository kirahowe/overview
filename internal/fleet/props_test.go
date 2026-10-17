package fleet

import (
	"fmt"
	"os"
	"path/filepath"
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
			Label:      text().Draw(t, "label"),
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

			if changed != (next != s) {
				t.Fatalf("event %d %q: changed=%v but next==prev is %v", i, ev.Name, changed, next == s)
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
			if changedAgain || again != next {
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

func TestPropLiveState(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		l, rec := genLive().Draw(t, "live"), genSession().Draw(t, "rec")
		st, since, reason := liveState(l, rec)
		st2, since2, reason2 := liveState(l, rec)
		if st != st2 || !since.Equal(since2) || reason != reason2 {
			t.Fatal("liveState is not deterministic")
		}

		switch {
		case rec.State == Blocked:
			if st != Blocked || !since.Equal(rec.Since) || reason != rec.Reason {
				t.Fatalf("a recorded block must win: got %s %v %q", st, since, reason)
			}
		case l.BgState == "blocked":
			if st != Blocked || !since.IsZero() || reason != "" {
				t.Fatalf("Claude's own block: got %s %v %q", st, since, reason)
			}
		case l.Status != "idle":
			if st != Working {
				t.Fatalf("busy and unblocked must be working, got %s", st)
			}
		default:
			if st != Idle {
				t.Fatalf("idle and unblocked must be idle, got %s", st)
			}
		}
		if st != Working && st != Blocked && st != Idle {
			t.Fatalf("liveState returned %q", st)
		}
		if st != Blocked && reason != "" {
			t.Fatalf("reason %q on a %s row", reason, st)
		}
		// Claude Code reporting idle overrides a stale Working record, so an
		// interrupted turn does not look busy forever.
		if l.Status == "idle" && l.BgState != "blocked" && rec.State != Blocked && st != Idle {
			t.Fatalf("idle session shown as %s", st)
		}
	})
}

func TestPropAskingAndNeedsYou(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := Row{
			State:     genState().Draw(t, "state"),
			LastReply: text().Draw(t, "reply"),
		}
		if r.Asking() != r.Asking() || r.NeedsYou() != r.NeedsYou() {
			t.Fatal("not deterministic")
		}
		if r.Asking() && r.State != Idle {
			t.Fatalf("a %s row cannot be asking", r.State)
		}
		if r.Asking() && r.State == Blocked {
			t.Fatal("blocked and asking overlap")
		}
		if r.NeedsYou() != (r.State == Blocked || r.Asking()) {
			t.Fatalf("NeedsYou disagrees with its definition for %+v", r)
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
	return Home(filepath.Join(root, fmt.Sprintf("case%d", *n), "home"))
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
		if got.ID != s.ID || got.Cwd != s.Cwd || got.Label != s.Label || got.State != s.State ||
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
		parent := filepath.Dir(string(h))
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
		err := filepath.WalkDir(string(h), func(p string, d os.DirEntry, err error) error {
			if err != nil || p == string(h) || p == h.dataDir() || p == h.sessionsDir() {
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
