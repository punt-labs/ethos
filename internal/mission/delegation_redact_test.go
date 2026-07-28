//go:build !windows

package mission

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWriteDelegationSkeleton_Redacts is the regression gate for
// ethos-ersr and ethos-n4np: thirteen prompt.md files carrying
// /Users/<name>/ paths reached a public repo because the Tier B
// skeleton writer did not run the redactor the audit lines use.
//
// The assertion is written the way the defect was found — grep the
// bytes on disk for the home prefix — so it fails for any future
// leak path, not just the two fields the fix rewrites today.
func TestWriteDelegationSkeleton_Redacts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoRoot := t.TempDir()

	tests := []struct {
		name    string
		payload DelegationSkeleton
		want    func(t *testing.T, dir string, d *Delegation)
	}{
		{
			name: "home path in prompt becomes tilde",
			payload: DelegationSkeleton{
				Tier:   TierB,
				Prompt: []byte("read " + filepath.Join(home, ".claude", "notes.md")),
			},
			want: func(t *testing.T, dir string, _ *Delegation) {
				assert.Equal(t, "read ~/.claude/notes.md", readPrompt(t, dir))
			},
		},
		{
			name: "repo path in prompt becomes token",
			payload: DelegationSkeleton{
				Tier:   TierB,
				Prompt: []byte("edit " + filepath.Join(repoRoot, "internal", "hook", "audit_log.go")),
			},
			want: func(t *testing.T, dir string, _ *Delegation) {
				assert.Equal(t, "edit <repo>/internal/hook/audit_log.go", readPrompt(t, dir))
			},
		},
		{
			name: "multi-line prompt rewrites every occurrence",
			payload: DelegationSkeleton{
				Tier: TierB,
				Prompt: []byte("Working dir: " + repoRoot + "\n" +
					"Config: " + filepath.Join(home, ".punt-labs", "ethos.yaml") + "\n"),
			},
			want: func(t *testing.T, dir string, _ *Delegation) {
				assert.Equal(t,
					"Working dir: <repo>\nConfig: ~/.punt-labs/ethos.yaml\n",
					readPrompt(t, dir))
			},
		},
		{
			name: "record fields are redacted too",
			payload: DelegationSkeleton{
				Tier:      TierB,
				AgentType: "bwk",
				Session:   filepath.Join(home, "sessions", "s-1"),
			},
			want: func(t *testing.T, _ string, d *Delegation) {
				assert.Equal(t, "~/sessions/s-1", d.Session)
				assert.Equal(t, "bwk", d.AgentType, "a value with no path must survive verbatim")
			},
		},
		{
			name: "prompt with no absolute path is byte-identical",
			payload: DelegationSkeleton{
				Tier:   TierB,
				Prompt: []byte("fix the bug in internal/hook/audit_log.go"),
			},
			want: func(t *testing.T, dir string, _ *Delegation) {
				assert.Equal(t, "fix the bug in internal/hook/audit_log.go", readPrompt(t, dir))
			},
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			missionID := "m-2026-07-28-016"
			delegationID := "d-2026-07-28-00" + string(rune('1'+i))

			recordPath, err := WriteDelegationSkeleton(repoRoot, missionID, delegationID, tt.payload)
			require.NoError(t, err)
			dir := filepath.Dir(recordPath)

			d, err := LoadDelegation(recordPath)
			require.NoError(t, err)
			tt.want(t, dir, d)

			// The defect's own detector: no byte on disk under this
			// delegation may name the operator's home directory.
			for _, name := range []string{"prompt.md", "record.yaml"} {
				data, rErr := os.ReadFile(filepath.Join(dir, name))
				if os.IsNotExist(rErr) {
					continue
				}
				require.NoError(t, rErr)
				assert.NotContains(t, string(data), home,
					"%s must not carry an absolute home path", name)
				assert.NotContains(t, string(data), repoRoot,
					"%s must not carry an absolute repo path", name)
			}
		})
	}
}

// TestWriteDelegationSkeleton_NoHomeFailsClosed asserts the fail-safe
// rule: a home directory ethos cannot resolve is one it cannot redact,
// so the write is refused rather than completed with the operator's
// paths intact. The PreToolUse dispatch turns this error into a spawn
// refusal.
func TestWriteDelegationSkeleton_NoHomeFailsClosed(t *testing.T) {
	repoRoot := t.TempDir()
	t.Setenv("HOME", "")

	_, err := WriteDelegationSkeleton(repoRoot, "m-1", "d-1", DelegationSkeleton{
		Tier:   TierB,
		Prompt: []byte("/Users/someone/secret.md"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path redaction")

	dir := DelegationDir(repoRoot, "m-1", "d-1")
	for _, name := range []string{"prompt.md", "record.yaml"} {
		_, statErr := os.Stat(filepath.Join(dir, name))
		assert.True(t, os.IsNotExist(statErr),
			"%s must not exist when redaction could not be applied", name)
	}
}

// TestCloseDelegation_RedactsReason covers the other free-text field on
// a git-tracked record. The repo prefix is not knowable from a bare
// record path; the home prefix — the one carrying the username — is.
func TestCloseDelegation_RedactsReason(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoRoot := t.TempDir()

	recordPath, err := WriteDelegationSkeleton(repoRoot, "m-1", "d-1", DelegationSkeleton{Tier: TierB})
	require.NoError(t, err)

	reason := "worker refused: cannot read " + filepath.Join(home, "secrets.yaml")
	require.NoError(t, CloseDelegation(recordPath, DelegationVerdictAborted, reason))

	d, err := LoadDelegation(recordPath)
	require.NoError(t, err)
	assert.Equal(t, "worker refused: cannot read ~/secrets.yaml", d.Reason)

	data, err := os.ReadFile(recordPath)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(data), home),
		"closed record must not carry an absolute home path")
}

func readPrompt(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "prompt.md"))
	require.NoError(t, err)
	return string(data)
}
