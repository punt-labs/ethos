package checkset

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRepositoryGoDefault(t *testing.T) {
	root := filepath.Join("..", "..", ".punt-labs")
	data, err := os.ReadFile(filepath.Join(root, "ethos", "checks", "go-default.yaml"))
	require.NoError(t, err)
	set, err := Decode(data)
	require.NoError(t, err)
	require.Equal(t, "go-default", set.Name)
	data, err = os.ReadFile(filepath.Join(root, "ethos.yaml"))
	require.NoError(t, err)
	var config struct {
		Checks string `yaml:"checks"`
	}
	require.NoError(t, yaml.Unmarshal(data, &config))
	require.Equal(t, set.Name, config.Checks)
	require.Len(t, set.Checks, 5)
	for i, want := range []struct {
		stage, pattern, mode string
		run                  Strings
		output               bool
	}{
		{"edit", "**/*.go", "advisory", Strings{"gofmt", "-l", "{file}"}, true},
		{"edit", "**/*.go", "advisory", Strings{"go", "vet", "{package_dir}"}, false},
		{"edit", "**/*.sh", "advisory", Strings{"shellcheck", "{file}"}, false},
		{"commit", "**/*.go", "block", Strings{"gofmt", "-l", "{files}"}, true},
		{"handoff", "", "block", Strings{"make", "check"}, false},
	} {
		check := set.Checks[i]
		require.Equal(t, want.stage, check.Stage)
		require.Equal(t, want.mode, check.OnFail)
		require.Equal(t, want.run, check.Run)
		require.Equal(t, want.output, check.FailOnOutput)
		require.Equal(t, "root", check.Cwd)
		if want.pattern == "" {
			require.True(t, check.Files.All)
			require.Equal(t, 30*time.Minute, check.Duration())
		} else {
			require.Equal(t, []string{want.pattern}, check.Files.Globs)
		}
		for _, excluded := range []string{".tmp/probe.go", ".tmp/probe.sh", ".punt-labs/local/probe.go", ".punt-labs/local/probe.sh"} {
			require.False(t, check.Matches(excluded), "%s: %s", check.Name, excluded)
		}
	}
}
