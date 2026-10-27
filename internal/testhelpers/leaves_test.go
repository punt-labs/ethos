package testhelpers

import (
	"bytes"
	"encoding/json"
	"testing"
)

func leavesOf(t *testing.T, doc string) Leaves {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader([]byte(doc)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return CollectLeaves(v)
}

func TestLostLeaf(t *testing.T) {
	cases := []struct {
		name, want, got string
		lost            bool
	}{
		{"same", `{"t":{"x":"a","y":1}}`, `{"t":{"x":"a","y":1}}`, false},
		{"value rewritten", `{"t":{"x":"/home/z/a","y":1}}`, `{"t":{"x":"~/a","y":1}}`, false},
		{"key renamed", `{"t":{"/home/z":"a"}}`, `{"t":{"[redacted-key]":"a"}}`, false},
		{"extra added", `{"t":{"x":"a"}}`, `{"t":{"x":"a","y":2}}`, false},
		// The mutant: x is rewritten (a surplus) and y is dropped. One global
		// count would let the one cover the other.
		{"y dropped while x rewritten", `{"t":{"x":"/home/z/a","y":1}}`, `{"t":{"x":"~/a"}}`, true},
		{"y dropped, x kept", `{"t":{"x":"a","y":1}}`, `{"t":{"x":"a"}}`, true},
		{"value dropped to nothing", `{"t":{"x":"a"}}`, `{"t":{"x":""}}`, false},
		{"leaf dropped from a list", `{"t":{"x":["a","b"]}}`, `{"t":{"x":["a"]}}`, true},
		{"bool and null count", `{"t":{"x":true,"y":null}}`, `{"t":{"x":true}}`, true},
		{"whole field vanishes", `{"t":{"x":"a"},"u":"b"}`, `{"t":{"x":"a"}}`, true},
		{"deep value dropped", `{"t":{"x":{"p":{"q":"a","r":"b"}}}}`, `{"t":{"x":{"p":{"q":"a"}}}}`, true},
	}
	for _, c := range cases {
		_, lost := LostLeaf(leavesOf(t, c.want), leavesOf(t, c.got))
		if lost != c.lost {
			t.Errorf("%s: lost = %v, want %v", c.name, lost, c.lost)
		}
	}
}
