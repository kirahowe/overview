package ghostty

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"pgregory.net/rapid"
)

func dropped(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

func TestSanitizeProps(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.OneOf(
			rapid.String(),
			rapid.StringOf(rapid.RuneFrom([]rune{'a', 'é', '日', '\x1b', '\x07', '\x9c', '\x7f', '\n', '\\', ']', 0x85})),
			rawBytes(),
		).Draw(t, "s")
		out := SanitizeTitle(s)
		if !utf8.ValidString(out) {
			t.Fatalf("invalid utf8: %q", out)
		}
		for _, r := range out {
			if dropped(r) {
				t.Fatalf("emitted %U in %q", r, out)
			}
		}
		if strings.ContainsAny(out, "\x1b\x07") {
			t.Fatalf("ESC or BEL in %q", out)
		}
		if SanitizeTitle(out) != out {
			t.Fatalf("not idempotent: %q -> %q", out, SanitizeTitle(out))
		}
		if utf8.RuneCountInString(out) > maxTitle {
			t.Fatalf("too long: %d", utf8.RuneCountInString(out))
		}
		// Every valid printable rune is kept, in order, up to the cap.
		var want []rune
		for _, r := range s {
			if !dropped(r) {
				want = append(want, r)
			}
		}
		if utf8.ValidString(s) {
			if len(want) > maxTitle {
				want = want[:maxTitle]
			}
			if out != string(want) {
				t.Fatalf("dropped printable runes: %q -> %q", s, out)
			}
		}
	})
}

func TestOSCTitleFrame(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.OneOf(rapid.String(), rawBytes()).Draw(t, "s")
		b := string(OSCTitle(s))
		if !strings.HasPrefix(b, "\x1b]2;") || !strings.HasSuffix(b, "\x07") {
			t.Fatalf("bad frame %q", b)
		}
		if strings.Count(b, "\x1b") != 1 || strings.Count(b, "\x07") != 1 {
			t.Fatalf("stray ESC/BEL in %q", b)
		}
		if strings.ContainsRune(b, 0x9c) {
			t.Fatalf("ST in %q", b)
		}
	})
}

func TestTTYPathProps(t *testing.T) {
	want := regexp.MustCompile(`^(/dev/)?ttys[0-9]+$`)
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.OneOf(
			rapid.String(),
			rapid.StringMatching(`(/dev/)?ttys[0-9]{1,5}`),
			rapid.StringMatching(`[/a-z.0-9\n ]{0,16}`),
			rapid.StringMatching(`(/dev/)?ttys[0-9]{0,3}[^0-9]{0,2}`),
		).Draw(t, "s")
		p, err := TTYPath(s)
		if (err == nil) != want.MatchString(s) {
			t.Fatalf("%q: err=%v, regexp says %v", s, err, want.MatchString(s))
		}
		if err == nil {
			if !strings.HasPrefix(p, "/dev/ttys") || strings.Contains(p[len("/dev/"):], "/") || strings.Contains(p, "..") {
				t.Fatalf("path escapes /dev: %q", p)
			}
		}
	})
}

func TestTokenProps(t *testing.T) {
	re := regexp.MustCompile(`^ov-[0-9a-f]{16}$`)
	seen := map[string]bool{}
	rapid.Check(t, func(t *rapid.T) {
		tok := newToken()
		if !re.MatchString(tok) {
			t.Fatalf("bad token %q", tok)
		}
		if seen[tok] {
			t.Fatalf("duplicate token %q", tok)
		}
		seen[tok] = true
		if SanitizeTitle(tok) != tok {
			t.Fatalf("sanitiser alters token")
		}
	})
}

func TestValidIDProps(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		good := rapid.StringMatching(`[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}`).Draw(t, "good")
		if !ValidID(good) {
			t.Fatalf("rejected %q", good)
		}
		bad := good + rapid.StringMatching(`.+`).Draw(t, "suffix")
		if ValidID(bad) {
			t.Fatalf("accepted %q", bad)
		}
	})
}

// rawBytes draws arbitrary bytes, so invalid UTF-8 gets exercised too.
func rawBytes() *rapid.Generator[string] {
	return rapid.Map(rapid.SliceOf(rapid.Byte()), func(b []byte) string { return string(b) })
}
