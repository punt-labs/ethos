package audit

import (
	"reflect"
	"testing"
)

func TestGuardTally_Notices(t *testing.T) {
	cases := []struct {
		name string
		add  func(*GuardTally)
		want []string
	}{
		{"empty", func(*GuardTally) {}, nil},
		{"unchanged lines are not counted", func(g *GuardTally) { g.Add("", false) }, nil},
		{"one line", func(g *GuardTally) { g.Add("a", false) },
			[]string{"p: 1 line rewritten before sealing (it a)"}},
		{"many lines, one rule", func(g *GuardTally) {
			for i := 0; i < 130; i++ {
				g.Add("held a path", false)
			}
		}, []string{"p: 130 lines rewritten before sealing (it held a path)"}},
		{"distinct rules in order of first appearance", func(g *GuardTally) {
			g.Add("b", false)
			g.Add("a", false)
			g.Add("b", false)
		}, []string{"p: 3 lines rewritten before sealing (it b; it a)"}},
		{"loss markers have their own line", func(g *GuardTally) {
			g.Add("r", false)
			g.Add("m", true)
			g.Add("m", true)
		}, []string{
			"p: 1 line rewritten before sealing (it r)",
			"p: 2 lines replaced by a loss marker before sealing (it m)",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var g GuardTally
			c.add(&g)
			if got := g.Notices("p:"); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Notices = %q, want %q", got, c.want)
			}
		})
	}
}
