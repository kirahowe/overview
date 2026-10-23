package issues

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode"

	"pgregory.net/rapid"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseListFixture(t *testing.T) {
	got, err := ParseList(fixture(t, "list.json"))
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	i := got[0]
	if i.ID != 1 || i.Ref != "overview#1" || i.Project != "overview" || i.Status != Inbox ||
		i.Priority != "p2" || i.Type != "feature" || i.Title == "" || i.Details != "" {
		t.Errorf("got %+v", i)
	}
}

func TestParseShowFixture(t *testing.T) {
	i, err := ParseShow(fixture(t, "show.json"))
	if err != nil || i.ID != 1 || !strings.HasPrefix(i.Details, "# Resume a session") {
		t.Fatalf("got %+v, %v", i, err)
	}
	if c := Criteria(i.Details); len(c) == 0 || c[0].Done || !strings.Contains(c[0].Text, "in that session's own directory.") {
		t.Errorf("criteria %+v", c)
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	for _, s := range []string{"", "nope", `{"issues": 3}`, `[1]`} {
		if _, err := ParseList([]byte(s)); err == nil {
			t.Errorf("ParseList(%q) accepted", s)
		}
	}
	if _, err := ParseShow([]byte(`{"id": "x"}`)); err == nil {
		t.Error("ParseShow accepted a string id")
	}
	if got, err := ParseList([]byte(`{"issues": []}`)); err != nil || len(got) != 0 {
		t.Errorf("empty list: %v, %v", got, err)
	}
}

func TestListAndShowUseTheRunner(t *testing.T) {
	var calls [][]string
	run := func(args ...string) ([]byte, error) {
		calls = append(calls, args)
		if args[2] == "list" {
			return fixture(t, "list.json"), nil
		}
		return fixture(t, "show.json"), nil
	}
	if l, err := List(run, "/p"); err != nil || len(l) != 1 {
		t.Fatalf("%v %v", l, err)
	}
	if i, err := Show(run, "/p", 1); err != nil || i.Details == "" {
		t.Fatalf("%+v %v", i, err)
	}
	want := [][]string{{"--dir", "/p", "list", "--json"}, {"--dir", "/p", "show", "1", "--json"}}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls %v, want %v", calls, want)
	}
	boom := func(...string) ([]byte, error) { return nil, errors.New("boom") }
	if _, err := List(boom, "/p"); err == nil {
		t.Error("List swallowed the error")
	}
	if _, err := Show(boom, "/p", 1); err == nil {
		t.Error("Show swallowed the error")
	}
}

func TestProjectID(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{`{:id "overview"}`, "overview", true},
		{"{:name \"x\",\n :id   \"a-b\" , :other 1}", "a-b", true},
		{`{:title ":id \"fake\"" :id "real"}`, "real", true},
		{`{:foo/id "no" :id "yes"}`, "yes", true},
		{"; :id \"c\"\n{:id \"d\"}", "d", true},
		{`{:name "x"}`, "", false},
		{`{:id 3}`, "", false},
		{`{:id`, "", false},
		{``, "", false},
	} {
		if got, ok := ProjectID(c.in); got != c.want || ok != c.ok {
			t.Errorf("ProjectID(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestCriteria(t *testing.T) {
	d := "# T\n\n## Context\n\n- [ ] not mine\n\n### acceptance CRITERIA\n\n- [ ] one\n      continues here\n- [x] two\n* [X] three\n\n- plain bullet\n- [ ]\n\n## Next\n\n- [ ] after\n"
	want := []Criterion{{"one continues here", false}, {"two", true}, {"three", true}, {"", false}}
	if got := Criteria(d); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := Criteria("no headings\n- [ ] x"); got != nil {
		t.Errorf("got %+v", got)
	}
	// A deeper heading stays inside the section; a same-level one ends it.
	d = "## Acceptance criteria\n- [ ] a\n### Sub\n- [ ] b\n## Other\n- [ ] c\n"
	if got := Criteria(d); len(got) != 2 {
		t.Errorf("got %+v", got)
	}
}

func TestRankOrder(t *testing.T) {
	order := []string{Review, InProgress, Blocked, Ready, Inbox, Done, Dropped, "weird"}
	for i := 1; i < len(order); i++ {
		if Rank(order[i-1]) >= Rank(order[i]) {
			t.Errorf("%s should rank before %s", order[i-1], order[i])
		}
	}
	if PriorityRank("p1") != 1 || PriorityRank("p3") != 3 || PriorityRank("p9") <= 3 || PriorityRank("") <= 3 {
		t.Error("priority ranks")
	}
	if Open(Done) || Open(Dropped) || !Open(Review) || !Open("x") {
		t.Error("Open")
	}
}

func TestHasTracker(t *testing.T) {
	seen := ""
	exists := func(p string) bool { seen = p; return true }
	if !HasTracker(exists, "/r") || seen != "/r/.issues" {
		t.Errorf("looked at %q", seen)
	}
}

func TestExecReportsStderr(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	script := t.TempDir() + "/fake"
	os.WriteFile(script, []byte("#!"+sh+"\necho out\necho bad thing >&2\nexit 3\n"), 0o755)
	_, err = Exec(script)("x")
	if err == nil || !strings.Contains(err.Error(), "bad thing") {
		t.Errorf("err %v", err)
	}
	os.WriteFile(script, []byte("#!"+sh+"\necho out\necho warn >&2\n"), 0o755)
	if out, err := Exec(script)("x"); err != nil || string(out) != "out\n" {
		t.Errorf("out %q, err %v", out, err)
	}
}

// With the real binary, against a project made in a temporary directory.
func TestRealBinary(t *testing.T) {
	path, err := exec.LookPath("issues")
	if err != nil {
		t.Skip("issues is not installed")
	}
	run, dir := Exec(path), t.TempDir()
	if _, err := run("--dir", dir, "init", "--id", "demo"); err != nil {
		t.Skipf("init: %v", err)
	}
	if _, err := run("--dir", dir, "add", "First one"); err != nil {
		t.Skipf("add: %v", err)
	}
	l, err := List(run, dir)
	if err != nil || len(l) != 1 || l[0].Project != "demo" || l[0].Title != "First one" {
		t.Fatalf("got %+v, %v", l, err)
	}
	if i, err := Show(run, dir, l[0].ID); err != nil || i.Ref != l[0].Ref {
		t.Errorf("got %+v, %v", i, err)
	}
	edn, err := os.ReadFile(dir + "/.issues/project.edn")
	if id, ok := ProjectID(string(edn)); err != nil || !ok || id != "demo" {
		t.Errorf("project id %q, %v, %v", id, ok, err)
	}
}

var word = rapid.StringMatching(`[A-Za-z0-9.,'()-]{1,8}`)

func genIssue() *rapid.Generator[Issue] {
	return rapid.Custom(func(t *rapid.T) Issue {
		return Issue{
			ID:        rapid.IntRange(1, 9999).Draw(t, "id"),
			Ref:       word.Draw(t, "ref"),
			Project:   word.Draw(t, "project"),
			Title:     rapid.String().Draw(t, "title"),
			Status:    rapid.SampledFrom([]string{Inbox, Ready, InProgress, Review, Blocked, Done, Dropped}).Draw(t, "status"),
			Priority:  rapid.SampledFrom([]string{"p0", "p1", "p2", "p3"}).Draw(t, "prio"),
			Type:      word.Draw(t, "type"),
			Tags:      rapid.SliceOf(word).Draw(t, "tags"),
			BlockedBy: rapid.SliceOf(rapid.IntRange(1, 99)).Draw(t, "blocked"),
		}
	})
}

// ParseList reads back what the CLI's JSON shape holds, and ignores
// fields it does not know.
func TestPropParseListRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.SliceOf(genIssue()).Draw(t, "issues")
		var docs []map[string]any
		for _, i := range in {
			docs = append(docs, map[string]any{"id": i.ID, "ref": i.Ref, "project": i.Project, "title": i.Title,
				"status": i.Status, "priority": i.Priority, "type": i.Type, "tags": i.Tags,
				"blocked-by": i.BlockedBy, "extra": []int{1}, "file": "x"})
		}
		b, _ := json.Marshal(map[string]any{"issues": docs, "all?": false})
		got, err := ParseList(b)
		if err != nil || len(got) != len(in) {
			t.Fatalf("got %d, %v; want %d", len(got), err, len(in))
		}
		for k := range in {
			a, b := in[k], got[k]
			a.Title = OneLine(a.Title)
			if len(a.Tags) == 0 && len(b.Tags) == 0 {
				a.Tags, b.Tags = nil, nil
			}
			if len(a.BlockedBy) == 0 && len(b.BlockedBy) == 0 {
				a.BlockedBy, b.BlockedBy = nil, nil
			}
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("got %+v, want %+v", b, a)
			}
		}
	})
}

// Criteria never panics, and returns no more items than the text has
// checklist lines, each made of words from the text.
func TestPropCriteriaComeFromChecklistLines(t *testing.T) {
	lines := rapid.OneOf(
		rapid.StringMatching(`#{1,3} (Acceptance criteria|Other)`),
		rapid.StringMatching(`[ ]{0,4}[-*+] \[[ xX]\] [a-z ]{0,12}`),
		rapid.StringMatching(`[ ]{0,6}[a-z ]{0,12}`),
		rapid.String(),
	)
	rapid.Check(t, func(t *rapid.T) {
		text := strings.Join(rapid.SliceOf(lines).Draw(t, "lines"), "\n")
		items := 0
		for _, l := range strings.Split(text, "\n") {
			if itemRe.MatchString(strings.TrimRight(l, "\r")) {
				items++
			}
		}
		got := Criteria(text)
		if len(got) > items {
			t.Fatalf("%d criteria from %d checklist lines in %q", len(got), items, text)
		}
		for _, c := range got {
			for _, w := range strings.Fields(c.Text) {
				if !strings.Contains(text, w) {
					t.Fatalf("word %q is not in the text", w)
				}
			}
		}
	})
}

// A done mark and a text written into a section come back as written.
func TestPropCriteriaRoundTrip(t *testing.T) {
	item := rapid.Custom(func(t *rapid.T) Criterion {
		return Criterion{Text: rapid.StringMatching(`[a-z]{1,6}( [a-z]{1,6}){0,4}`).Draw(t, "text"), Done: rapid.Bool().Draw(t, "done")}
	})
	rapid.Check(t, func(t *rapid.T) {
		want := rapid.SliceOfN(item, 0, 6).Draw(t, "items")
		var b strings.Builder
		b.WriteString("# T\n\n## Acceptance criteria\n\n")
		for _, c := range want {
			mark := " "
			if c.Done {
				mark = "x"
			}
			b.WriteString("- [" + mark + "] " + c.Text + "\n")
		}
		b.WriteString("\n## After\n- [ ] no\n")
		got := Criteria(b.String())
		if len(want) == 0 && len(got) == 0 {
			return
		}
		if !slices.Equal(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})
}

// ProjectID finds a generated id wherever it sits among other keys, and
// ignores an :id inside a string value.
func TestPropProjectIDRoundTrips(t *testing.T) {
	id := rapid.StringMatching(`[a-z][a-z0-9-]{0,12}`)
	sep := rapid.StringMatching(`[ ,\n\t]{1,3}`)
	rapid.Check(t, func(t *rapid.T) {
		want, decoy := id.Draw(t, "id"), id.Draw(t, "decoy")
		edn := "{" + `:note ":id \"` + decoy + `\""` + sep.Draw(t, "s1") + ":id" + sep.Draw(t, "s2") + `"` + want + `"` +
			sep.Draw(t, "s3") + ":tail 1}"
		if got, ok := ProjectID(edn); !ok || got != want {
			t.Fatalf("ProjectID(%q) = %q, %v; want %q", edn, got, ok, want)
		}
	})
}

// Rank and PriorityRank are total orders over the names they know: no two
// distinct known names tie, and every other name sorts after them.
func TestPropRanksAreTotal(t *testing.T) {
	statuses := []string{Review, InProgress, Blocked, Ready, Inbox, Done, Dropped}
	rapid.Check(t, func(t *rapid.T) {
		a, b := rapid.SampledFrom(statuses).Draw(t, "a"), rapid.SampledFrom(statuses).Draw(t, "b")
		if (a == b) != (Rank(a) == Rank(b)) {
			t.Fatalf("%s and %s: ranks %d and %d", a, b, Rank(a), Rank(b))
		}
		other := rapid.String().Draw(t, "other")
		if !slices.Contains(statuses, other) && Rank(other) <= Rank(Dropped) {
			t.Fatalf("%q ranks with the known statuses", other)
		}
		p := rapid.String().Draw(t, "p")
		if r := PriorityRank(p); (r <= 3) != (len(p) == 2 && p[0] == 'p' && p[1] >= '0' && p[1] <= '3') {
			t.Fatalf("PriorityRank(%q) = %d", p, r)
		}
	})
}

// messy is text with the characters that break a layout or reach the
// terminal as commands: newlines, tabs, escape sequences, other controls,
// and wide characters around them.
var messy = rapid.Custom(func(t *rapid.T) string {
	parts := rapid.SliceOfN(rapid.SampledFrom([]string{"fix", "the", "日本", "é", " ", "  ", "\n", "\r\n", "\t",
		"\x1b[31m", "\x1b]0;title\x07", "\x00", "\x7f", "\u0085", "\u009b", "\u2028"}), 0, 12).Draw(t, "parts")
	return strings.Join(parts, "")
})

func hasControl(s string, allow rune) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r != allow && unicode.IsControl(r) })
}

// OneLine leaves one line of the same words, single spaced and free of
// control characters, and changes nothing more on a second pass. Text
// keeps the lines and drops every other control character.
func TestPropCleaning(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.OneOf(messy, rapid.String()).Draw(t, "s")
		one := OneLine(s)
		if hasControl(one, -1) || strings.Contains(one, "  ") || strings.TrimSpace(one) != one {
			t.Fatalf("OneLine(%q) = %q", s, one)
		}
		if strings.ContainsFunc(one, func(r rune) bool { return r != ' ' && unicode.IsSpace(r) }) {
			t.Fatalf("OneLine(%q) = %q keeps other white space", s, one)
		}
		if OneLine(one) != one {
			t.Fatalf("OneLine is not idempotent on %q", s)
		}
		text := Text(s)
		if hasControl(text, '\n') || strings.Count(text, "\n") != strings.Count(s, "\n") || Text(text) != text {
			t.Fatalf("Text(%q) = %q", s, text)
		}
	})
}

// Every text field the CLI prints comes out of the parsers clean.
func TestPropParsedFieldsAreClean(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		doc := map[string]any{"id": 1, "ref": messy.Draw(t, "ref"), "project": messy.Draw(t, "project"),
			"title": messy.Draw(t, "title"), "status": messy.Draw(t, "status"), "priority": messy.Draw(t, "priority"),
			"type": messy.Draw(t, "type"), "tags": []string{messy.Draw(t, "tag")}, "details-text": messy.Draw(t, "details")}
		b, _ := json.Marshal(doc)
		i, err := ParseShow(b)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{i.Ref, i.Project, i.Title, i.Status, i.Priority, i.Type, i.Tags[0]} {
			if f != OneLine(f) {
				t.Fatalf("field %q is not clean in %+v", f, i)
			}
		}
		if i.Details != Text(i.Details) {
			t.Fatalf("details %q are not clean", i.Details)
		}
		lb, _ := json.Marshal(map[string]any{"issues": []any{doc}})
		l, err := ParseList(lb)
		if err != nil || !reflect.DeepEqual(l[0], i) {
			t.Fatalf("list gave %+v, show %+v (%v)", l, i, err)
		}
	})
}
