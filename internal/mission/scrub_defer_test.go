package mission

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

const deferMission = "m-2026-04-07-001"

func deferFixture(t *testing.T) (layout.Policy, string, string) {
	t.Helper()
	newLeakFixture(t)
	root := t.TempDir()
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	return p, root, t.TempDir()
}

// A scrub that finds a file's writer lock busy reports the file as busy and
// returns at once: it never blocks, and it never rewrites that file.
func TestScrubFilesDeferring_BusyLockDefers(t *testing.T) {
	m := ".punt-labs/ethos/missions/" + deferMission + "/"
	for _, tt := range []struct {
		name, rel, body string
		hold            func(t *testing.T, p layout.Policy, repo, global string) func()
	}{
		{"trace", ".punt-labs/ethos/missions.jsonl", `{"success_criteria":["/home/alice/a"]}` + "\n", func(t *testing.T, p layout.Policy, repo, global string) func() {
			return holdLock(t, func(fn func() error) error { return withTraceLock(RepoStatePath(repo), fn) })
		}},
		{"mission", m + "reflections.yaml", "reflections:\n    - reason: /home/alice/a\n", func(t *testing.T, p layout.Policy, repo, global string) func() {
			return holdLock(t, func(fn func() error) error { return NewRepositoryStore(p, global).withLock(deferMission, fn) })
		}},
		{"delegation", m + "delegations/d-1/prompt.md", "/home/alice/a\n", func(t *testing.T, p layout.Policy, repo, global string) func() {
			release, err := NewRepositoryStore(p, global).AcquireDelegationLock("d-1")
			require.NoError(t, err)
			return release
		}},
		{"mission delegations", m + "delegations/d-1/prompt.md", "/home/alice/a\n", func(t *testing.T, p layout.Policy, repo, global string) func() {
			release, err := AcquireMissionLockExclusive(repo, deferMission)
			require.NoError(t, err)
			return release
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, repo, global := deferFixture(t)
			writeTree(t, repo, map[string]string{tt.rel: tt.body})
			release := tt.hold(t, p, repo, global)
			defer release()

			done := make(chan struct{})
			var res ScrubResult
			var err error
			go func() {
				defer close(done)
				res, _, err = ScrubFilesDeferring(context.Background(), p, global, []string{tt.rel})
			}()
			<-done
			require.NoError(t, err)
			assert.Equal(t, []string{tt.rel}, res.Busy)
			assert.Empty(t, res.Changed)
			assert.Equal(t, tt.body, readFileRel(t, repo, tt.rel), "a busy file is left as it was")
		})
	}
}

// A cancelled context stops the scrub between files.
func TestScrubFilesDeferring_StopsOnCancel(t *testing.T) {
	p, repo, global := deferFixture(t)
	m := ".punt-labs/ethos/missions/" + deferMission + "/"
	writeTree(t, repo, map[string]string{
		m + "contract.yaml":    "mission_id: " + deferMission + "\ncontext: /home/alice/a\n",
		m + "reflections.yaml": "reflections:\n    - reason: /home/alice/a\n",
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, _, err := ScrubFilesDeferring(ctx, p, global, []string{m + "contract.yaml", m + "reflections.yaml"})
	require.NoError(t, err)
	assert.True(t, res.Interrupted)
	assert.Empty(t, res.Changed)
}

// M2: a durability warning is returned in the outcome, never printed.
func TestScrubFilesDeferring_WarningsGoToTheOutcome(t *testing.T) {
	p, repo, global := deferFixture(t)
	m := ".punt-labs/ethos/missions/" + deferMission + "/"
	rel := m + "reflections.yaml"
	writeTree(t, repo, map[string]string{rel: "reflections:\n    - reason: /home/alice/a\n"})
	real := syncDir
	t.Cleanup(func() { syncDir = real })
	syncDir = func(dir string) error {
		if dir == filepath.Dir(filepath.Join(repo, filepath.FromSlash(rel))) {
			return errors.New("injected sync failure")
		}
		return real(dir)
	}
	var res ScrubResult
	var out MutationOutcome
	var err error
	stderr := captureStderr(t, func() { res, out, err = ScrubFilesDeferring(context.Background(), p, global, []string{rel}) })
	require.NoError(t, err)
	assert.Equal(t, []string{rel}, res.Changed)
	assert.Empty(t, stderr)
	require.NotEmpty(t, out.Warnings)
	assert.Contains(t, out.Warnings[0], "injected sync failure")
	_, statErr := os.Stat(filepath.Join(repo, filepath.FromSlash(rel)))
	require.NoError(t, statErr)
}
