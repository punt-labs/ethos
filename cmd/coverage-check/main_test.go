package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
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
		if !strings.Contains(baseline, "# os:") {
			baseline = "# os: " + runtime.GOOS + "\n" + baseline
		}
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

func TestCheckLeavesUnmeasuredPackagesOutOfOverallAndFloors(t *testing.T) {
	// internal/bad has a partial profile (its tests timed out); it must not drag overall down.
	prof := profile([3]any{"internal/a/a.go", 10, true}, [3]any{"internal/bad/b.go", 90, false})
	base := "# unmeasured: internal/bad (tests fail on the host)\noverall-target 100.0\ninternal/a 100.0\n"
	root, p, b := setup(t, prof, base, false)
	code, out := runTool(t, root, p, b)
	if code != 0 || !strings.Contains(out, "unmeasured") {
		t.Fatalf("want pass with an unmeasured row, got %d: %s", code, out)
	}
}

func TestWriteExcludeRecordsUnmeasuredPackagesAndDropsThemFromOverall(t *testing.T) {
	prof := profile([3]any{"internal/a/a.go", 10, true}, [3]any{"internal/bad/b.go", 90, false})
	root, p, b := setup(t, prof, "", false)
	if code, out := runTool(t, root, p, b, "-write", "-exclude", "internal/bad,internal/gone"); code != 0 {
		t.Fatalf("write failed: %s", out)
	}
	data, err := os.ReadFile(b)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"# unmeasured: internal/bad", "# unmeasured: internal/gone", "overall-current 100.0", "overall-target 100.0", "internal/a 100.0"} {
		if !strings.Contains(got, want) {
			t.Fatalf("baseline missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\ninternal/bad ") {
		t.Fatalf("excluded package has a baseline line:\n%s", got)
	}
	if code, out := runTool(t, root, p, b, "-write", "-exclude", "internal/bad,internal/gone"); code != 0 {
		t.Fatalf("second write failed: %s", out)
	}
	again, _ := os.ReadFile(b)
	if strings.Count(string(again), "# unmeasured: internal/bad") != 1 {
		t.Fatalf("rewrite duplicated the unmeasured comment:\n%s", again)
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

func TestWriteWithoutExcludeHonoursExistingUnmeasuredComments(t *testing.T) {
	prof := profile([3]any{"internal/a/a.go", 10, true}, [3]any{"internal/u/u.go", 90, false})
	root, p, b := setup(t, prof, "# unmeasured: internal/u (host-specific)\noverall-target 50.0\n", false)
	if code, out := runTool(t, root, p, b, "-write"); code != 0 {
		t.Fatalf("write failed: %s", out)
	}
	data, err := os.ReadFile(b)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Contains(got, "\ninternal/u ") {
		t.Fatalf("unmeasured package got a baseline line:\n%s", got)
	}
	if !strings.Contains(got, "overall-current 100.0") {
		t.Fatalf("overall-current folded in the unmeasured package:\n%s", got)
	}
	if code, out := runTool(t, root, p, b); code != 0 {
		t.Fatalf("written baseline fails its own check: %s", out)
	}
}

func TestReadBaselineRejectsNonFiniteAndOutOfRange(t *testing.T) {
	prof := profile([3]any{"internal/a/a.go", 10, true})
	for _, bad := range []string{
		"overall-target 10.0\ninternal/a NaN\n",
		"overall-target 10.0\ninternal/a +Inf\n",
		"overall-target 10.0\ninternal/a 100.1\n",
		"overall-target 10.0\ninternal/a -0.5\n",
		"overall-target NaN\ninternal/a 100.0\n",
		"overall-target 101.0\ninternal/a 100.0\n",
		"overall-target 10.0\noverall-current 250.0\ninternal/a 100.0\n",
	} {
		root, p, b := setup(t, prof, bad, false)
		code, out := runTool(t, root, p, b)
		if code == 0 || !strings.Contains(out, "bad percentage") {
			t.Errorf("baseline %q: want a bad-percentage rejection, got %d: %s", bad, code, out)
		}
	}
}

func TestReadBaselineRejectsDuplicateOverallTarget(t *testing.T) {
	prof := profile([3]any{"internal/a/a.go", 10, true})
	root, p, b := setup(t, prof, "overall-target 10.0\noverall-target 5.0\ninternal/a 100.0\n", false)
	code, out := runTool(t, root, p, b)
	if code == 0 || !strings.Contains(out, "duplicate") {
		t.Fatalf("want duplicate overall-target rejection, got %d: %s", code, out)
	}
}

func TestUnmeasuredAutorepairIsRefused(t *testing.T) {
	prof := profile([3]any{"internal/autorepair/e.go", 10, false})
	base := "# unmeasured: internal/autorepair (host)\noverall-target 0.0\n"
	root, p, b := setup(t, prof, base, true)
	code, out := runTool(t, root, p, b)
	if code == 0 || !strings.Contains(out, "internal/autorepair") || !strings.Contains(out, "cannot be unmeasured") {
		t.Fatalf("want refusal of an unmeasured internal/autorepair, got %d: %s", code, out)
	}
}

func TestNewPackageWarnsUngated(t *testing.T) {
	prof := profile([3]any{"internal/a/a.go", 10, true}, [3]any{"internal/fresh/f.go", 10, false})
	root, p, b := setup(t, prof, "overall-target 10.0\ninternal/a 100.0\n", false)
	code, out := runTool(t, root, p, b)
	if code != 0 {
		t.Fatalf("new package must not fail: %s", out)
	}
	if !strings.Contains(out, "WARN internal/fresh") || !strings.Contains(out, "ungated") {
		t.Fatalf("want a visible ungated warning naming internal/fresh: %s", out)
	}
}

func TestOSMismatchFails(t *testing.T) {
	prof := profile([3]any{"internal/p/p.go", 10, true})
	root, p, b := setup(t, prof, "# os: darwin\noverall-target 10.0\ninternal/p 100.0\n", false)
	code, out := runTool(t, root, p, b, "-os", "linux")
	want := "baseline was generated on darwin; this host is linux; regenerate the baseline on darwin"
	if code == 0 || !strings.Contains(out, want) {
		t.Fatalf("want failure %q, got %d: %s", want, code, out)
	}
	if code, out := runTool(t, root, p, b, "-os", "darwin"); code != 0 {
		t.Fatalf("matching os failed: %s", out)
	}
}

func TestWriteRejectsEmptyOS(t *testing.T) {
	root, p, b := setup(t, profile([3]any{"internal/a/a.go", 10, true}), "", false)
	if code, out := runTool(t, root, p, b, "-write", "-os", ""); code == 0 {
		t.Fatalf("empty -os accepted: %s", out)
	}
}

func TestWriteRejectsUnknownOS(t *testing.T) {
	root, p, b := setup(t, profile([3]any{"internal/a/a.go", 10, true}), "", false)
	if code, out := runTool(t, root, p, b, "-write", "-os", "beos"); code == 0 || !strings.Contains(out, "beos") {
		t.Fatalf("unknown -os accepted: %d %s", code, out)
	}
}

func TestReadBaselineRejectsDuplicateOS(t *testing.T) {
	root, p, b := setup(t, profile([3]any{"internal/a/a.go", 10, true}), "# os: darwin\n# os: linux\noverall-target 10.0\ninternal/a 100.0\n", false)
	code, out := runTool(t, root, p, b)
	if code == 0 || !strings.Contains(out, "duplicate") {
		t.Fatalf("want duplicate os rejection, got %d: %s", code, out)
	}
}

func TestReadBaselineRejectsEmptyOS(t *testing.T) {
	root, p, b := setup(t, profile([3]any{"internal/a/a.go", 10, true}), "# os: \noverall-target 10.0\ninternal/a 100.0\n", false)
	if code, out := runTool(t, root, p, b); code == 0 {
		t.Fatalf("empty os accepted: %s", out)
	}
}

func TestReadBaselineRejectsUnknownOS(t *testing.T) {
	root, p, b := setup(t, profile([3]any{"internal/a/a.go", 10, true}), "# os: beos\noverall-target 10.0\ninternal/a 100.0\n", false)
	if code, out := runTool(t, root, p, b); code == 0 {
		t.Fatalf("unknown os accepted: %s", out)
	}
}

func TestWriteWarnsForUnmeasuredPackageInProfile(t *testing.T) {
	prof := profile([3]any{"internal/a/a.go", 10, true}, [3]any{"internal/u/u.go", 10, true})
	root, p, b := setup(t, prof, "# unmeasured: internal/u (x)\noverall-target 10.0\n", false)
	code, out := runTool(t, root, p, b, "-write")
	if code != 0 || !strings.Contains(out, "WARN internal/u has coverage data but is listed unmeasured") {
		t.Fatalf("want WARN for internal/u, got %d: %s", code, out)
	}
}

func TestWriteDoesNotReaddCommentWhenCustomOneExists(t *testing.T) {
	prof := profile([3]any{"internal/a/a.go", 10, true})
	root, p, b := setup(t, prof, "# unmeasured: internal/ghost (flaky on ci)\noverall-target 10.0\n", false)
	if code, out := runTool(t, root, p, b, "-write"); code != 0 {
		t.Fatalf("write failed: %s", out)
	}
	data, _ := os.ReadFile(b)
	if n := strings.Count(string(data), "# unmeasured: internal/ghost"); n != 1 {
		t.Fatalf("want exactly one unmeasured comment for internal/ghost, got %d:\n%s", n, data)
	}
}

func TestReadProfileRejectsNegativeCounts(t *testing.T) {
	for _, line := range []string{"-5 1", "5 -1"} {
		prof := "mode: atomic\n" + mod + "internal/a/a.go:1.1,1.2 " + line + "\n"
		root, p, b := setup(t, prof, "overall-target 10.0\ninternal/a 100.0\n", false)
		if code, out := runTool(t, root, p, b); code == 0 || !strings.Contains(out, "malformed") {
			t.Errorf("line %q: want malformed rejection, got %d: %s", line, code, out)
		}
	}
}

func TestWriteRecordsOS(t *testing.T) {
	prof := profile([3]any{"internal/a/a.go", 10, true})
	root, p, b := setup(t, prof, "", false)
	if code, out := runTool(t, root, p, b, "-write", "-os", "plan9"); code != 0 {
		t.Fatalf("write failed: %s", out)
	}
	data, _ := os.ReadFile(b)
	if !strings.Contains(string(data), "\n# os: plan9\n") {
		t.Fatalf("baseline does not record the os:\n%s", data)
	}
}
