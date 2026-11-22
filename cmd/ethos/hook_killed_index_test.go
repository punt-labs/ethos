//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestKilledPreCommitPrintedRecovery(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		committed, tracked, edited bool
	}{
		{name: "unborn"},
		{name: "committed", committed: true},
		{name: "already tracked", tracked: true},
		{name: "unborn staged then edited", edited: true},
		{name: "committed staged then edited", committed: true, edited: true},
		{name: "tracked staged then edited", tracked: true, edited: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			se := enableSubprocessRepo(t)
			if tc.committed {
				gitRunEnv(t, se.repo, se.env, "commit", "--allow-empty", "-qm", "initial")
			}
			local := ".punt-labs/local/ethos/stray ' [data].txt"
			path := filepath.Join(se.repo, local)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, []byte("private\n"), 0o600))
			gitRunEnv(t, se.repo, se.env, "add", "-f", "--", local)
			if tc.tracked {
				gitRunEnv(t, se.repo, se.env, "commit", "-qm", "initial private fixture")
			}
			want := "private\n"
			if tc.edited {
				if tc.tracked {
					require.NoError(t, os.WriteFile(path, []byte("staged\n"), 0o600))
					gitRunEnv(t, se.repo, se.env, "add", "-f", "--", local)
				}
				want = "edited private content\n"
				require.NoError(t, os.WriteFile(path, []byte(want), 0o600))
			}
			require.NoError(t, os.WriteFile(filepath.Join(se.home, ".punt-hooks-kill"), nil, 0o600))
			_, stderr, code := runCLI(t, se, "hook", "pre-commit")
			require.Equal(t, 2, code, stderr)
			start := strings.Index(stderr, "git ")
			require.NotEqual(t, -1, start, stderr)
			command, _, _ := strings.Cut(strings.TrimSpace(stderr[start:]), ". For")
			cmd := exec.Command("/bin/sh", "-c", command)
			cmd.Dir, cmd.Env = se.repo, se.env
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s: %s", command, output)
			cmd = exec.Command("git", "ls-files", "--cached", "--", local)
			cmd.Dir, cmd.Env = se.repo, se.env
			output, err = cmd.Output()
			require.NoError(t, err)
			require.Empty(t, output)
			out, stderr, code := runCLI(t, se, "hook", "pre-commit")
			require.Zero(t, code, stderr)
			require.Empty(t, out)
			require.Empty(t, stderr)
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, want, string(data))
		})
	}
}

func TestKilledPreCommitPrivacyGuard(t *testing.T) {
	se := enableSubprocessRepo(t)
	t.Chdir(se.repo)
	index := filepath.Join(se.repo, "candidate-index")
	t.Setenv("GIT_INDEX_FILE", index)
	se.env = append(se.env, "GIT_INDEX_FILE="+index)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var stderr bytes.Buffer
	require.NoError(t, killedPreCommit(ctx, &stderr))
	require.Empty(t, stderr.String())
	require.NoFileExists(t, index)
	paths := []string{".punt-labs/local/ethos/a.txt", ".punt-labs/local/ethos/quoted ' [data].txt"}
	for _, path := range paths {
		full := filepath.Join(se.repo, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o700))
		require.NoError(t, os.WriteFile(full, []byte("private\n"), 0o600))
		gitRunEnv(t, se.repo, se.env, "add", "-f", "--", path)
	}
	before, err := os.ReadFile(index)
	require.NoError(t, err)
	var refused failClosed
	require.ErrorAs(t, killedPreCommit(ctx, &stderr), &refused)
	for _, path := range paths {
		require.Contains(t, stderr.String(), fmt.Sprintf("%q", path))
		require.Contains(t, stderr.String(), "git rm --cached -f -r -- "+shellQuote(":(top,literal)"+path))
	}
	after, err := os.ReadFile(index)
	require.NoError(t, err)
	require.Equal(t, before, after)
	for _, mode := range []string{"cancelled", "empty selector", "corrupt index", "outside git", "missing cwd"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			switch mode {
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "empty selector":
				t.Setenv("GIT_INDEX_FILE", "")
			case "corrupt index":
				require.NoError(t, os.WriteFile(index, []byte("invalid index"), 0o600))
			case "outside git", "missing cwd":
				dir := se.home
				if mode == "missing cwd" {
					dir = filepath.Join(se.home, "cwd")
					require.NoError(t, os.Mkdir(dir, 0o700))
				}
				t.Chdir(dir)
				if mode == "missing cwd" {
					require.NoError(t, os.Remove(dir))
				}
			}
			var stderr bytes.Buffer
			require.ErrorAs(t, killedPreCommit(ctx, &stderr), &refused)
			require.Contains(t, stderr.String(), "cannot inspect committing index")
		})
	}
}

func TestKilledPreCommitIndexSafety(t *testing.T) {
	for _, mode := range []string{"clean", "linked", "absolute index", "relative index from subdir", "custom clean real private", "corrupt index", "empty index selector"} {
		t.Run(mode, func(t *testing.T) {
			se := enabledHookRepo(t)
			gitRunEnv(t, se.repo, se.env, "-c", "core.hooksPath="+t.TempDir(), "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "initial")
			gitRunEnv(t, se.repo, se.env, "read-tree", "--empty")
			root := se.repo
			if mode == "linked" {
				root = filepath.Join(t.TempDir(), "linked")
				gitRunEnv(t, se.repo, se.env, "worktree", "add", "--detach", root)
				se.repo = root
			}
			local := ".punt-labs/local/ethos/private ' [data].txt"
			path := filepath.Join(root, local)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, []byte("private fixture\n"), 0o600))
			index := filepath.Join(root, ".git/index")
			if mode == "linked" {
				cmd := exec.Command("git", "-C", root, "rev-parse", "--path-format=absolute", "--git-path", "index")
				cmd.Env = se.env
				data, err := cmd.Output()
				require.NoError(t, err)
				index = strings.TrimSuffix(string(data), "\n")
			}
			realIndex := index
			if mode == "custom clean real private" {
				gitRunEnv(t, root, se.env, "add", "-f", "--", local)
			}
			if mode == "absolute index" || mode == "relative index from subdir" || mode == "custom clean real private" || mode == "corrupt index" {
				index = filepath.Join(root, "candidate-index")
				selector := index
				if mode == "relative index from subdir" {
					selector = "candidate-index"
				}
				se.env = append(se.env, "GIT_INDEX_FILE="+selector)
				gitRunEnv(t, root, se.env, "read-tree", "HEAD")
			}
			want := 2
			if mode == "clean" || mode == "custom clean real private" {
				want = 0
			} else if mode == "corrupt index" {
				require.NoError(t, os.WriteFile(index, []byte("invalid index"), 0o600))
			} else if mode == "empty index selector" {
				se.env = append(se.env, "GIT_INDEX_FILE=")
			} else {
				gitRunEnv(t, root, se.env, "add", "-f", "--", local)
			}
			if mode == "relative index from subdir" {
				se.repo = filepath.Join(root, "subdir")
				require.NoError(t, os.MkdirAll(se.repo, 0o700))
			}
			before, err := os.ReadFile(index)
			require.NoError(t, err)
			realBefore, err := os.ReadFile(realIndex)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(se.home, ".punt-hooks-kill"), nil, 0o600))
			out, stderr, code := runCLI(t, se, "hook", "pre-commit")
			require.Equal(t, want, code, stderr)
			require.Empty(t, out)
			if want == 0 {
				require.Empty(t, stderr)
			} else if mode == "corrupt index" || mode == "empty index selector" {
				require.Contains(t, stderr, "index")
			} else {
				require.Contains(t, stderr, local)
				require.Contains(t, stderr, "git rm --cached -f -r -- "+shellQuote(":(top,literal)"+local))
			}
			after, err := os.ReadFile(index)
			require.NoError(t, err)
			require.Equal(t, before, after)
			realAfter, err := os.ReadFile(realIndex)
			require.NoError(t, err)
			require.Equal(t, realBefore, realAfter)
		})
	}
}
