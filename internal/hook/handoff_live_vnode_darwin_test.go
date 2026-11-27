package hook

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestDarwinLiveVnodeGuard(t *testing.T) {
	for _, name := range []string{"clean", "write", "revert", "chmod", "HEAD", "index", "ref", "transient source", "nested transient source"} {
		t.Run(name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			for _, path := range []string{"sub/source", ".git/HEAD", ".git/index", ".git/refs/heads/main"} {
				writeFile(t, filepath.Join(root, path), "original")
			}
			git := filepath.Join(root, ".git")
			guard, err := armDarwinLiveVnodes(context.Background(), root, darwinLiveGit{local: git, common: git}, []string{"sub/source"}, 20)
			require.NoError(t, err)
			defer guard.close()
			source := filepath.Join(root, "sub/source")
			switch name {
			case "clean":
				_, err := os.ReadFile(source)
				require.NoError(t, err)
			case "write", "revert":
				require.NoError(t, os.WriteFile(source, []byte("changed"), 0o600))
				if name == "revert" {
					require.NoError(t, os.WriteFile(source, []byte("original"), 0o600))
				}
			case "chmod":
				require.NoError(t, os.Chmod(source, 0o400))
			case "HEAD", "index", "ref":
				target := name
				if name == "ref" {
					target = "refs/heads/main"
				}
				tmp := filepath.Join(root, ".git", "replacement")
				require.NoError(t, os.WriteFile(tmp, []byte("original"), 0o600))
				require.NoError(t, os.Rename(tmp, filepath.Join(root, ".git", target)))
			default:
				path := filepath.Join(root, "transient")
				if name == "nested transient source" {
					path = filepath.Join(root, "sub", "transient")
				}
				require.NoError(t, os.WriteFile(path, nil, 0o600))
				require.NoError(t, os.Remove(path))
			}
			if name == "clean" {
				require.NoError(t, guard.drain())
			} else {
				veto := guard.drain()
				require.ErrorContains(t, veto, "vnode note")
				switch name {
				case "write", "revert", "chmod":
					require.ErrorContains(t, veto, source)
				case "transient source":
					require.ErrorContains(t, veto, root+`"`)
				case "nested transient source":
					require.ErrorContains(t, veto, filepath.Dir(source)+`"`)
				}
			}
		})
	}
}

func TestDarwinLiveVnodeFDBudget(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	writeFile(t, filepath.Join(root, ".git/HEAD"), "ref")
	writeFile(t, filepath.Join(root, "nested/source"), "data")
	git := filepath.Join(root, ".git")
	_, err = armDarwinLiveVnodes(context.Background(), root, darwinLiveGit{local: git, common: git}, []string{"nested/source"}, 4)
	require.ErrorContains(t, err, "fd budget")
	require.ErrorContains(t, err, git)
}

func TestDarwinLiveVnodeAtimeException(t *testing.T) {
	var before unix.Stat_t
	after := before
	after.Atim.Sec++
	require.True(t, darwinAtimeOnly(before, after))
	after.Ctim.Sec++
	require.False(t, darwinAtimeOnly(before, after))
	require.False(t, darwinAtimeOnly(before, before))
}

func TestDarwinLiveVnodeIgnoresPrivateSentinelWrite(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	writeFile(t, filepath.Join(root, ".git/HEAD"), "ref")
	writeFile(t, filepath.Join(root, "nested/source"), "data")
	sentinel := filepath.Join(root, ".git", "ethos-live", "proof")
	require.NoError(t, os.MkdirAll(sentinel, 0o700))
	git := filepath.Join(root, ".git")
	guard, err := armDarwinLiveVnodes(t.Context(), root, darwinLiveGit{local: git, common: git}, []string{"nested/source"}, 20)
	require.NoError(t, err)
	defer guard.close()
	require.NoError(t, os.WriteFile(filepath.Join(sentinel, "marker"), []byte("cutoff"), 0o600))
	require.NoError(t, guard.drain())
}
