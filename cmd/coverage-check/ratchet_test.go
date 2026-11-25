package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareMeasuredProfiles(t *testing.T) {
	full := profile([3]any{"internal/a/a.go", 10, true})
	none := profile([3]any{"internal/a/a.go", 10, false})
	for _, tc := range []struct {
		name, head, base, want string
		fail                   bool
	}{
		{"matched", full, full, "coverage-check: ok", false},
		{"regression", none, full, "internal/a: 0.0% is below merge base 100.0%", true},
		{"missing base", full, "", "base profile", true},
		{"empty base", full, "mode: atomic\n", "no statements", true},
		{"invalid head", "invalid\n", full, "head profile", true},
		{"invalid base", full, "mode: invalid\n" + full[strings.IndexByte(full, '\n')+1:], "invalid coverage mode", true},
		{"invalid range", full, "mode: atomic\nnot-a-range 10 1\n", "malformed", true},
		{"backwards range", full, "mode: atomic\nfile.go:4.1,1.3 10 1\n", "malformed", true},
		{"overflowing count", full, "mode: atomic\nfile.go:1.1,1.3 10 9999999999999999999999999\n", "malformed", true},
		{"contradictory duplicate", full, full + mod + "internal/a/a.go:1.1,1.20 5 1\n", "inconsistent statement counts", true},
		{"duplicate with zero count", full, full + mod + "internal/a/a.go:1.1,1.20 10 0\n", "coverage-check: ok", false},
		{"overlong line", full, "mode: atomic\n" + strings.Repeat("x", 1<<17), "token too long", true},
		{"different mode", full, strings.Replace(none, "atomic", "set", 1), "coverage modes differ", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			head, base := filepath.Join(dir, "head.out"), filepath.Join(dir, "base.out")
			if err := os.WriteFile(head, []byte(tc.head), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.base != "" {
				if err := os.WriteFile(base, []byte(tc.base), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			code := run([]string{"-profile", head, "-base-profile", base, "-root", dir}, &out, &out)
			if (code != 0) != tc.fail || !strings.Contains(out.String(), tc.want) {
				t.Fatalf("exit %d, want %q; output:\n%s", code, tc.want, &out)
			}
		})
	}
}

func TestCompareRequiresExactlyTwoNamedProfiles(t *testing.T) {
	for _, args := range [][]string{nil, {"-base-profile", "base.out", "extra"}} {
		var out bytes.Buffer
		if code := run(args, &out, &out); code != 2 || !strings.Contains(out.String(), "-base-profile is required") {
			t.Fatalf("exit %d: %s", code, &out)
		}
	}
}
