//go:build linux || darwin

package test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestCIRaceShardsPartitionDefaultPackages(t *testing.T) {
	var workflow struct {
		Jobs map[string]struct {
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
