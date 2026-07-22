package textscan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSplitKeepEndsRoundTrip(t *testing.T) {
	inputs := []string{
		"a\nb\n",
		"a\r\nb\r\n",
		"a\rb\r",
		"no terminator",
		"",
		"trailing\n\n",
	}
	for _, in := range inputs {
		got := ""
		for _, l := range SplitKeepEnds([]byte(in)) {
			got += l
		}
		if got != in {
			t.Errorf("round-trip: got %q, want %q", got, in)
		}
	}
}

func TestStripTerminator(t *testing.T) {
	cases := map[string]string{
		"x\n":   "x",
		"x\r\n": "x",
		"x\r":   "x",
		"x":     "x",
	}
	for in, want := range cases {
		if got := StripTerminator(in); got != want {
			t.Errorf("StripTerminator(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetectEOL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"a\nb\n", "\n"},
		{"a\r\nb\r\n", "\r\n"},
		{"a\rb\r", "\r"},
		{"", "\n"},
	}
	for _, c := range cases {
		if got := string(DetectEOL([]byte(c.in))); got != c.want {
			t.Errorf("DetectEOL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSamePath(t *testing.T) {
	dir := t.TempDir()
	if !SamePath(dir, dir) {
		t.Error("SamePath(dir, dir) = false")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if !SamePath(link, dir) {
		t.Error("SamePath through a symlink = false")
	}
	if SamePath(dir, t.TempDir()) {
		t.Error("SamePath of two distinct dirs = true")
	}
}

func TestHeredocMask(t *testing.T) {
	// mask indices refer to SplitKeepEnds line order.
	tests := []struct {
		name string
		src  string
		want []bool // per line: inside a heredoc body
	}{
		{
			name: "quoted heredoc body is opaque, terminator and code are not",
			src:  "#!/bin/sh\ncat <<'EOF'\nbody line\nEOF\necho done\n",
			want: []bool{false, false, true, false, false},
		},
		{
			name: "unquoted heredoc",
			src:  "cat <<EOF\nbody\nEOF\n",
			want: []bool{false, true, false},
		},
		{
			name: "tab-stripping heredoc terminator may be tab-indented",
			src:  "cat <<-END\n\tbody\n\tEND\nafter\n",
			want: []bool{false, true, false, false},
		},
		{
			name: "here-string has no body",
			src:  "cat <<<word\nafter\n",
			want: []bool{false, false},
		},
		{
			name: "<< inside a comment is not a heredoc",
			src:  "echo hi # note << EOF\nafter\n",
			want: []bool{false, false},
		},
		{
			name: "<< inside quotes is not a heredoc",
			src:  "echo \"a << b\"\nafter\n",
			want: []bool{false, false},
		},
		{
			name: "stacked heredocs on one line",
			src:  "cmd <<A <<B\naaa\nA\nbbb\nB\nend\n",
			want: []bool{false, true, false, true, false, false},
		},
		{
			name: "unterminated heredoc runs to EOF",
			src:  "cat <<EOF\nbody1\nbody2\n",
			want: []bool{false, true, true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HeredocMask([]byte(tt.src))
			if len(got) != len(tt.want) {
				t.Fatalf("mask len = %d, want %d (%v)", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("line %d: mask = %v, want %v (full %v)", i, got[i], tt.want[i], got)
				}
			}
		})
	}
}
