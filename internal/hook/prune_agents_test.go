package hook

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/agentmark"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeAgentsDirFile writes <root>/.claude/agents/<handle>.md with content,
// creating parent directories as needed.
func writeAgentsDirFile(t *testing.T, root, handle, content string) {
	t.Helper()
	dir := filepath.Join(root, ".claude", "agents")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, handle+".md"), []byte(content), 0o644))
}

func TestPruneOrphanedGeneratedAgents(t *testing.T) {
	t.Run("marked file not in keep is deleted", func(t *testing.T) {
		root := t.TempDir()
		writeAgentsDirFile(t, root, "gone", agentmark.Add("---\nname: gone\n---\nbody\n"))

		deleted, err := PruneOrphanedGeneratedAgents(root, map[string]bool{})
		require.NoError(t, err)
		assert.Equal(t, []string{"gone"}, deleted)
		assert.NoFileExists(t, filepath.Join(root, ".claude", "agents", "gone.md"))
	})

	t.Run("marked file in keep is kept", func(t *testing.T) {
		root := t.TempDir()
		writeAgentsDirFile(t, root, "kept", agentmark.Add("---\nname: kept\n---\nbody\n"))

		deleted, err := PruneOrphanedGeneratedAgents(root, map[string]bool{"kept": true})
		require.NoError(t, err)
		assert.Empty(t, deleted)
		assert.FileExists(t, filepath.Join(root, ".claude", "agents", "kept.md"))
	})

	t.Run("unmarked file not in keep is kept — the safety boundary", func(t *testing.T) {
		root := t.TempDir()
		writeAgentsDirFile(t, root, "handauthored", "---\nname: handauthored\n---\nbody\n")

		deleted, err := PruneOrphanedGeneratedAgents(root, map[string]bool{})
		require.NoError(t, err)
		assert.Empty(t, deleted)
		assert.FileExists(t, filepath.Join(root, ".claude", "agents", "handauthored.md"))
	})

	t.Run("missing .claude/agents dir: no error, no panic", func(t *testing.T) {
		root := t.TempDir()

		deleted, err := PruneOrphanedGeneratedAgents(root, map[string]bool{})
		require.NoError(t, err)
		assert.Nil(t, deleted)
	})

	t.Run("empty checkout root is refused, never globs the CWD", func(t *testing.T) {
		// A marked, off-keep file under the process CWD's .claude/agents must
		// survive: an empty checkoutRoot must not collapse to a relative glob
		// and delete it. Guards the Qodo/silent-failure MEDIUM on PR for
		// PL-878.
		cwd := t.TempDir()
		t.Chdir(cwd)
		writeAgentsDirFile(t, cwd, "cwd-victim", agentmark.Add("---\nname: cwd-victim\n---\nbody\n"))

		deleted, err := PruneOrphanedGeneratedAgents("", map[string]bool{})
		require.Error(t, err)
		assert.Empty(t, deleted)
		assert.FileExists(t, filepath.Join(cwd, ".claude", "agents", "cwd-victim.md"))
	})

	t.Run("removal error is surfaced, not swallowed", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root ignores directory write permission bits")
		}
		root := t.TempDir()
		dir := filepath.Join(root, ".claude", "agents")
		writeAgentsDirFile(t, root, "blocked", agentmark.Add("---\nname: blocked\n---\nbody\n"))
		require.NoError(t, os.Chmod(dir, 0o555))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

		deleted, err := PruneOrphanedGeneratedAgents(root, map[string]bool{})
		require.Error(t, err)
		assert.Empty(t, deleted)
	})

	t.Run("accumulates errors across multiple files via errors.Join", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root ignores directory write permission bits")
		}
		root := t.TempDir()
		dir := filepath.Join(root, ".claude", "agents")
		writeAgentsDirFile(t, root, "a", agentmark.Add("---\nname: a\n---\nbody\n"))
		writeAgentsDirFile(t, root, "b", agentmark.Add("---\nname: b\n---\nbody\n"))
		require.NoError(t, os.Chmod(dir, 0o555))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

		_, err := PruneOrphanedGeneratedAgents(root, map[string]bool{})
		require.Error(t, err)
		var joined interface{ Unwrap() []error }
		if errors.As(err, &joined) {
			assert.GreaterOrEqual(t, len(joined.Unwrap()), 2)
		}
	})
}
