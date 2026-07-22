// Package textscan holds the byte-level line helpers shared by the ethos
// packages that scan text files — the claudemd import writer, the githook
// chainer, and the doctor seal check — plus a heredoc-aware classifier for
// shell source.
//
// The heredoc classifier is deliberately narrow: it recognizes here-document
// bodies so a marker-shaped line or an `ethos audit seal` mention inside one
// is never misread as real shell. It tracks heredocs and arithmetic spans
// (which claim `<<` as a shift), the same bounded approach claudemd takes for
// markdown code fences.
//
// This lexical scope is frozen: heredocs and arithmetic spans, no more. A
// future lexical corner a reviewer or tool finds is handled by refuse-on-
// ambiguity or a documented limitation, never by adding another grammar rule
// — the durable safeguards are githook's ident-fingerprint guard and
// execution-based doctor verification (bead ethos-kcbv), not an ever-growing
// half-lexer here.
package textscan

import (
	"bytes"
	"path/filepath"
	"strings"
)

// SplitKeepEnds splits data into lines, each retaining its trailing
// terminator (\n, \r\n, or \r). A final line without a terminator is kept
// as-is. Concatenating the result reproduces data byte-for-byte.
func SplitKeepEnds(data []byte) []string {
	var lines []string
	for i := 0; i < len(data); {
		j := i
		for j < len(data) && data[j] != '\n' && data[j] != '\r' {
			j++
		}
		if j < len(data) {
			if data[j] == '\r' && j+1 < len(data) && data[j+1] == '\n' {
				j += 2
			} else {
				j++
			}
		}
		lines = append(lines, string(data[i:j]))
		i = j
	}
	return lines
}

// StripTerminator drops a single trailing \n, \r\n, or \r.
func StripTerminator(s string) string {
	s = strings.TrimSuffix(s, "\n")
	return strings.TrimSuffix(s, "\r")
}

// DetectEOL returns a file's EOL convention: CRLF if any \r\n is present,
// else lone CR if any \r, else LF (also the default for an empty file).
func DetectEOL(data []byte) []byte {
	if bytes.Contains(data, []byte("\r\n")) {
		return []byte("\r\n")
	}
	if bytes.IndexByte(data, '\r') >= 0 {
		return []byte("\r")
	}
	return []byte("\n")
}

// SamePath reports whether a and b name the same location, tolerating the
// symlinked temp roots (macOS /tmp → /private/tmp) tests run under.
func SamePath(a, b string) bool {
	if a == b {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// HeredocMask returns, for each line in SplitKeepEnds(data), whether that
// line sits inside a here-document body. A body line is opaque: callers must
// never read it as a marker boundary, a comment, or a command position. The
// line that introduces a heredoc and the terminator line itself are not
// opaque — they are real shell.
//
// Recognized forms: `<<DELIM`, `<<'DELIM'`, `<<"DELIM"`, `<<\DELIM`, and the
// tab-stripping `<<-DELIM` (whose terminator may be indented with tabs).
// `<<<` here-strings have no body and are ignored. Multiple heredocs opened on
// one line stack in order. `<<` inside a quote or after a `#` comment on the
// introducing line is not treated as a heredoc.
func HeredocMask(data []byte) []bool {
	mask, _ := scanHeredocs(data)
	return mask
}

// HeredocOpenAtEOF reports whether data ends inside an unterminated
// here-document — a body opened but never closed by its terminator. Such a
// host is malformed for line-oriented editing (its trailing lines, including
// an appender's own insertion point, are all masked opaque), so callers
// should refuse rather than edit it, in parity with claudemd's open-fence
// check.
func HeredocOpenAtEOF(data []byte) bool {
	_, open := scanHeredocs(data)
	return open
}

// scanHeredocs classifies each line of data (opaque iff inside a heredoc
// body) and reports whether a heredoc is still open at EOF. Arithmetic-span
// depth is carried across lines as loop state — NOT re-derived per line — so
// a `<<` inside a multi-line `$(( … ))` or bare `(( … ))` is the shift
// operator it is, never a phantom heredoc opener.
func scanHeredocs(data []byte) (mask []bool, openAtEOF bool) {
	lines := SplitKeepEnds(data)
	mask = make([]bool, len(lines))
	var queue []delim
	arith := 0 // paren depth inside an arithmetic span; carries across lines
	for i, raw := range lines {
		content := StripTerminator(raw)
		if len(queue) > 0 {
			// Inside a heredoc body — opaque until the terminator line. Do not
			// scan it (arithmetic depth freezes across the body).
			if queue[0].terminates(content) {
				queue = queue[1:]
				continue
			}
			mask[i] = true
			continue
		}
		var delims []delim
		delims, arith = scanLine(content, arith)
		queue = append(queue, delims...)
	}
	return mask, len(queue) > 0
}

// delim is a queued here-document delimiter.
type delim struct {
	text      string
	stripTabs bool // <<- form: leading tabs stripped before matching
}

// terminates reports whether content is this delimiter's terminator line.
func (d delim) terminates(content string) bool {
	if d.stripTabs {
		content = strings.TrimLeft(content, "\t")
	}
	return content == d.text
}

// scanLine walks one line for heredoc openers, given the arithmetic-span depth
// carried in from prior lines, and returns any delimiters opened plus the
// depth to carry out. While the depth is positive the line is inside an
// arithmetic span, where `<<` is a shift and no opener, comment, or quote is
// recognized; the span opens at `$((` or a bare command-position `((` (two
// adjacent parens — a subshell writes `( (` or a lone `(`) and closes when the
// paren depth returns to zero. Quotes and comments are per-line.
func scanLine(line string, arith int) ([]delim, int) {
	var out []delim
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		if arith > 0 {
			switch c {
			case '(':
				arith++
			case ')':
				arith--
			}
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '#':
			if i == 0 || isWordBreak(line[i-1]) {
				return out, arith // rest of the line is a comment
			}
		case '$':
			// Arithmetic expansion $(( … )).
			if i+2 < len(line) && line[i+1] == '(' && line[i+2] == '(' {
				arith = 2
				i += 2 // consume both '('
			}
		case '(':
			// Bare arithmetic command (( … )) — two adjacent parens. A
			// subshell is "( (" (separated) or a lone "(", neither of which
			// starts an arithmetic span.
			if i+1 < len(line) && line[i+1] == '(' {
				arith = 2
				i++ // consume the second '('
			}
		case '<':
			if i+1 >= len(line) || line[i+1] != '<' {
				continue
			}
			if i+2 < len(line) && line[i+2] == '<' {
				i += 2 // here-string, no body
				continue
			}
			j := i + 2
			stripTabs := false
			if j < len(line) && line[j] == '-' {
				stripTabs = true
				j++
			}
			for j < len(line) && (line[j] == ' ' || line[j] == '\t') {
				j++
			}
			text, next := readDelim(line, j)
			if text != "" {
				out = append(out, delim{text: text, stripTabs: stripTabs})
			}
			if next <= i {
				next = i + 1
			}
			i = next - 1
		}
	}
	return out, arith
}

// readDelim reads a heredoc delimiter word starting at j and returns its text
// and the index just past it. A single- or double-quoted delimiter is read to
// its closing quote; a bare delimiter (optionally backslash-escaped) is read
// to the next word break.
func readDelim(s string, j int) (string, int) {
	if j >= len(s) {
		return "", j
	}
	switch s[j] {
	case '\'':
		if k := strings.IndexByte(s[j+1:], '\''); k >= 0 {
			return s[j+1 : j+1+k], j + 1 + k + 1
		}
		return "", len(s)
	case '"':
		if k := strings.IndexByte(s[j+1:], '"'); k >= 0 {
			return s[j+1 : j+1+k], j + 1 + k + 1
		}
		return "", len(s)
	default:
		start := j
		if s[j] == '\\' {
			start = j + 1
			j++
		}
		k := start
		for k < len(s) && !isDelimEnd(s[k]) {
			k++
		}
		return s[start:k], k
	}
}

func isDelimEnd(b byte) bool {
	switch b {
	case ' ', '\t', ';', '&', '|', '<', '>', '(', ')':
		return true
	}
	return false
}

func isWordBreak(b byte) bool {
	switch b {
	case ' ', '\t', ';', '&', '|', '(':
		return true
	}
	return false
}
