package jarmeta

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// tomlDoc is the part of a TOML document that a mods.toml reader needs: the
// string values of the top-level table and of arrays of tables such as
// [[mods]] and [[dependencies.<modid>]]. Plain [tables] are skipped.
type tomlDoc struct {
	top    map[string]string
	arrays map[string][]map[string]string
}

// parseTOML reads the subset of TOML that mods.toml files use: comments,
// key = value pairs with all four string forms, arrays (joined with ", ")
// and bare values such as numbers and booleans (kept as written). It is
// forgiving: anything it doesn't understand is skipped, never fatal, and it
// always terminates.
func parseTOML(src string) tomlDoc {
	p := &tomlParser{s: src, doc: tomlDoc{top: map[string]string{}, arrays: map[string][]map[string]string{}}}
	p.cur = p.doc.top
	for {
		p.skipBlank()
		if p.i >= len(p.s) {
			return p.doc
		}
		switch p.s[p.i] {
		case '#':
			p.skipLine()
		case '[':
			p.header()
		default:
			p.keyValue()
		}
	}
}

type tomlParser struct {
	s   string
	i   int
	doc tomlDoc
	cur map[string]string // where values go; nil inside a table that is skipped
}

// skipBlank skips spaces, tabs and line breaks.
func (p *tomlParser) skipBlank() {
	for p.i < len(p.s) && strings.IndexByte(" \t\r\n", p.s[p.i]) >= 0 {
		p.i++
	}
}

// skipSpace skips spaces and tabs.
func (p *tomlParser) skipSpace() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t') {
		p.i++
	}
}

// skipLine moves past the end of the current line.
func (p *tomlParser) skipLine() {
	if k := strings.IndexByte(p.s[p.i:], '\n'); k >= 0 {
		p.i += k + 1
	} else {
		p.i = len(p.s)
	}
}

// restOfLine is the text from the cursor to the end of the line.
func (p *tomlParser) restOfLine() string {
	line := p.s[p.i:]
	if k := strings.IndexByte(line, '\n'); k >= 0 {
		line = line[:k]
	}
	return line
}

func (p *tomlParser) header() {
	line := p.restOfLine()
	p.skipLine()
	array := strings.HasPrefix(line, "[[")
	open, end := "[", "]"
	if array {
		open, end = "[[", "]]"
	}
	k := strings.Index(line[len(open):], end)
	if k < 0 || !array {
		p.cur = nil
		return
	}
	name := tomlKey(line[len(open) : len(open)+k])
	t := map[string]string{}
	p.doc.arrays[name] = append(p.doc.arrays[name], t)
	p.cur = t
}

func (p *tomlParser) keyValue() {
	line := p.restOfLine()
	eq := strings.IndexByte(line, '=')
	if eq < 0 {
		p.skipLine()
		return
	}
	key := tomlKey(line[:eq])
	p.i += eq + 1
	p.skipSpace()
	v, ok := p.value(false)
	if ok && p.cur != nil && key != "" {
		if _, dup := p.cur[key]; !dup {
			p.cur[key] = v
		}
	}
	p.skipLine() // a trailing comment, or junk
}

// value parses the value at the cursor. Inside an array, bare values also end
// at ',' and ']'.
func (p *tomlParser) value(inArray bool) (string, bool) {
	rest := p.s[p.i:]
	switch {
	case strings.HasPrefix(rest, `"""`):
		return p.multiline(`"""`)
	case strings.HasPrefix(rest, `'''`):
		return p.multiline(`'''`)
	case strings.HasPrefix(rest, `"`):
		return p.quoted('"')
	case strings.HasPrefix(rest, `'`):
		return p.quoted('\'')
	case strings.HasPrefix(rest, "["):
		return p.array()
	case strings.HasPrefix(rest, "{"):
		p.skipInlineTable()
		return "", false
	}
	stop := "#\n"
	if inArray {
		stop = "#\n,]"
	}
	end := strings.IndexAny(rest, stop)
	if end < 0 {
		end = len(rest)
	}
	p.i += end
	return strings.TrimSpace(rest[:end]), true
}

// quoted reads a one-line basic ("…", with escapes) or literal ('…') string.
func (p *tomlParser) quoted(q byte) (string, bool) {
	j := p.i + 1
	for j < len(p.s) && p.s[j] != q && p.s[j] != '\n' {
		if q == '"' && p.s[j] == '\\' {
			j++ // the escaped character
		}
		j++
	}
	if j >= len(p.s) || p.s[j] != q {
		p.i = min(j, len(p.s)) // unterminated: give up on this value
		return "", false
	}
	raw := p.s[p.i+1 : j]
	p.i = j + 1
	if q == '"' {
		raw = unescape(raw)
	}
	return raw, true
}

// multiline reads a multi-line string: basic (three double quotes, with
// escapes) or literal (three single quotes).
func (p *tomlParser) multiline(delim string) (string, bool) {
	start := p.i + 3
	// A line break right after the opening quotes isn't part of the value.
	if strings.HasPrefix(p.s[start:], "\r\n") {
		start += 2
	} else if strings.HasPrefix(p.s[start:], "\n") {
		start++
	}
	end := -1
	for j := start; ; {
		k := strings.Index(p.s[j:], delim)
		if k < 0 {
			break
		}
		k += j
		if delim == `"""` && escaped(p.s, k) {
			j = k + 1
			continue
		}
		end = k
		break
	}
	if end < 0 {
		p.i = len(p.s)
		return "", false
	}
	// Up to two quotes right before the closing ones belong to the value.
	for n := 0; n < 2 && end+3 < len(p.s) && p.s[end+3] == delim[0]; n++ {
		end++
	}
	raw := p.s[start:end]
	p.i = end + 3
	if delim == `"""` {
		raw = unescape(raw)
	}
	return raw, true
}

// escaped reports whether the byte at i is preceded by an odd number of
// backslashes.
func escaped(s string, i int) bool {
	n := 0
	for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
		n++
	}
	return n%2 == 1
}

// array reads [a, b, …], possibly over several lines, and joins the values.
func (p *tomlParser) array() (string, bool) {
	p.i++ // [
	var items []string
	for {
		p.skipBlank()
		if p.i >= len(p.s) {
			return strings.Join(items, ", "), false
		}
		switch p.s[p.i] {
		case ']':
			p.i++
			return strings.Join(items, ", "), true
		case ',':
			p.i++
		case '#':
			p.skipLine()
		default:
			before := p.i
			if v, ok := p.value(true); ok && v != "" {
				items = append(items, v)
			}
			if p.i == before {
				p.i++ // never get stuck
			}
		}
	}
}

// skipInlineTable skips {…}, which TOML keeps on one line.
func (p *tomlParser) skipInlineTable() {
	depth := 0
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				p.i++
				return
			}
		case '\n':
			return
		}
		p.i++
	}
}

// tomlKey normalises a key or table name: ` dependencies."my-mod" ` becomes
// dependencies.my-mod. Dots inside quotes don't split the key.
func tomlKey(k string) string {
	var parts []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '.':
			parts = append(parts, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	parts = append(parts, strings.TrimSpace(cur.String()))
	return strings.Join(parts, ".")
}

// unescape resolves the escapes of a TOML basic string.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 == len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch e := s[i]; e {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'e':
			b.WriteByte(0x1b)
		case '"', '\\':
			b.WriteByte(e)
		case 'u', 'U':
			n := 4
			if e == 'U' {
				n = 8
			}
			if i+1+n <= len(s) {
				if v, err := strconv.ParseUint(s[i+1:i+1+n], 16, 32); err == nil && utf8.ValidRune(rune(v)) {
					b.WriteRune(rune(v))
					i += n
					continue
				}
			}
			b.WriteByte('\\')
			b.WriteByte(e)
		case ' ', '\t', '\r', '\n':
			// A backslash at the end of a line joins it to the next one,
			// dropping the line break and the following whitespace.
			j := i
			for j < len(s) && strings.IndexByte(" \t\r\n", s[j]) >= 0 {
				j++
			}
			i = j - 1
		default:
			b.WriteByte('\\')
			b.WriteByte(e)
		}
	}
	return b.String()
}
