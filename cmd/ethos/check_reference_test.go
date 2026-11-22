//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInvalidCheckReferenceDoesNotBreakIdentity(t *testing.T) {
	for _, command := range []string{"whoami", "doctor"} {
		t.Run(command, func(t *testing.T) {
			se := setupCLISubprocessEnv(t)
			require.NoError(t, os.WriteFile(filepath.Join(se.repo, ".punt-labs/ethos/identities/repo-caller.yaml"), []byte("name: Repo Caller\nhandle: repo-caller\nkind: human\n"), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(se.repo, ".punt-labs/ethos.yaml"), []byte("agent: repo-caller\nchecks: Go_Default\n"), 0600))
			se.env = append(se.env, "USER=repo-caller")
			out, stderr, code := runCLI(t, se, command, "--json")
			if command == "whoami" {
				require.Zero(t, code, stderr)
				require.Contains(t, out, `"handle": "repo-caller"`)
			} else {
				require.Equal(t, 1, code, stderr)
				require.Contains(t, out, `"name": "Check-set reference"`)
				require.Contains(t, out, "Go_Default")
				require.NotContains(t, out, "repository configuration could not be read")
			}
		})
	}
}
