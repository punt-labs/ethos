package testhelpers

import (
	"os"
	"slices"
	"testing"
)

func TestProcessCoverageIsOptIn(t *testing.T) {
	t.Setenv(ProcessCoverDir, "")
	t.Setenv("GOCOVERDIR", "package-tests")
	EnableInheritedCoverage()
	if os.Getenv("GOCOVERDIR") != "package-tests" || CoverEnv(nil) != nil ||
		!slices.Equal(CoverBuild("-o", "bin", "."), []string{"build", "-o", "bin", "."}) {
		t.Fatal("process coverage changed the default package test environment")
	}

	dir := t.TempDir()
	t.Setenv(ProcessCoverDir, dir)
	env := CoverEnv([]string{"HOME=/isolated"})
	if !slices.Equal(env, []string{"HOME=/isolated", "GOCOVERDIR=" + dir}) ||
		!slices.Contains(CoverEnv(nil), "GOCOVERDIR="+dir) ||
		!slices.Equal(CoverBuild("-o", "bin", "."), []string{"build", "-cover", "-covermode=atomic", "-o", "bin", "."}) {
		t.Fatal("missing opt-in process coverage flags or environment")
	}
	EnableInheritedCoverage()
	if os.Getenv("GOCOVERDIR") != dir {
		t.Fatal("missing process coverage directory for children that inherit their environment")
	}
}
