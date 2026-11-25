package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mod = "github.com/punt-labs/ethos/v4/"

func profile(entries ...[3]any) string {
	var b strings.Builder
	b.WriteString("mode: atomic\n")
	for i, e := range entries {
		count := 0
		if e[2].(bool) {
			count = 1
		}
		fmt.Fprintf(&b, "%s%s:%d.1,%d.20 %d %d\n", mod, e[0], i+1, i+1, e[1], count)
	}
	return b.String()
}

func compare(t *testing.T, head, base string, autorepairTests bool) (int, string) {
	t.Helper()
	root := t.TempDir()
	if autorepairTests {
		dir := filepath.Join(root, "internal/autorepair")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "x_test.go"), []byte("package autorepair\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, b := filepath.Join(root, "head.out"), filepath.Join(root, "base.out")
	for file, contents := range map[string]string{p: head, b: base} {
		if err := os.WriteFile(file, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	code := run([]string{"-profile", p, "-base-profile", b, "-root", root}, &out, &out)
	return code, out.String()
}

func TestToleranceIsPointOne(t *testing.T) {
	for _, tc := range []struct {
		name string
		base int
		fail bool
	}{{"inside", 991, false}, {"outside", 992, true}} {
		t.Run(tc.name, func(t *testing.T) {
			head := profile([3]any{"internal/a/a.go", 990, true}, [3]any{"internal/a/b.go", 10, false}, [3]any{"internal/b/a.go", 1000, true})
			base := profile([3]any{"internal/a/a.go", tc.base, true}, [3]any{"internal/a/b.go", 1000 - tc.base, false}, [3]any{"internal/b/a.go", 1000, false})
			if code, out := compare(t, head, base, false); (code != 0) != tc.fail {
				t.Fatalf("exit %d: %s", code, out)
			}
		})
	}
}

func TestFailsWhenOverallBelowMergeBase(t *testing.T) {
	head := profile([3]any{"internal/a/a.go", 10, true}, [3]any{"internal/b/b.go", 10, false})
	base := profile([3]any{"internal/a/a.go", 10, true})
	if code, out := compare(t, head, base, false); code == 0 || !strings.Contains(out, "overall: 50.0% is below merge base 100.0%") {
		t.Fatalf("want overall failure, got %d: %s", code, out)
	}
}

func TestAutorepairFloorOnlyWithTestFiles(t *testing.T) {
	prof := profile([3]any{"internal/autorepair/e.go", 10, true}, [3]any{"internal/autorepair/f.go", 10, false})
	if code, out := compare(t, prof, prof, false); code != 0 {
		t.Fatalf("floor applied without test files: %s", out)
	}
	if code, out := compare(t, prof, prof, true); code == 0 || !strings.Contains(out, "81.0") {
		t.Fatalf("want 81.0 floor failure, got %d: %s", code, out)
	}
}

func TestMissingPackageInProfileFails(t *testing.T) {
	head := profile([3]any{"internal/a/a.go", 10, true})
	base := profile([3]any{"internal/a/a.go", 10, true}, [3]any{"internal/gone/a.go", 10, true})
	if code, out := compare(t, head, base, false); code == 0 || !strings.Contains(out, "internal/gone: measured at merge base but absent") {
		t.Fatalf("want missing-package failure, got %d: %s", code, out)
	}
}

func TestAutorepairFloorWithoutBase(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal/autorepair")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x_test.go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "coverage.out")
	for _, tc := range []struct {
		name, contents, message string
		code                    int
	}{
		{"pass", profile([3]any{"internal/autorepair/a.go", 81, true}, [3]any{"internal/autorepair/b.go", 19, false}), "coverage-check: ok", 0},
		{"below floor", profile([3]any{"internal/autorepair/a.go", 80, true}, [3]any{"internal/autorepair/b.go", 20, false}), "80.0% is below the 81.0 floor", 1},
		{"missing package", profile([3]any{"cmd/ethos/a.go", 1, true}), "internal/autorepair: absent from head profile", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(p, []byte(tc.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if code := run([]string{"-profile", p, "-autorepair-floor-only", "-root", root}, &out, &out); code != tc.code || !strings.Contains(out.String(), tc.message) {
				t.Fatalf("want exit %d and %q, got %d: %s", tc.code, tc.message, code, &out)
			}
		})
	}
}

func TestReadProfileRejectsNegativeCounts(t *testing.T) {
	for _, line := range []string{"-5 1", "5 -1"} {
		prof := "mode: atomic\n" + mod + "internal/a/a.go:1.1,1.2 " + line + "\n"
		if code, out := compare(t, prof, prof, false); code == 0 || !strings.Contains(out, "malformed") {
			t.Errorf("line %q: want malformed rejection, got %d: %s", line, code, out)
		}
	}
}

func TestFloorAndBaseCompareThePrintedFigure(t *testing.T) {
	prof := profile([3]any{"internal/autorepair/e.go", 2024, true}, [3]any{"internal/autorepair/f.go", 476, false})
	if code, out := compare(t, prof, prof, true); code != 0 {
		t.Fatalf("80.96%%, displayed as 81.0%%, must satisfy the floor: %s", out)
	}
}
