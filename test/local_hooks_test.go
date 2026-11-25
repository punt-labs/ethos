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
		sealed           bool
	}{
		{name: "failed gate", gate: "false", want: "test-changed", fail: true},
		{name: "successful gate", gate: "true", want: "existing hook"},
		{name: "installed seal keeps existing checks", gate: "true", want: "existing hook", sealed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, run := developmentHookFixture(t)
			git := func(args ...string) ([]byte, error) {
				return run(dir, "git", args...)
			}
			require.NoError(t, os.Mkdir(filepath.Join(dir, ".git", "hooks"), 0o700))
			hook := "#!/bin/sh\necho existing hook\n"
			if tc.sealed {
				hook += "\n# --- BEGIN ETHOS DES-058 SEAL ---\necho development seal\n# --- END ETHOS DES-058 SEAL ---\n"
			}
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "hooks", "pre-commit"), []byte(hook), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "hooks", "commit-msg"),
				[]byte("#!/bin/sh\nprintf '\\nHost trailer: preserved\\n' >> \"$1\"\n"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "Makefile"), []byte("test-changed:\n\t"+tc.gate+"\n"), 0o600))
			out, err := git("add", "Makefile")
			require.NoError(t, err, "%s", out)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "Makefile"), []byte("test-changed:\n\t@echo ran-dirty-makefile\n"), 0o600))
			installDevelopmentHooks(t, dir, run)

			out, err = git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.test",
				"commit", "--allow-empty", "-m", "fixture")
			if tc.fail {
				require.Error(t, err, "%s", out)
				require.NotContains(t, string(out), "existing hook")
			} else {
				require.NoError(t, err, "%s", out)
				msg, err := git("log", "-1", "--format=%B")
				require.NoError(t, err, "%s", msg)
				require.Contains(t, string(msg), "Host trailer: preserved")
			}
			require.Contains(t, string(out), tc.want)
			require.NotContains(t, string(out), "ran-dirty-makefile")
			if tc.sealed {
				require.Contains(t, string(out), "development seal")
			}
		})
	}
}

func TestDevelopmentPrePush(t *testing.T) {
	const tagNotice = "Version tags are gated by the release workflow's candidate smoke."
	for _, tc := range []struct {
		name, gate, oid, want string
		fail                  bool
	}{
		{name: "failed gate", gate: "false", oid: "head", want: "test-push", fail: true},
		{name: "passes refs to existing hook", gate: "true", oid: "head", want: "refs/heads/topic"},
		{name: "delete bypasses gate but not existing hook", gate: "false", oid: "zero", want: "refs/heads/topic"},
		{name: "refuses untested commit", gate: "true", oid: "other", want: "check it out", fail: true},
		{name: "release tags bypass the gate", gate: "false", oid: "release", want: tagNotice},
		{name: "non-version tag is gated", gate: "false", oid: "qa-baseline", want: "test-push", fail: true},
		{name: "namespaced v-prefix tag is gated", gate: "false", oid: "validation/checkpoint", want: "test-push", fail: true},
		{name: "branch still tested alongside release tag", gate: "false", oid: "mixed", want: "test-push", fail: true},
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
			require.NoError(t, os.WriteFile(filepath.Join(dir, "Makefile"), []byte("test-push:\n\t"+tc.gate+"\n"), 0o600))
			out, err = run("", "git", "add", "Makefile")
			require.NoError(t, err, "%s", out)
			out, err = run("", "git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test",
				"commit", "--allow-empty", "-m", "fixture")
			require.NoError(t, err, "%s", out)
			out, err = run("", "git", "rev-parse", "HEAD")
			require.NoError(t, err, "%s", out)
			head := strings.TrimSpace(string(out))
			require.NoError(t, os.Mkdir(filepath.Join(dir, ".git", "hooks"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "hooks", "pre-push"), []byte("#!/bin/sh\ncat\n"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "Makefile"), []byte("test-push:\n\t@echo ran-dirty-makefile\n"), 0o600))
			root, err := filepath.Abs("..")
			require.NoError(t, err)
			for _, path := range []string{"scripts", "go.mod", "docs", ".githooks"} {
				require.NoError(t, os.Symlink(filepath.Join(root, path), filepath.Join(dir, path)))
			}
			oid := head
			ref := "refs/heads/topic"
			remoteRef := ref
			switch tc.oid {
			case "zero":
				oid = strings.Repeat("0", len(head))
				ref = "(delete)"
			case "other":
				oid = strings.Repeat("1", len(head))
			case "qa-baseline", "validation/checkpoint":
				out, err = run("", "git", "tag", tc.oid, head)
				require.NoError(t, err, "%s", out)
				ref = "refs/tags/" + tc.oid
				remoteRef = ref
			}
			input := ref + " " + oid + " " + remoteRef + " " + strings.Repeat("0", len(head)) + "\n"
			if tc.oid == "release" || tc.oid == "mixed" {
				for _, tag := range []string{"v99.99.99", "v99.99.98"} {
					out, err = run("", "git", "tag", tag, head)
					require.NoError(t, err, "%s", out)
				}
				out, err = run("", "git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test",
					"commit", "--allow-empty", "-m", "pin README")
				require.NoError(t, err, "%s", out)
				tagUpdate := func(tag string) string {
					ref := "refs/tags/" + tag
					return ref + " " + head + " " + ref + " " + strings.Repeat("0", len(head)) + "\n"
				}
				input = tagUpdate("v99.99.99") + tagUpdate("v99.99.98")
				if tc.oid == "mixed" {
					out, err = run("", "git", "rev-parse", "HEAD")
					require.NoError(t, err, "%s", out)
					input += ref + " " + strings.TrimSpace(string(out)) + " " + ref + " " + strings.Repeat("0", len(head)) + "\n"
				}
			}
			installDevelopmentHooks(t, dir, func(_ string, name string, args ...string) ([]byte, error) {
				return run("", name, args...)
			})
			out, err = run(input, filepath.Join(dir, ".git", "hooks", "pre-push"), "origin", "file:///fixture")
			if tc.fail {
				require.Error(t, err, "%s", out)
			} else {
				require.NoError(t, err, "%s", out)
				require.Contains(t, string(out), input)
			}
			require.Contains(t, string(out), tc.want)
			require.NotContains(t, string(out), "ran-dirty-makefile")
			if tc.oid == "release" || tc.oid == "mixed" {
				require.Equal(t, 1, strings.Count(string(out), tagNotice))
			} else {
				require.NotContains(t, string(out), tagNotice)
			}
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
	require.NoError(t, os.WriteFile(filepath.Join(dir, "changed", "example.go"), []byte("unstaged\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "untracked"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte(`#!/bin/sh
if [ "$1" = list ]; then
    root=$(pwd -P)
    printf '%s\n' "fixture/changed|$root/changed|" "fixture/untouched|$root/untouched|" \
        "fixture/internal/seed|$root/internal/seed|" "fixture/test|$root/test|" \
        "fixture/direct|$root/direct|fixture/changed" "fixture/transitive|$root/transitive|fixture/direct"
else
    if [ "${GIT_INDEX_FILE+x}" = x ]; then
        echo "go test inherited the parent Git index"
        exit 1
    fi
    [ ! -e untracked ] || exit 2
    if [ -e changed/example.go ]; then
        [ "$(cat changed/example.go)" = "package changed" ] || exit 3
    fi
    printf 'go %s\n' "$*"
fi
`), 0o700))
	script, err := filepath.Abs("../scripts/test-changed.sh")
	require.NoError(t, err)
	snapshot, err := filepath.Abs("../scripts/test-snapshot.sh")
	require.NoError(t, err)
	selectPackages := func() string {
		return string(run("bash", snapshot, "staged", "bash", script))
	}
	require.Equal(t, "go test -race -count=1 ./changed ./direct ./transitive\n", selectPackages())
	require.Equal(t, "unstaged\n", string(run("cat", "changed/example.go")))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "changed", "example.go"), []byte("package changed\n"), 0o600))
	run("git", "rm", "--cached", "changed/example.go")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "changed", "testdata", "nested"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "changed", "testdata", "nested", "sample.json"), []byte("{}\n"), 0o600))
	run("git", "add", "changed/testdata/nested/sample.json")
	require.Equal(t, "go test -race -count=1 ./changed ./direct ./transitive\n", selectPackages())

	run("git", "rm", "--cached", "changed/testdata/nested/sample.json")
	template := "internal/seed/sidecar/agents/example.md"
	require.NoError(t, os.MkdirAll(filepath.Join(dir, filepath.Dir(template)), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, template), []byte("# Example\n"), 0o600))
	run("git", "add", template)
	require.Equal(t, "go test -race -count=1 ./internal/seed\n", selectPackages())

	run("git", "rm", "--cached", template)
	helper := "scripts/legacy-seal.go"
	require.NoError(t, os.MkdirAll(filepath.Join(dir, filepath.Dir(helper)), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, helper), []byte("package main\n"), 0o600))
	run("git", "add", helper)
	require.Equal(t, "go test -race -count=1 ./test\n", selectPackages())

	run("git", "rm", "--cached", helper)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test/fixture\n"), 0o600))
	run("git", "add", "go.mod")
	require.Equal(t, "go test -race -count=1 ./changed ./untouched ./internal/seed ./test ./direct ./transitive\n", selectPackages())
	require.NotContains(t, string(run("git", "worktree", "list", "--porcelain")), "checkout\n")
}

func TestChangedPackagesFindsTestsImportingMovedPackage(t *testing.T) {
	fixture, run := developmentHookFixture(t)
	dir := filepath.Join(fixture, "repo")
	require.NoError(t, os.Mkdir(dir, 0o700))
	git := func(args ...string) {
		t.Helper()
		out, err := run(dir, "git", args...)
		require.NoError(t, err, "%s", out)
	}
	write := func(path, data string) {
		t.Helper()
		name := filepath.Join(dir, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(name), 0o700))
		require.NoError(t, os.WriteFile(name, []byte(data), 0o600))
	}
	git("init", "-q", "--template=")
	write("go.mod", "module example.test/move\n\ngo 1.26\n")
	write("old/pkg.go", "package moved\n")
	write("consumer/pkg.go", "package consumer\n")
	write("consumer/pkg_test.go", "package consumer_test\nimport _ \"example.test/move/old\"\n")
	git("add", ".")
	git("commit", "-qm", "initial packages")
	git("mv", "old", "new")

	snapshot, err := filepath.Abs("../scripts/test-snapshot.sh")
	require.NoError(t, err)
	selector, err := filepath.Abs("../scripts/test-changed.sh")
	require.NoError(t, err)
	out, err := run(dir, "bash", snapshot, "staged", "bash", selector)
	require.Error(t, err, "%s", out)
	require.Contains(t, string(out), "example.test/move/consumer")
	require.Contains(t, string(out), "no required module provides package example.test/move/old")
}

func TestDevelopmentLiveRequiresKey(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "env", "-u", "ANTHROPIC_API_KEY", "make", "test-live")
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "%s", out)
	require.Contains(t, string(out), "ANTHROPIC_API_KEY is required")
}

func TestDevelopmentHooksRefuseNonShellHook(t *testing.T) {
	dir, run := developmentHookFixture(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o700))
	hook := filepath.Join(dir, ".git", "hooks", "pre-push")
	source := []byte("#!/usr/bin/env python3\nprint('existing hook')\n")
	require.NoError(t, os.WriteFile(hook, source, 0o700))
	makefile, err := filepath.Abs("../Makefile")
	require.NoError(t, err)
	out, err := run(dir, "make", "-f", makefile, "hooks")
	require.Error(t, err, "%s", out)
	require.Contains(t, string(out), "pre-push: expected a shell hook")
	got, err := os.ReadFile(hook)
	require.NoError(t, err)
	require.Equal(t, source, got)
	require.NoFileExists(t, filepath.Join(dir, ".git", "hooks", "pre-commit"))
	out, err = run(dir, "git", "config", "--get", "core.hooksPath")
	require.Error(t, err, "%s", out)
}
