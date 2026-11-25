//go:build linux || darwin

package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTestKindsPartitionMixedPackages(t *testing.T) {
	goBin, err := exec.LookPath("go")
	require.NoError(t, err)
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte(`#!/bin/sh
case " $* " in
	*" test "*)
		case " $* " in
			*" ./internal/testhelpers "*) ;;
			*) echo 'selector ignored package scope'; exit 1 ;;
		esac ;;
esac
exec "$TEST_KIND_REAL_GO" "$@"
`), 0o700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TEST_KIND_REAL_GO", goBin)
	const unit = "TestGitEnvStripsLocations"
	const subprocess = "TestIsolateGitIgnoresHomeConfig"
	for _, tc := range []struct{ name, kind, filter, selected, excluded string }{
		{"unit", "unit", "", unit, subprocess},
		{"subprocess", "subprocess", "", subprocess, unit},
		{"unit with skip", "unit", "-skip=^" + unit + "$", "TestLostLeaf", unit},
		{"subprocess with run", "subprocess", "-run=^" + subprocess + "$", subprocess, "TestIsolateGitHomeUsesFixtureConfig"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), "make", "test-"+tc.kind,
				"TEST_KIND_ARGS=-v ./internal/testhelpers "+tc.filter)
			cmd.Dir = ".."
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			require.Contains(t, string(out), "\n=== RUN   "+tc.selected+"\n")
			require.NotContains(t, string(out), "\n=== RUN   "+tc.excluded+"\n")
		})
	}
}

func TestTestKindSelectorInQuotedCheckout(t *testing.T) {
	dir := filepath.Join(t.TempDir(), `checkout with 'single' and "double" quotes`)
	write := func(path string, data []byte) {
		t.Helper()
		name := filepath.Join(dir, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(name), 0o700))
		require.NoError(t, os.WriteFile(name, data, 0o600))
	}
	script, err := os.ReadFile("../scripts/test-kind.py")
	require.NoError(t, err)
	write("scripts/test-kind.py", script)
	write("tests/go-subprocess-tests.txt", []byte(". TestSubprocess\n"))
	write("go.mod", []byte("module example.test/selector\n\ngo 1.26\n"))
	write("fixture_test.go", []byte("package selector\nimport \"testing\"\nfunc TestUnit(t *testing.T) {}\nfunc TestSubprocess(t *testing.T) {}\n"))
	cmd := exec.CommandContext(t.Context(), "python3", "scripts/test-kind.py", "unit", "-count=1", "-v", ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	require.Contains(t, string(out), "=== RUN   TestUnit\n")
	require.NotContains(t, string(out), "\n=== RUN   TestSubprocess\n")
}
