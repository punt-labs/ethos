//go:build linux || darwin

package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestCIRaceShardsPartitionDefaultPackages(t *testing.T) {
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				ID  string
				Env map[string]string
			}
			Strategy struct {
				Matrix struct {
					Shard []string
				}
			}
		}
	}
	data, err := os.ReadFile("../.github/workflows/test.yml")
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	shards := workflow.Jobs["test-suite"].Strategy.Matrix.Shard
	require.NotEmpty(t, shards)

	var expectedProfiles string
	for _, step := range workflow.Jobs["coverage"].Steps {
		if step.ID == "combine" {
			expectedProfiles = step.Env["EXPECTED_PROFILES"]
			break
		}
	}
	require.Equal(t, strconv.Itoa(len(shards)), expectedProfiles)

	cmd := exec.CommandContext(t.Context(), "go", "list", "-race", "./...")
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	all := strings.Fields(string(out))
	require.NotEmpty(t, all)

	var selected []string
	for _, shard := range shards {
		cmd := exec.CommandContext(t.Context(), "bash", "scripts/test-shard.sh", shard)
		cmd.Dir = ".."
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		packages := strings.Fields(string(out))
		require.NotEmpty(t, packages, shard)
		selected = append(selected, packages...)
	}
	require.ElementsMatch(t, all, selected)
}

func TestCIBaseCoverageUsesCompatibleTraceObserver(t *testing.T) {
	const observer = "plugin/hooks/archival_trace_assertions_test.go"
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct{ Name, Run string }
		}
	}
	data, err := os.ReadFile("../.github/workflows/test.yml")
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	var run string
	for _, step := range workflow.Jobs["test-suite"].Steps {
		if step.Name == "Test" {
			run = step.Run
		}
	}
	require.NotEmpty(t, run)
	for _, tc := range []struct{ snapshot, shard, blob, want string }{
		{"base", "git", "8e368c5948b336311cde1332de81e6243f5db0dd", "corrected"},
		{"base", "git", "other", "base"},
		{"base", "cli", "8e368c5948b336311cde1332de81e6243f5db0dd", "base"},
		{"head", "git", "8e368c5948b336311cde1332de81e6243f5db0dd", "head"},
		{"candidate", "git", "8e368c5948b336311cde1332de81e6243f5db0dd", "corrected"},
	} {
		t.Run(tc.snapshot+"/"+tc.shard+"/"+tc.blob, func(t *testing.T) {
			dir := t.TempDir()
			runner := t.TempDir()
			write := func(path, body string) {
				t.Helper()
				path = filepath.Join(dir, path)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, []byte(body), 0o700))
			}
			write(observer, "corrected\n")
			write("scripts/test-shard.sh", "echo ./plugin/hooks\n")
			write("git", `#!/bin/sh
case "$1" in
  merge-base) echo base ;;
  checkout) echo "$3" > plugin/hooks/production.go; echo "$3" > plugin/hooks/archival_trace_assertions_test.go ;;
  hash-object) echo "$FIXTURE_BLOB" ;;
  *) exit 1 ;;
esac
`)
			write("go", `#!/bin/sh
case "$1" in
  env) echo go1.26 ;;
  test) cat plugin/hooks/archival_trace_assertions_test.go
    if [ -f plugin/hooks/production.go ]; then cat plugin/hooks/production.go; else echo candidate; fi ;;
  *) exit 1 ;;
esac
`)
			cmd := exec.CommandContext(t.Context(), "bash", "-e", "-o", "pipefail", "-c", run)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "RUNNER_TEMP="+runner,
				"SNAPSHOT="+tc.snapshot, "SHARD="+tc.shard, "FIXTURE_BLOB="+tc.blob,
				"GITHUB_EVENT_NAME=pull_request", "PR_HEAD=head", "PR_BASE=main")
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			require.Equal(t, tc.want+"\n"+tc.snapshot+"\n", string(out))
		})
	}
}
