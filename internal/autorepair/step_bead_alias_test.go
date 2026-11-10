package autorepair

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var corpusVersions34 = []string{
	"v3.11.0", "v3.12.0", "v4.0.0", "v4.8.0", "v4.13.0",
	"v4.17.0", "v4.20.4", "v4.21.0", "v4.22.1",
}

// plantBead34 turns every inputs.ticket in the staged repository contracts
// into inputs.bead, as a contract written through the deprecated alias would
// be on disk, and returns the contract paths with their pre-plant bytes.
func plantBead34(t *testing.T, env Env) map[string]string {
	t.Helper()
	want := map[string]string{}
	for _, dir := range missionDirs34(t, env) {
		path := filepath.Join(dir, "contract.yaml")
		orig := read34(t, path)
		require.Contains(t, orig, "\n    ticket: ", "%s: corpus contract has no inputs.ticket", path)
		write34(t, path, strings.Replace(orig, "\n    ticket: ", "\n    bead: ", 1))
		want[path] = orig
	}
	return want
}

func TestBeadAlias_Corpus(t *testing.T) {
	for _, v := range corpusVersions34 {
		t.Run(v, func(t *testing.T) {
			require.True(t, provenanceOK34(t, v, "mission create (inputs.bead)"), "scenario step missing")
			env := stage34(t, v)
			if len(missionDirs34(t, env)) == 0 {
				// v3.11.0 and v3.12.0 keep missions in the global store only, so
				// the repository holds no contract for this step to rewrite.
				t.Skipf("%s keeps no repository mission contracts; the step has nothing to rewrite", v)
			}
			want := plantBead34(t, env)

			out := run34(t, "bead-alias", env)
			assert.Equal(t, len(want), out.Items, "contracts rewritten")
			assert.False(t, out.Deferred)
			assert.Empty(t, out.Residuals)
			for path, orig := range want {
				got := read34(t, path)
				assert.Equal(t, orig, got, "%s: every byte but the key is unchanged", path)
				assert.Empty(t, mission.BeadAlias([]byte(got)), "%s still has inputs.bead", path)
			}

			again := run34(t, "bead-alias", env)
			assert.Zero(t, again.Items, "second run is a no-op")
			for path, orig := range want {
				assert.Equal(t, orig, read34(t, path))
			}
		})
	}
}

// A flow-style inputs mapping has no key on its own line; the step falls back
// to the store's own load and save, which keeps the value.
func TestBeadAlias_FlowStyle(t *testing.T) {
	env := stage34(t, "v4.17.0")
	dirs := missionDirs34(t, env)
	require.NotEmpty(t, dirs)
	path := filepath.Join(dirs[0], "contract.yaml")
	orig := read34(t, path)
	flow := strings.Replace(orig, "inputs:\n    ticket: ethos-0001", "inputs: {bead: ethos-0001}", 1)
	require.NotEqual(t, orig, flow)
	write34(t, path, flow)

	out := run34(t, "bead-alias", env)
	assert.Equal(t, 1, out.Items)
	got := read34(t, path)
	assert.Empty(t, mission.BeadAlias([]byte(got)))
	c, err := mission.DecodeContractStrict([]byte(got), "flow")
	require.NoError(t, err)
	assert.Equal(t, "ethos-0001", c.Inputs.Ticket)
	assert.Zero(t, run34(t, "bead-alias", env).Items)
}

// A contract that sets both keys is not ours to choose between: it stays as it
// is and is recorded.
func TestBeadAlias_BothKeysIsResidual(t *testing.T) {
	env := stage34(t, "v4.17.0")
	dirs := missionDirs34(t, env)
	require.NotEmpty(t, dirs)
	path := filepath.Join(dirs[0], "contract.yaml")
	both := strings.Replace(read34(t, path), "\n    ticket: ethos-0001", "\n    ticket: ethos-0001\n    bead: ethos-0001", 1)
	write34(t, path, both)

	out := run34(t, "bead-alias", env)
	assert.Zero(t, out.Items)
	require.Len(t, out.Residuals, 1)
	assert.Equal(t, "bead-alias", out.Residuals[0].Step)
	assert.Equal(t, filepath.Base(dirs[0]), out.Residuals[0].ID)
	assert.Equal(t, both, read34(t, path))

	again := run34(t, "bead-alias", env)
	assert.Equal(t, out.Residuals, again.Residuals, "same residual, not a second one")
}

func TestBeadAlias_OutsideRepositoryDoesNothing(t *testing.T) {
	out := run34(t, "bead-alias", Env{Log: &recLog34{}})
	assert.Equal(t, Outcome{}, out)
}

// L6: the step's real entry re-encodes the contract in its canonical form, so a
// comment is dropped and every value is kept.
func TestBeadAlias_RewriteIsCanonicalRoundTrip(t *testing.T) {
	env := stage34(t, "v4.17.0")
	dirs := missionDirs34(t, env)
	require.NotEmpty(t, dirs)
	path := filepath.Join(dirs[0], "contract.yaml")
	orig := read34(t, path)
	want, err := mission.DecodeContractStrict([]byte(orig), path)
	require.NoError(t, err)
	planted := "# a comment the canonical form drops\n" + strings.Replace(orig, "\n    ticket: ", "\n    bead: ", 1)
	write34(t, path, planted)

	out := run34(t, "bead-alias", env)
	require.Equal(t, 1, out.Items)
	got := read34(t, path)
	assert.NotContains(t, got, "a comment the canonical form drops")
	have, err := mission.DecodeContractStrict([]byte(got), path)
	require.NoError(t, err)
	assert.Equal(t, want, have, "every value survives the round trip")
}

// M4: a busy lock defers; an unreadable contract is permanent and is a
// residual, so the stamp is not held back forever.
func TestBeadAlias_BusyLockDefers(t *testing.T) {
	env := stage34(t, "v4.17.0")
	want := plantBead34(t, env)
	id := filepath.Base(missionDirs34(t, env)[0])
	var out Outcome
	require.NoError(t, mission.WithRecordLockTry(env.Policy, id, func() error {
		out = run34(t, "bead-alias", env)
		return nil
	}))
	assert.True(t, out.Deferred)
	assert.Empty(t, out.Residuals)
	assert.Equal(t, len(want)-1, out.Items, "the unlocked mission is still rewritten")
}

func TestBeadAlias_UnreadableContractIsResidual(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads every file")
	}
	env := stage34(t, "v4.17.0")
	plantBead34(t, env)
	path := filepath.Join(missionDirs34(t, env)[0], "contract.yaml")
	require.NoError(t, os.Chmod(path, 0))
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	out := run34(t, "bead-alias", env)
	assert.False(t, out.Deferred, "a permanent condition never defers")
	require.Len(t, out.Residuals, 1)
	assert.Equal(t, "bead-alias", out.Residuals[0].Step)
	assert.Contains(t, out.Residuals[0].Reason, "unreadable")
}

// A residual reason names the class of failure. A YAML parse error carries line
// numbers and token text from the file, and none of it may reach the reason.
func TestBeadAlias_ReasonCarriesNoFileText(t *testing.T) {
	const token = "ghp_SECRETTOKEN"
	parse := fmt.Errorf("%w: %w", mission.ErrContractParse, errors.New("yaml: line 7: did not find expected ',' near "+token))
	for name, err := range map[string]error{
		"parse":       parse,
		"both":        mission.ErrBothBeadKeys,
		"not mapping": mission.ErrContractNotMapping,
		"other":       errors.New("encoding contract: " + token),
	} {
		t.Run(name, func(t *testing.T) {
			reason := beadAliasReason("rename", err)
			assert.NotContains(t, reason, token)
			assert.NotContains(t, reason, "line 7")
			assert.Contains(t, reason, "cannot be rewritten")
		})
	}
}
