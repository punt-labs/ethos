package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeStatementProfiles(t *testing.T) {
	dir := t.TempDir()
	head, child := filepath.Join(dir, "head.out"), filepath.Join(dir, "child.out")
	for file, body := range map[string]string{
		head:  profile([3]any{"internal/a/a.go", 10, false}, [3]any{"internal/a/b.go", 5, true}),
		child: profile([3]any{"internal/a/a.go", 10, true}, [3]any{"internal/a/b.go", 5, false}),
	} {
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out, errs bytes.Buffer
	if code := run([]string{"-merge", head, child}, &out, &errs); code != 0 {
		t.Fatalf("exit %d: %s", code, &errs)
	}
	want := "mode: atomic\n" + mod + "internal/a/a.go:1.1,1.20 10 1\n" + mod + "internal/a/b.go:2.1,2.20 5 1\n"
	if out.String() != want {
		t.Fatalf("want union of covered blocks:\n%s\ngot:\n%s", want, &out)
	}
	if err := os.WriteFile(child, []byte(profile([3]any{"internal/a/a.go", 11, true})), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"-merge", head, child}, &out, &errs); code == 0 || !strings.Contains(errs.String(), "inconsistent statement counts") {
		t.Fatalf("want conflicting block rejected, got %d: %s", code, &errs)
	}
}

func TestMergeRejectsMissingAndIncompatibleProfiles(t *testing.T) {
	dir := t.TempDir()
	head, child := filepath.Join(dir, "head.out"), filepath.Join(dir, "child.out")
	prof := profile([3]any{"internal/a/a.go", 10, true})
	if err := os.WriteFile(head, []byte(prof), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(child, []byte(strings.Replace(prof, "atomic", "count", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{head}, "at least two profiles"},
		{[]string{head, child}, "coverage modes differ"},
		{[]string{head, "missing"}, "missing"},
		{[]string{"missing", child}, "missing"},
	} {
		var out bytes.Buffer
		if code := run(append([]string{"-merge"}, tc.args...), &out, &out); code != 1 || !strings.Contains(out.String(), tc.want) {
			t.Fatalf("exit %d, want %q: %s", code, tc.want, &out)
		}
	}
}
