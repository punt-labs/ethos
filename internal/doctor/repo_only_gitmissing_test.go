package doctor

import (
	"strings"
	"testing"
)

// TestTrackedLocalExtNamesAMissingGit pins that a missing git reads as
// "git not found on PATH", not Go's raw exec error.
func TestTrackedLocalExtNamesAMissingGit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := trackedLocalExt(t.TempDir())
	if err == nil {
		t.Fatal("want an error with no git on PATH")
	}
	if !strings.Contains(err.Error(), "git not found on PATH") || strings.Contains(err.Error(), "executable file not found") {
		t.Errorf("got %q", err)
	}
}
