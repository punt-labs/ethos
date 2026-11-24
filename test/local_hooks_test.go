//go:build linux || darwin

package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDevelopmentPreCommit(t *testing.T) {
	for _, tc := range []struct {
		name, gate, want string
		fail             bool
	}{
		{name: "failed gate", gate: "false", want: "test-changed", fail: true},
		{name: "successful gate", gate: "true", want: "existing hook"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			home := t.TempDir()
			git := func(args ...string) ([]byte, error) {
				cmd := exec.CommandContext(t.Context(), "git", args...)
				cmd.Dir = dir
				cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1"}
				return cmd.CombinedOutput()
			}
			out, err := git("init", "-q", "--template=")
			require.NoError(t, err, "%s", out)
			out, err = git("config", "core.hooksPath", ".githooks")
			require.NoError(t, err, "%s", out)
			require.NoError(t, os.Mkdir(filepath.Join(dir, ".githooks"), 0o700))
			require.NoError(t, os.Mkdir(filepath.Join(dir, ".git", "hooks"), 0o700))
			hook, err := os.ReadFile("../.githooks/pre-commit")
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".githooks", "pre-commit"), hook, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "hooks", "pre-commit"), []byte("#!/bin/sh\necho existing hook\n"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "Makefile"), []byte("test-changed:\n\t"+tc.gate+"\n"), 0o600))

			out, err = git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.test",
				"commit", "--allow-empty", "-m", "fixture")
			if tc.fail {
				require.Error(t, err, "%s", out)
				require.NotContains(t, string(out), "existing hook")
			} else {
				require.NoError(t, err, "%s", out)
			}
			require.Contains(t, string(out), tc.want)
		})
	}
}

func TestDevelopmentPrePush(t *testing.T) {
	for _, tc := range []struct {
		name, gate, oid, want string
		fail                  bool
	}{
		{name: "failed gate", gate: "false", oid: "head", want: "test-push", fail: true},
		{name: "passes refs to existing hook", gate: "true", oid: "head", want: "refs/heads/topic"},
		{name: "delete bypasses gate but not existing hook", gate: "false", oid: "zero", want: "refs/heads/topic"},
		{name: "refuses untested commit", gate: "true", oid: "other", want: "check it out", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			home := t.TempDir()
			run := func(input, name string, args ...string) ([]byte, error) {
				cmd := exec.CommandContext(t.Context(), name, args...)
				cmd.Dir = dir
				cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1"}
				cmd.Stdin = strings.NewReader(input)
				return cmd.CombinedOutput()
			}
			out, err := run("", "git", "init", "-q", "--template=")
			require.NoError(t, err, "%s", out)
			out, err = run("", "git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test",
				"commit", "--allow-empty", "-m", "fixture")
			require.NoError(t, err, "%s", out)
			out, err = run("", "git", "rev-parse", "HEAD")
			require.NoError(t, err, "%s", out)
			head := strings.TrimSpace(string(out))
			require.NoError(t, os.Mkdir(filepath.Join(dir, ".git", "hooks"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "hooks", "pre-push"), []byte("#!/bin/sh\ncat\n"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "Makefile"), []byte("test-push:\n\t"+tc.gate+"\n"), 0o600))
			hook, err := filepath.Abs("../.githooks/pre-push")
			require.NoError(t, err)
			oid := head
			ref := "refs/heads/topic"
			switch tc.oid {
			case "zero":
				oid = strings.Repeat("0", len(head))
				ref = "(delete)"
			case "other":
				oid = strings.Repeat("1", len(head))
			}
			input := ref + " " + oid + " refs/heads/topic " + strings.Repeat("0", len(head)) + "\n"
			out, err = run(input, hook, "origin", "file:///fixture")
			if tc.fail {
				require.Error(t, err, "%s", out)
			} else {
				require.NoError(t, err, "%s", out)
				require.Contains(t, string(out), input)
			}
			require.Contains(t, string(out), tc.want)
		})
	}
}

func TestChangedPackages(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	bin := t.TempDir()
	index := filepath.Join(t.TempDir(), "index")
	env := []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + home, "GIT_CONFIG_NOSYSTEM=1", "GIT_INDEX_FILE=" + index}
	run := func(name string, args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), name, args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		return out
	}
	run("git", "init", "-q", "--template=")
	require.NoError(t, os.Mkdir(filepath.Join(dir, "changed"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "changed", "example.go"), []byte("package changed\n"), 0o600))
	run("git", "add", "changed/example.go")
	require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte(`#!/bin/sh
if [ "$1" = list ]; then
    printf '%s\n' "$(pwd -P)/changed" "$(pwd -P)/untouched"
else
    if [ "${GIT_INDEX_FILE+x}" = x ]; then
        echo "go test inherited the parent Git index"
        exit 1
    fi
    printf 'go %s\n' "$*"
fi
`), 0o700))
	script, err := filepath.Abs("../scripts/test-changed.sh")
	require.NoError(t, err)
	require.Equal(t, "go test -race -count=1 ./changed\n", string(run("bash", script)))
}
