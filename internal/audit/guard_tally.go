package audit

import (
	"fmt"
	"strings"
)

// GuardTally counts the lines an audit guard rewrote or replaced by a loss
// marker in one run, with the distinct rules that forced it, so a run says so
// in one line per kind however many lines it touched. A rule names the reason,
// never a value.
type GuardTally struct {
	rewritten, replaced tallyKind
}

type tallyKind struct {
	n     int
	rules []string // distinct, in order of first appearance
}

func (k *tallyKind) add(rule string) {
	k.n++
	if rule == "" {
		return
	}
	for _, r := range k.rules {
		if r == rule {
			return
		}
	}
	k.rules = append(k.rules, rule)
}

// Add records one line the guard changed. marker says the whole line became a
// loss marker; a rule of "" means the line was unchanged and is not counted.
func (t *GuardTally) Add(rule string, marker bool) {
	switch {
	case marker:
		t.replaced.add(rule)
	case rule != "":
		t.rewritten.add(rule)
	}
}

// Notices returns the lines to print, each beginning with prefix: one for the
// rewritten lines and one for the loss markers, in that order, omitting a kind
// that has none.
func (t *GuardTally) Notices(prefix string) []string {
	var out []string
	if n := t.rewritten.n; n > 0 {
		out = append(out, fmt.Sprintf("%s %s rewritten before sealing%s", prefix, lines(n), t.rewritten.why()))
	}
	if n := t.replaced.n; n > 0 {
		out = append(out, fmt.Sprintf("%s %s replaced by a loss marker before sealing%s", prefix, lines(n), t.replaced.why()))
	}
	return out
}

func lines(n int) string {
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}

func (k tallyKind) why() string {
	if len(k.rules) == 0 {
		return ""
	}
	return " (it " + strings.Join(k.rules, "; it ") + ")"
}
