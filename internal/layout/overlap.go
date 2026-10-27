package layout

import "strings"

// Overlap reports whether some path matches both patterns. It decides the
// question on the segment sequences, so it does not depend on sampled paths:
// two patterns overlap iff their segment lists can be aligned so that every
// aligned pair of segments matches a common string, with "**" absorbing any
// number of segments.
func Overlap(a, b string) bool {
	return overlapSegments(strings.Split(a, "/"), strings.Split(b, "/"))
}

func overlapSegments(a, b []string) bool {
	switch {
	case len(a) == 0 && len(b) == 0:
		return true
	case len(a) > 0 && a[0] == "**":
		return overlapSegments(a[1:], b) || (len(b) > 0 && overlapSegments(a, b[1:]))
	case len(b) > 0 && b[0] == "**":
		return overlapSegments(a, b[1:]) || (len(a) > 0 && overlapSegments(a[1:], b))
	case len(a) == 0 || len(b) == 0:
		return false
	}
	return segmentsIntersect(tokens(a[0]), tokens(b[0])) && overlapSegments(a[1:], b[1:])
}

// token is one element of a single-segment glob: a run of any characters, any
// one rune (as path.Match counts), or one literal rune.
type token struct {
	kind byte // '*', '?' or 'c'
	c    rune
}

// tokens parses a path.Match segment. Character classes are not used by any
// table entry and are rejected by the package tests.
func tokens(seg string) []token {
	var out []token
	rs := []rune(seg)
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '*':
			out = append(out, token{kind: '*'})
		case '?':
			out = append(out, token{kind: '?'})
		case '\\':
			if i+1 < len(rs) {
				i++
			}
			out = append(out, token{kind: 'c', c: rs[i]})
		default:
			out = append(out, token{kind: 'c', c: rs[i]})
		}
	}
	return out
}

// segmentsIntersect reports whether some string matches both token lists.
func segmentsIntersect(a, b []token) bool {
	type key struct{ i, j int }
	seen := make(map[key]bool)
	var walk func(i, j int) bool
	walk = func(i, j int) bool {
		k := key{i, j}
		if v, ok := seen[k]; ok {
			return v
		}
		var r bool
		switch {
		case i == len(a) && j == len(b):
			r = true
		case i < len(a) && a[i].kind == '*':
			r = walk(i+1, j) || (j < len(b) && walk(i, j+1))
		case j < len(b) && b[j].kind == '*':
			r = walk(i, j+1) || (i < len(a) && walk(i+1, j))
		case i == len(a) || j == len(b):
			r = false
		case a[i].kind == '?' || b[j].kind == '?' || a[i].c == b[j].c:
			r = walk(i+1, j+1)
		}
		seen[k] = r
		return r
	}
	return walk(0, 0)
}
