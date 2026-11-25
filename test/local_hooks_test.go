//go:build linux || darwin

package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDevelopmentPreCommit(t *testing.T) {
	for _, tc := range []struct {
		name, gate, want string
		fail             bool
		sealed           bool
		rejectSecret     bool
	}{
		{name: "failed secret scan", gate: "@echo ran-staged-tests", want: "secret scan ran", fail: true, rejectSecret: true},
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
			scan := "true"
			if tc.rejectSecret {
				scan = "false"
			}
			scanRecipe := "\nsecret-scan-staged:\n\t@echo 'secret scan ran'\n\t" + scan + "\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "Makefile"), []byte(
				"test-changed:\n\t"+tc.gate+scanRecipe), 0o600))
			out, err := git("add", "Makefile")
			require.NoError(t, err, "%s", out)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "Makefile"), []byte("test-changed:\n\t@echo ran-dirty-makefile"+scanRecipe), 0o600))
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
			require.Contains(t, string(out), "secret scan ran")
			require.Regexp(t, `(?s)secret scan ran.*\npre-commit: \d+s elapsed \((within|over) 30s budget\), exit \d+`, string(out))
			if tc.rejectSecret {
				require.NotContains(t, string(out), "test-changed")
				require.NotContains(t, string(out), "ran-staged-tests")
			}
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
		fail, legacy          bool
	}{
		{name: "failed gate", gate: "false", oid: "head", want: "test-push", fail: true},
		{name: "passes refs to existing hook", gate: "true", oid: "head", want: "refs/heads/topic"},
		{name: "commit predates snapshot helper", gate: "@echo ran-legacy-tests", oid: "head", want: "ran-legacy-tests", legacy: true},
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
			writePushedHookScripts(t, dir)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "Makefile"), []byte("test-push:\n\t"+tc.gate+"\n"), 0o600))
			paths := []string{"Makefile", "scripts/install-dev-hooks.py", "scripts/test-timed.sh"}
			if !tc.legacy {
				paths = append(paths, "scripts/test-snapshot.sh")
			}
			out, err = run("", "git", append([]string{"add"}, paths...)...)
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
			for _, path := range []string{"go.mod", "docs", ".githooks"} {
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

func TestPushRunsComplementaryPartitionsAndRemainingPackages(t *testing.T) {
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte(`#!/bin/sh
if [ "$1" = list ]; then
    module=github.com/punt-labs/ethos/v4
    printf '%s\n' "$module/internal/mission $module" "$module/plugin/hooks $module" "$module/cmd/ethos $module" "$module/internal/hook $module" "$module/internal/resolve $module"
elif [ "$1" = test ]; then
    printf '%s\n' "$*"
fi
`), 0o700))
	cmd := exec.CommandContext(t.Context(), "make", "test-push", "TEST_PUSH_PARALLEL=4")
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	var calls []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "test ") {
			calls = append(calls, line)
		}
	}
	require.ElementsMatch(t, []string{
		"test -race -count=1 -timeout 30m -p 1 -run ^Test(CLI|Mission) ./cmd/ethos",
		"test -race -count=1 -timeout 30m -p 1 -skip ^Test(CLI|Mission) ./cmd/ethos",
		"test -race -count=1 -timeout 30m -p 1 -run ^TestGeneratedPostEditShell ./internal/hook",
		"test -race -count=1 -timeout 30m -p 1 -skip ^TestGeneratedPostEditShell ./internal/hook",
		"test -race -count=1 -timeout 30m -p 4 github.com/punt-labs/ethos/v4/internal/mission github.com/punt-labs/ethos/v4/plugin/hooks github.com/punt-labs/ethos/v4/internal/resolve",
		"test -race -count=1 -run ^TestHooks(DormantRepositoryRunsNoProbe|FailLoudWhenRevParseFails)$ ./plugin/hooks",
	}, calls)
}

func TestPushSignalsWorkersOnInterrupt(t *testing.T) {
	bin := t.TempDir()
	worker := filepath.Join(bin, "worker")
	stopped := filepath.Join(bin, "stopped")
	require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte(`#!/bin/sh
if [ "$1" = list ]; then
    printf '%s\n' 'example.org/rest example.org'
elif [ "$8" = -run ] && [ "${10}" = ./cmd/ethos ]; then
    trap 'touch "$STOPPED"; exit' TERM
    touch "$WORKER"
    sleep 5
fi
`), 0o700))
	cmd := exec.CommandContext(t.Context(), "bash", "scripts/test-push.sh", "1")
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "WORKER="+worker, "STOPPED="+stopped)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	require.Eventually(t, func() bool { _, err := os.Stat(worker); return err == nil }, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, cmd.Process.Signal(syscall.SIGINT))
	var exit *exec.ExitError
	require.ErrorAs(t, cmd.Wait(), &exit)
	require.Equal(t, 130, exit.ExitCode())
	require.FileExists(t, stopped)
}

func TestChangedPackages(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	bin := t.TempDir()
	index := filepath.Join(t.TempDir(), "index")
	env := []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + home, "GIT_CONFIG_NOSYSTEM=1", "GIT_INDEX_FILE=" + index}
	invoke := func(name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(t.Context(), name, args...)
		cmd.Dir = dir
		cmd.Env = env
		return cmd.CombinedOutput()
	}
	run := func(name string, args ...string) []byte {
		t.Helper()
		out, err := invoke(name, args...)
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
    if [ -e cmd/ethos/main.go ]; then
        printf '%s\n' "fixture/cmd/ethos|$root/cmd/ethos|"
    fi
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
    case "$*" in
        *NoSuchTest*) printf 'testing: warning: no tests to run\nPASS\n' ;;
        *-run*) printf '=== RUN   TestSlugify\n--- PASS: TestSlugify (0.00s)\nPASS\n' ;;
    esac
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
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "cmd/ethos"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cmd/ethos/main.go"), []byte("package main\n"), 0o600))
	run("git", "add", "cmd/ethos/main.go", "changed/example.go")
	got := selectPackages()
	require.Contains(t, got, "go test -race -count=1 -v -run ^Test(Slugify|VersionCommand|VersionCommandJSON)$ ./cmd/ethos\n")
	require.Contains(t, got, "go test -race -count=1 ./changed ./direct ./transitive\n")

	smokeScript := filepath.Join(t.TempDir(), "test-changed.sh")
	source, err := os.ReadFile(script)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(smokeScript, source, 0o600))
	smokeFile := filepath.Join(filepath.Dir(smokeScript), "test-smoke.tsv")
	t.Run("CRLF smoke row", func(t *testing.T) {
		require.NoError(t, os.WriteFile(smokeFile, []byte("./cmd/ethos\tSlugify|VersionCommand\r\n"), 0o600))
		require.Contains(t, string(run("bash", snapshot, "staged", "bash", smokeScript)), "-run ^Test(Slugify|VersionCommand)$ ./cmd/ethos\n")
	})
	t.Run("final smoke row without a newline", func(t *testing.T) {
		require.NoError(t, os.WriteFile(smokeFile, []byte("./internal/hook\tBuildPersonaBlock_Full\n./cmd/ethos\tSlugify"), 0o600))
		require.Contains(t, string(run("bash", snapshot, "staged", "bash", smokeScript)), "-run ^Test(Slugify)$ ./cmd/ethos\n")
	})
	t.Run("empty smoke run", func(t *testing.T) {
		require.NoError(t, os.WriteFile(smokeFile, []byte("./cmd/ethos\tNoSuchTest\n"), 0o600))
		out, err := invoke("bash", snapshot, "staged", "bash", smokeScript)
		require.Error(t, err, "%s", out)
		require.Contains(t, string(out), "No smoke tests matched for ./cmd/ethos")
	})

	run("git", "rm", "--cached", "cmd/ethos/main.go", "changed/example.go")
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

func TestChangedPackagesDirectDoesNotScanSecrets(t *testing.T) {
	fixture, run := developmentHookFixture(t)
	dir := filepath.Join(fixture, "repo")
	require.NoError(t, os.Mkdir(dir, 0o700))
	for _, subdir := range []string{"scripts", ".githooks", "docs"} {
		require.NoError(t, os.Mkdir(filepath.Join(dir, subdir), 0o700))
	}
	for _, path := range []string{"scripts/test-snapshot.sh", "scripts/test-changed.sh", "scripts/test-smoke.tsv", "scripts/test-timed.sh", ".githooks/pre-commit", "Makefile"} {
		data, err := os.ReadFile(filepath.Join("..", path))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, path), data, 0o700))
	}
	const module = "module example.test/changed\n\ngo 1.26\n"
	toolchain, _, _ := strings.Cut(strings.Fields(runtime.Version())[0], "-X:")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module+"\ntoolchain "+toolchain+"\n"), 0o600))
	for _, args := range [][]string{{"init", "-q", "--template="}, {"add", "."}, {"commit", "-qm", "fixture"}} {
		out, err := run(dir, "git", args...)
		require.NoError(t, err, "%s", out)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "changed.go"), []byte("package changed\n"), 0o600))
	out, err := run(dir, "git", "add", "changed.go")
	require.NoError(t, err, "%s", out)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module+"\ntoolchain go1.0.1\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "changed.go"), []byte("not staged\n"), 0o600))

	out, err = run(dir, "make", "secret-scan-staged", "GITLEAKS=false")
	require.Error(t, err, "%s", out)
	out, err = run(dir, "make", "test-changed", "GITLEAKS=false", "GOPROXY=off")
	require.NoError(t, err, "%s", out)
	require.Contains(t, string(out), "example.test/changed")
	require.NotContains(t, string(out), "secret-scan-staged")
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
