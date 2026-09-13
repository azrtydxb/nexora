// Package zonefile parses and writes BIND master files for hosted zones. The lexer (directives,
// parentheses, comments, owner inheritance) is Nexora code; RDATA text goes through miekg/dns.
package zonefile

import (
	"bufio"
	"io"
	"strings"
)

// logicalLine is one record or directive after joining parenthesised continuation lines.
type logicalLine struct {
	line       int
	ownerBlank bool
	fields     []string // quoted strings keep their quotes and escapes
}

// maxLineBytes bounds one physical line.
const maxLineBytes = 1 << 20

func splitLines(r io.Reader) ([]logicalLine, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	var out []logicalLine
	var cur logicalLine
	depth, n := 0, 0
	for sc.Scan() {
		n++
		text := sc.Text()
		if depth == 0 {
			cur = logicalLine{line: n, ownerBlank: len(text) > 0 && (text[0] == ' ' || text[0] == '\t')}
		}
		var tok strings.Builder
		flush := func() {
			if tok.Len() > 0 {
				cur.fields = append(cur.fields, tok.String())
				tok.Reset()
			}
		}
		inQuote, escaped := false, false
	scan:
		for i := 0; i < len(text); i++ {
			c := text[i]
			switch {
			case escaped:
				tok.WriteByte(c)
				escaped = false
			case c == '\\':
				tok.WriteByte(c)
				escaped = true
			case inQuote:
				tok.WriteByte(c)
				if c == '"' {
					inQuote = false
				}
			case c == '"':
				// A quote inside a token (key="value") stays part of that token.
				tok.WriteByte(c)
				inQuote = true
			case c == ';':
				break scan
			case c == '(':
				flush()
				depth++
			case c == ')':
				flush()
				if depth == 0 {
					return nil, Errors{{Line: n, Message: "unbalanced ')'"}}
				}
				depth--
			case c == ' ' || c == '\t' || c == '\r':
				flush()
			default:
				tok.WriteByte(c)
			}
		}
		if inQuote {
			return nil, Errors{{Line: n, Message: "unterminated quoted string"}}
		}
		flush()
		if depth == 0 && len(cur.fields) > 0 {
			out = append(out, cur)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, Errors{{Line: n + 1, Message: err.Error()}}
	}
	if depth != 0 {
		return nil, Errors{{Line: cur.line, Message: "unbalanced '('"}}
	}
	return out, nil
}
