package hook

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/role"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/punt-labs/ethos/v4/internal/team"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// snapshotPipeReader announces that ReadInput has reached the blocked pipe.
// The writer may then replace config without racing any in-memory dependencies.
type snapshotPipeReader struct {
	*io.PipeReader
	entered chan struct{}
	first   bool
}

func (r *snapshotPipeReader) Read(p []byte) (int, error) {
	if !r.first {
		r.first = true
		close(r.entered)
	}
	return r.PipeReader.Read(p)
}

// withSnapshotPipe replaces config only after the handler is waiting on stdin.
func withSnapshotPipe(t *testing.T, path, replacement string, handle func(io.Reader) error) (string, error) {
	t.Helper()
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	entered := make(chan struct{})
	written := make(chan error, 1)
	go func() {
		<-entered
		if err := os.WriteFile(path, []byte(replacement), 0o600); err != nil {
			written <- err
			w.CloseWithError(err)
			return
		}
		_, err := io.WriteString(w, `{"session_id":"snapshot-session"}`)
		if closeErr := w.Close(); err == nil {
			err = closeErr
		}
		written <- err
	}()
	out, err := captureStdout(t, func() error {
		return handle(&snapshotPipeReader{PipeReader: r, entered: entered})
	})
	require.NoError(t, <-written)
	return out, err
}

func snapshotHookStores(t *testing.T) (resolve.Located, *identity.Store, *session.Store, *team.LayeredStore, *role.LayeredStore) {
	t.Helper()
	isolateGitConfig(t, "snapshot-human")
	id := &identity.Identity{Name: "Alpha Agent", Handle: "alpha", Kind: "agent", Personality: "snapshot-personality"}
	s, ss := setupIdentityWithAttributes(t, id, "# Snapshot\n\nUse the captured selection.\n", "")
	for _, h := range []string{"beta", "gamma", "delta"} {
		require.NoError(t, s.Save(&identity.Identity{
			Name: h + " Agent", Handle: h, Kind: "agent", Personality: "snapshot-personality",
		}))
	}
	repo := setupRepoWithAgent(t, "alpha")
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".punt-labs", "ethos.yaml"), []byte("agent: alpha\nteam: alpha-team\nrecord: blame\n"), 0o600))
	ts := team.NewLayeredStore("", s.Root())
	rs := role.NewLayeredStore("", s.Root())
	require.NoError(t, rs.Save(&role.Role{Name: "worker", Tools: []string{"Read"}}))
	for name, handles := range map[string][]string{"alpha-team": {"alpha", "gamma"}, "beta-team": {"beta", "delta"}} {
		members := []team.Member{}
		for _, h := range handles {
			members = append(members, team.Member{Identity: h, Role: "worker"})
		}
		require.NoError(t, ts.Save(&team.Team{Name: name, Members: members}, s.Exists, rs.Exists))
	}
	loc := resolve.Roots()
	require.NoError(t, loc.Err())
	return loc, s, ss, ts, rs
}

func TestPreCompactSnapshot_BlockedInput(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		for _, replacement := range []string{"agent: beta\nteam: beta-team\nrecord: full\n", "agent: [invalid\n"} {
			t.Run(fmt.Sprintf("explicit=%t/%s", explicit, replacement), func(t *testing.T) {
				loc, s, ss, ts, rs := snapshotHookStores(t)
				require.NoError(t, ss.Create("snapshot-session",
					session.Participant{AgentID: "human", Persona: "snapshot-human"},
					session.Participant{AgentID: "agent", Persona: "alpha", Parent: "human"}, "", ""))
				deps := PreCompactDeps{Identities: s, Sessions: ss, Teams: ts, Roles: rs}
				if explicit {
					deps.Located = &loc
				}
				out, err := withSnapshotPipe(t, filepath.Join(loc.Store, ".punt-labs", "ethos.yaml"), replacement, func(r io.Reader) error {
					return HandlePreCompact(r, deps)
				})
				require.NoError(t, err)
				assert.Contains(t, out, "Alpha Agent")
				assert.Contains(t, out, "## Team: alpha-team")
				assert.NotContains(t, out, "beta-team")
				assert.NotContains(t, out, "could not")
			})
		}
	}
}

func TestHookSnapshot_EntryConfigFailureSurvivesRepair(t *testing.T) {
	for _, name := range []string{"session-start", "pre-compact"} {
		t.Run(name, func(t *testing.T) {
			loc, s, ss, ts, rs := snapshotHookStores(t)
			path := filepath.Join(loc.Store, ".punt-labs", "ethos.yaml")
			require.NoError(t, os.WriteFile(path, []byte("agent: [invalid\n"), 0o600))
			loc = resolve.Roots()
			require.Error(t, loc.ConfigErr)
			var handle func(io.Reader) error
			if name == "session-start" {
				deps := SessionStartDeps{Located: &loc, Store: s, Sessions: ss, Teams: ts, Roles: rs}
				handle = func(r io.Reader) error { return HandleSessionStart(r, deps) }
			} else {
				require.NoError(t, ss.Create("snapshot-session",
					session.Participant{AgentID: "human", Persona: "snapshot-human"},
					session.Participant{AgentID: "agent", Persona: "alpha", Parent: "human"}, "", ""))
				deps := PreCompactDeps{Located: &loc, Identities: s, Sessions: ss, Teams: ts, Roles: rs}
				handle = func(r io.Reader) error { return HandlePreCompact(r, deps) }
			}
			out, err := withSnapshotPipe(t, path, "agent: alpha\nteam: alpha-team\n", handle)
			if name == "session-start" {
				require.Error(t, err, "repair after entry must not erase the captured config error")
			} else {
				require.NoError(t, err, "pre-compact reports refusal in its sidecar output")
			}
			assert.Contains(t, out, "could not be read")
			assert.NotContains(t, out, "Alpha Agent")
			assert.NotContains(t, out, "## Team: alpha-team")
		})
	}
}
