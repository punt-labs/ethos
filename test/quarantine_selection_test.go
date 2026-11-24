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
		name, inventory, mode, goOutput, want string
		fail                                  bool
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			inventory := filepath.Join(dir, "quarantine.tsv")
			require.NoError(t, os.WriteFile(inventory, []byte(tc.inventory), 0o600))
			fakeGo := "#!/bin/sh\ncase \" $* \" in\n *' -list '*) printf '%s\\n' \"$TEST_QUARANTINE_LIST\";;\n *) printf 'go %s\\n' \"$*\";;\nesac\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "go"), []byte(fakeGo), 0o700))
			cmd := exec.CommandContext(t.Context(), "python3", "../scripts/test-quarantine.py", tc.mode, inventory)
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TEST_QUARANTINE_LIST="+tc.goOutput)
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
