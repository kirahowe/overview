package diff

import (
	"strconv"
	"strings"
)

// quote writes a path as git does in a diff header: bare when it is
// plain, otherwise in double quotes with C escapes and octal bytes.
func quote(s string) string {
	if !needsQuote(s) {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\a':
			b.WriteString(`\a`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\v':
			b.WriteString(`\v`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			if c < 0x20 || c >= 0x7f {
				b.WriteByte('\\')
				b.WriteByte('0' + c>>6)
				b.WriteByte('0' + c>>3&7)
				b.WriteByte('0' + c&7)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func needsQuote(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c >= 0x7f || c == '"' || c == '\\' {
			return true
		}
	}
	return false
}

// unquote reads a C-quoted string at the start of s, which must begin
// with a double quote. It returns the text, whatever follows the closing
// quote, and false when the quoting is malformed.
func unquote(s string) (val, rest string, ok bool) {
	if s == "" || s[0] != '"' {
		return "", s, false
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			return b.String(), s[i+1:], true
		case c != '\\':
			b.WriteByte(c)
		case i+1 >= len(s):
			return "", s, false
		case s[i+1] >= '0' && s[i+1] <= '7':
			j := i + 1
			for j < len(s) && j < i+4 && s[j] >= '0' && s[j] <= '7' {
				j++
			}
			n, _ := strconv.ParseUint(s[i+1:j], 8, 16)
			b.WriteByte(byte(n))
			i = j - 1
		default:
			i++
			b.WriteByte(unescape(s[i]))
		}
	}
	return "", s, false
}

// unescape maps the character after a backslash to what it stands for.
// An unknown one stands for itself.
func unescape(c byte) byte {
	switch c {
	case 'a':
		return '\a'
	case 'b':
		return '\b'
	case 't':
		return '\t'
	case 'n':
		return '\n'
	case 'v':
		return '\v'
	case 'f':
		return '\f'
	case 'r':
		return '\r'
	}
	return c
}
