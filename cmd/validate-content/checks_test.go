package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckReferences(t *testing.T) {
	for _, entity := range []string{"identity", "role", "repo"} {
		t.Run(entity, func(t *testing.T) {
			repo, ethos := minimalRepo(t, "")
			file, data := "", ""
			switch entity {
			case "identity":
				file, data = filepath.Join(ethos, "identities/alice.yaml"), "name: Alice\nhandle: alice\nkind: human\nchecks: missing\n"
			case "role":
				file, data = filepath.Join(ethos, "roles/developer.yaml"), "name: developer\nchecks: missing\n"
			case "repo":
				file, data = filepath.Join(repo, ".punt-labs/ethos.yaml"), "checks: missing\n"
			}
			require.NoError(t, os.WriteFile(file, []byte(data), 0o600))
			report, err := run(ethos, t.TempDir())
			require.NoError(t, err)
			var failures []string
			for _, result := range report.results {
				if !result.pass {
					failures = append(failures, result.detail)
				}
			}
			require.Contains(t, strings.Join(failures, "\n"), `check set "missing" not found`)
			require.NoError(t, os.WriteFile(file, []byte(strings.ReplaceAll(data, "checks: missing", "checks: none")), 0o600))
			report, err = run(ethos, t.TempDir())
			require.NoError(t, err)
			for _, result := range report.results {
				require.True(t, result.pass, "%s: %s", result.label, result.detail)
			}
		})
	}
}
