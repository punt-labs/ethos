// Package textscan holds the byte-level line helpers shared by the ethos
// packages that scan text files — the claudemd import writer, the githook
// chainer, and the doctor seal check — plus a heredoc-aware classifier for
// shell source.
//
// The heredoc classifier is deliberately narrow: it recognizes here-document
// bodies so a marker-shaped line or an `ethos audit seal` mention inside one
// is never misread as real shell. It is not a shell lexer — it tracks
// heredocs and nothing else, the same bounded approach claudemd takes for
// markdown code fences.
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
	lines := SplitKeepEnds(data)
	mask := make([]bool, len(lines))
	var queue []delim
	for i, raw := range lines {
		content := StripTerminator(raw)
		if len(queue) > 0 {
			if queue[0].terminates(content) {
				queue = queue[1:]
				continue // terminator line is structural, not body
			}
			mask[i] = true
			continue
		}
		queue = append(queue, parseHeredocStarts(content)...)
	}
	return mask
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

// parseHeredocStarts returns the heredoc delimiters opened on line, in order.
// It tracks quotes and stops at a comment so `<<` inside a string or comment
// is not misread as a redirection.
func parseHeredocStarts(line string) []delim {
	var out []delim
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
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
				return out // rest of the line is a comment
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
	return out
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
