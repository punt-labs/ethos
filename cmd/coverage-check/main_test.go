package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mod = "github.com/punt-labs/ethos/v4/"

// profile builds a coverage.out where each entry is (file, statements, covered).
func profile(entries ...[3]any) string {
	var b strings.Builder
	b.WriteString("mode: atomic\n")
	for i, e := range entries {
		count := 0
		if e[2].(bool) {
			count = 1
		}
		b.WriteString(mod + e[0].(string) + ":" + itoa(i+1) + ".1," + itoa(i+1) + ".20 " + itoa(e[1].(int)) + " " + itoa(count) + "\n")
	}
	return b.String()
}

func itoa(n int) string {
	var b bytes.Buffer
	if n == 0 {
		return "0"
	}
	var digits []byte
	for ; n > 0; n /= 10 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
	}
	b.Write(digits)
	return b.String()
}

func setup(t *testing.T, prof, baseline string, autorepairTests bool) (root, profPath, basePath string) {
	t.Helper()
	root = t.TempDir()
	if autorepairTests {
		dir := filepath.Join(root, "internal/autorepair")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "x_test.go"), []byte("package autorepair\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	profPath = filepath.Join(root, "coverage.out")
	basePath = filepath.Join(root, "coverage-baseline.txt")
	if err := os.WriteFile(profPath, []byte(prof), 0o644); err != nil {
		t.Fatal(err)
	}
	if baseline != "" {
		if err := os.WriteFile(basePath, []byte(baseline), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func runTool(t *testing.T, root, prof, base string, extra ...string) (int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(append([]string{"-profile", prof, "-baseline", base, "-root", root}, extra...), &out, &errb)
	return code, out.String() + errb.String()
}

func TestPassesAtBaseline(t *testing.T) {
	prof := profile([3]any{"internal/a/a.go", 10, true}, [3]any{"internal/b/b.go", 10, false}, [3]any{"internal/b/c.go", 10, true})
	root, p, b := setup(t, prof, "overall-target 50.0\ninternal/a 100.0\ninternal/b 50.0\n", false)
	code, out := runTool(t, root, p, b)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if !strings.Contains(out, "internal/a") || !strings.Contains(out, "overall") {
		t.Fatalf("table missing rows: %s", out)
	}
}

func TestFailsWhenPackageDrops(t *testing.T) {
	prof := profile([3]any{"internal/a/a.go", 10, true}, [3]any{"internal/a/b.go", 10, false})
	root, p, b := setup(t, prof, "overall-target 10.0\ninternal/a 60.0\n", false)
	code, out := runTool(t, root, p, b)
	if code == 0 || !strings.Contains(out, "internal/a") || !strings.Contains(out, "below baseline") {
		t.Fatalf("want failure naming internal/a, got %d: %s", code, out)
	}
}

func TestToleranceIsPointOne(t *testing.T) {
	// 99 of 100 statements = 99.0; baseline 99.1 is inside tolerance, 99.2 is not.
	prof := profile([3]any{"internal/a/a.go", 99, true}, [3]any{"internal/a/b.go", 1, false})
	root, p, b := setup(t, prof, "overall-target 10.0\ninternal/a 99.1\n", false)
	if code, out := runTool(t, root, p, b); code != 0 {
		t.Fatalf("within tolerance failed: %s", out)
	}
	root, p, b = setup(t, prof, "overall-target 10.0\ninternal/a 99.2\n", false)
	if code, _ := runTool(t, root, p, b); code == 0 {
		t.Fatal("beyond tolerance passed")
	}
}

func TestFailsWhenOverallBelowTarget(t *testing.T) {
	prof := profile([3]any{"internal/a/a.go", 10, true}, [3]any{"internal/a/b.go", 10, false})
	root, p, b := setup(t, prof, "overall-target 85.0\ninternal/a 50.0\n", false)
	code, out := runTool(t, root, p, b)
	if code == 0 || !strings.Contains(out, "overall") {
		t.Fatalf("want overall failure, got %d: %s", code, out)
	}
}

func TestAutorepairFloorOnlyWithTestFiles(t *testing.T) {
	prof := profile([3]any{"internal/autorepair/e.go", 10, true}, [3]any{"internal/autorepair/f.go", 10, false})
	base := "overall-target 10.0\ninternal/autorepair 50.0\n"
	root, p, b := setup(t, prof, base, false)
	if code, out := runTool(t, root, p, b); code != 0 {
		t.Fatalf("floor applied without test files: %s", out)
	}
	root, p, b = setup(t, prof, base, true)
	code, out := runTool(t, root, p, b)
	if code == 0 || !strings.Contains(out, "95.0") {
		t.Fatalf("want 95.0 floor failure, got %d: %s", code, out)
	}
}

func TestMissingPackageInProfileFails(t *testing.T) {
	prof := profile([3]any{"internal/a/a.go", 10, true})
	root, p, b := setup(t, prof, "overall-target 10.0\ninternal/a 100.0\ninternal/gone 80.0\n", false)
	code, out := runTool(t, root, p, b)
	if code == 0 || !strings.Contains(out, "internal/gone") {
		t.Fatalf("want missing-package failure, got %d: %s", code, out)
	}
}

func TestMissingBaselineFails(t *testing.T) {
	root, p, b := setup(t, profile([3]any{"internal/a/a.go", 1, true}), "", false)
	if code, _ := runTool(t, root, p, b); code == 0 {
		t.Fatal("missing baseline passed")
	}
}

func TestWriteGeneratesBaselineAndPreservesTargetAndComments(t *testing.T) {
	prof := profile([3]any{"internal/a/a.go", 10, true}, [3]any{"internal/b/b.go", 10, false})
	root, p, b := setup(t, prof, "# unmeasured: internal/zzz (host-specific failure)\noverall-target 42.5\n", false)
	if code, out := runTool(t, root, p, b, "-write"); code != 0 {
		t.Fatalf("write failed: %s", out)
	}
	data, err := os.ReadFile(b)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"# unmeasured: internal/zzz", "overall-target 42.5", "overall-current 50.0", "internal/a 100.0", "internal/b 0.0"} {
		if !strings.Contains(got, want) {
			t.Fatalf("baseline missing %q:\n%s", want, got)
		}
	}
	if code, out := runTool(t, root, p, b); code != 0 {
		t.Fatalf("freshly written baseline fails check: %s", out)
	}
}
