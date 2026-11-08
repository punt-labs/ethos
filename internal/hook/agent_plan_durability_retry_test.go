package hook

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestAgentEqualContentRetriesDirectoryDurability(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no directory-fsync implementation or POSIX directory permissions")
	}
	for _, entry := range []string{"compatibility writer", "captured plan"} {
		for _, parent := range []string{".claude/agents", ".claude", "."} {
			t.Run(entry+"/"+parent, func(t *testing.T) {
				store, checkout := physicalAgentTempDir(t), physicalAgentTempDir(t)
				p, err := layout.NewRepository(store, checkout, layout.RecordBlame)
				require.NoError(t, err)
				path := filepath.Join(checkout, ".claude", "agents", "bwk.md")
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				blocked := filepath.Join(checkout, filepath.FromSlash(parent))
				// Search/write still work, so installation and equal-byte reads
				// succeed. Opening this directory to fsync it must fail. This is
				// a real persistent fault, not a mocked successful installation.
				require.NoError(t, os.Chmod(blocked, 0o311))
				t.Cleanup(func() { require.NoError(t, os.Chmod(blocked, 0o755)) })
				f, err := os.Open(blocked)
				if err == nil {
					require.NoError(t, f.Close())
					t.Skip("directory read permissions are not enforced (for example, privileged execution)")
				}
				require.ErrorIs(t, err, os.ErrPermission)
				const content = "complete generated agent\n"
				plan := AgentPlan{Files: []AgentFile{{Handle: "bwk", Content: content}}}
				apply := func() (bool, error) {
					if entry == "captured plan" {
						return false, ApplyAgentPlan(p, plan)
					}
					return writeAgentFile(store, checkout, path, content)
				}
				changed, err := apply()
				require.ErrorIs(t, err, os.ErrPermission)
				assert.Contains(t, err.Error(), "installed, but syncing its directory tree failed")
				if entry == "compatibility writer" {
					assert.True(t, changed, "the rename applied despite the durability failure")
				}
				before, err := os.Stat(path)
				require.NoError(t, err)
				// Repeated retries cannot erase the persistent failure just
				// because the renamed file already has the desired bytes.
				for range 2 {
					changed, err = apply()
					assert.False(t, changed)
					require.ErrorIs(t, err, os.ErrPermission)
					assert.Contains(t, err.Error(), "syncing its directory tree failed")
				}
				require.NoError(t, os.Chmod(blocked, 0o755))
				changed, err = apply()
				require.NoError(t, err, "retry must reconfirm every ancestor through captured C")
				assert.False(t, changed, "confirmation is not a replacement")
				after, err := os.Stat(path)
				require.NoError(t, err)
				assert.True(t, os.SameFile(before, after), "no equal-content retry may replace the inode")
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, content, string(data))
				assert.Equal(t, before.ModTime(), after.ModTime())
				assert.NoDirExists(t, filepath.Join(checkout, ".punt-labs", "local"), "staging belongs to captured S, not C")
			})
		}
	}
}

func TestAgentEqualContentStillRejectsSymlinks(t *testing.T) {
	for _, entry := range []string{"compatibility writer", "captured plan"} {
		for _, link := range []string{"file", "parent"} {
			t.Run(entry+"/"+link, func(t *testing.T) {
				store, checkout, outside := physicalAgentTempDir(t), physicalAgentTempDir(t), physicalAgentTempDir(t)
				p, err := layout.NewRepository(store, checkout, layout.RecordBlame)
				require.NoError(t, err)
				const content = "same bytes\n"
				target := filepath.Join(outside, "bwk.md")
				writeFile(t, target, content)
				before, err := os.Stat(target)
				require.NoError(t, err)
				path := filepath.Join(checkout, ".claude", "agents", "bwk.md")
				var from, to string
				if link == "parent" {
					require.NoError(t, os.MkdirAll(filepath.Join(checkout, ".claude"), 0o755))
					from, to = outside, filepath.Dir(path)
				} else {
					require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
					from, to = target, path
				}
				if err := os.Symlink(from, to); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
				if entry == "captured plan" {
					err = ApplyAgentPlan(p, AgentPlan{Files: []AgentFile{{Handle: "bwk", Content: content}}})
				} else {
					var changed bool
					changed, err = writeAgentFile(store, checkout, path, content)
					assert.False(t, changed)
				}
				require.ErrorContains(t, err, "symlink")
				after, err := os.Stat(target)
				require.NoError(t, err)
				assert.True(t, os.SameFile(before, after))
				data, err := os.ReadFile(target)
				require.NoError(t, err)
				assert.Equal(t, content, string(data))
			})
		}
	}
}
