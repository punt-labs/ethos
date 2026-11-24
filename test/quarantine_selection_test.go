//go:build linux || darwin

package test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQuarantineSelection(t *testing.T) {
	today := time.Now().UTC()
	record := func(issue, owner, reason string, days int) string {
		return fmt.Sprintf("./internal/example\tTestQuarantined_Example\t%s\t%s\t%s\t%s\n",
			issue, owner, today.AddDate(0, 0, days).Format(time.DateOnly), reason)
	}
	for _, tc := range []struct {
		name, inventory, mode, goOutput, defaultOutput, want string
		fail                                                 bool
	}{
		{name: "empty", mode: "list", want: "No quarantined tests."},
		{name: "valid", inventory: record("PL-1", "owner", "race reproducer", 7), mode: "list", want: "PL-1"},
		{name: "expired", inventory: record("PL-1", "owner", "reason", -2), mode: "list", want: "within 14 days", fail: true},
		{name: "too long", inventory: record("PL-1", "owner", "reason", 16), mode: "list", want: "within 14 days", fail: true},
		{name: "unowned", inventory: record("PL-1", "", "reason", 7), mode: "list", want: "owner and reason", fail: true},
		{name: "untracked", inventory: record("TODO", "owner", "reason", 7), mode: "list", want: "Linear issue", fail: true},
		{name: "selected", inventory: record("PL-1", "owner", "reason", 7), mode: "run", goOutput: "TestQuarantined_Example",
			want: "go test -race -count=1 -tags quarantine -run ^TestQuarantined_Example$ -v ./internal/example"},
		{name: "absent", inventory: record("PL-1", "owner", "reason", 7), mode: "run", goOutput: "no tests to run",
			want: "did not select TestQuarantined_Example", fail: true},
		{name: "ordinary selection", inventory: record("PL-1", "owner", "reason", 7), mode: "list",
			defaultOutput: "TestQuarantined_Example", want: "also selected without -tags quarantine", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			inventory := filepath.Join(dir, "quarantine.tsv")
			require.NoError(t, os.WriteFile(inventory, []byte(tc.inventory), 0o600))
			fakeGo := `#!/bin/sh
case " $* " in
 *' list '*" ./... "*) ;;
 *' list '*) [ -z "$TEST_DEFAULT_LIST" ] || echo yes;;
 *' -tags quarantine -list '*) printf '%s\n' "$TEST_QUARANTINE_LIST";;
 *' -list '*) printf '%s\n' "$TEST_DEFAULT_LIST";;
 *) printf 'go %s\n' "$*";;
esac
`
			require.NoError(t, os.WriteFile(filepath.Join(dir, "go"), []byte(fakeGo), 0o700))
			cmd := exec.CommandContext(t.Context(), "python3", "../scripts/test-quarantine.py", tc.mode, inventory)
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TEST_QUARANTINE_LIST="+tc.goOutput, "TEST_DEFAULT_LIST="+tc.defaultOutput)
			out, err := cmd.CombinedOutput()
			if tc.fail {
				require.Error(t, err, "%s", out)
			} else {
				require.NoError(t, err, "%s", out)
			}
			require.Contains(t, string(out), tc.want)
			if tc.fail {
				require.NotContains(t, string(out), "go test -race")
			}
		})
	}
}

func TestQuarantineInventoryRejectsUnlistedTest(t *testing.T) {
	dir := t.TempDir()
	inventory := filepath.Join(dir, "quarantine.tsv")
	require.NoError(t, os.WriteFile(inventory, nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test/quarantine\n\ngo 1.26\n"), 0o600))
	pkg := filepath.Join(dir, "fixture")
	require.NoError(t, os.Mkdir(pkg, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(pkg, "quarantine_test.go"), []byte(`//go:build quarantine

package fixture

import "testing"

func TestQuarantined_Unlisted(t *testing.T) {
	t.Fatal("listing must not run the test")
}
`), 0o600))
	script, err := filepath.Abs("../scripts/test-quarantine.py")
	require.NoError(t, err)
	cmd := exec.CommandContext(t.Context(), "python3", script, "list", inventory)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "%s", out)
	require.Contains(t, string(out), "./fixture TestQuarantined_Unlisted is missing from the quarantine inventory")
}
