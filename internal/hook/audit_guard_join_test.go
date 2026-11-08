package hook

import "testing"

func TestJoinRules(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"", "", ""},
		{"", "b", "b"},
		{"a", "", "a"},
		{"a", "b", "a; it b"},
	}
	for _, c := range cases {
		if got := joinRules(c.a, c.b); got != c.want {
			t.Errorf("joinRules(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}
