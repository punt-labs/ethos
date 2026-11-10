package autorepair

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/adr"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

// linkGlobalADR rewrites the corpus global DES-001 to name mission id.
func linkGlobalADR(t *testing.T, env Env, id string) {
	t.Helper()
	path := filepath.Join(env.Global, "adrs", "DES-001.yaml")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	b = append(b, []byte("mission_id: "+id+"\n")...)
	require.NoError(t, os.WriteFile(path, b, 0o600))
}

func TestRepoADRs(t *testing.T) {
	for _, v := range []string{"v4.0.0", "v4.8.0", "v4.13.0", "v4.17.0", "v4.20.4"} {
		t.Run(v+"/linked", func(t *testing.T) {
			env, _ := corpusEnvLogged(t, v)
			linkGlobalADR(t, env, "m-2026-11-09-001")
			out, err := runRepoADRs(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, 1, out.Items)
			assert.Empty(t, out.Residuals)
			dest := filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "adrs", "DES-001.yaml")
			assert.FileExists(t, dest)
			assert.FileExists(t, filepath.Join(env.Global, "adrs", "DES-001.yaml"), "global copy remains")
			s, err := adr.NewRepositoryStore(env.Policy, filepath.Join(env.Global, "adrs"))
			require.NoError(t, err)
			a, err := s.Load("DES-001")
			require.NoError(t, err)
			assert.Equal(t, "Use a corpus", a.Title)

			repoBefore, globalBefore := treeDigest(t, env.Roots.Store), treeDigest(t, env.Global)
			out, err = runRepoADRs(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, 0, out.Items, "second run changes nothing")
			assert.Equal(t, repoBefore, treeDigest(t, env.Roots.Store), "second run leaves the repository tree unchanged")
			assert.Equal(t, globalBefore, treeDigest(t, env.Global), "second run leaves the global tree unchanged")
		})
		t.Run(v+"/unlinked", func(t *testing.T) {
			env, _ := corpusEnvLogged(t, v)
			out, err := runRepoADRs(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, 0, out.Items)
			_, statErr := os.Stat(filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "adrs", "DES-001.yaml"))
			assert.True(t, os.IsNotExist(statErr), "unlinked ADR stays global")
		})
	}
}

func TestRepoADRsLinkedByReference(t *testing.T) {
	env, _ := corpusEnvLogged(t, "v4.20.4")
	cp := filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "missions", "m-2026-11-09-001", "contract.yaml")
	b, err := os.ReadFile(cp)
	require.NoError(t, err)
	s := strings.Replace(string(b), "inputs:\n", "inputs:\n    references:\n        - DES-001\n", 1)
	require.NotEqual(t, string(b), s)
	require.NoError(t, os.WriteFile(cp, []byte(s), 0o600))
	out, err := runRepoADRs(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 1, out.Items)
}

// TestRepoADRsLinkedByFreeFormReference: a reference entry that is not exactly
// the ID still links the ADR: a path to its page, or the ID with a title.
func TestRepoADRsLinkedByFreeFormReference(t *testing.T) {
	for _, ref := range []string{"docs/DES-001.md", "'DES-001: Use a corpus'", "see DES-001 before changing the corpus"} {
		t.Run(ref, func(t *testing.T) {
			env, _ := corpusEnvLogged(t, "v4.20.4")
			cp := filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "missions", "m-2026-11-09-001", "contract.yaml")
			b, err := os.ReadFile(cp)
			require.NoError(t, err)
			s := strings.Replace(string(b), "inputs:\n", "inputs:\n    references:\n        - "+ref+"\n", 1)
			require.NotEqual(t, string(b), s)
			require.NoError(t, os.WriteFile(cp, []byte(s), 0o600))
			out, err := runRepoADRs(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, 1, out.Items, "the ADR named inside the reference is linked")
			assert.FileExists(t, filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "adrs", "DES-001.yaml"))
		})
	}
}

// TestRepoADRsPrintsNothing: a step never writes to stderr, including when the
// redactor rewrites the copy. The global ADR is given a home path so that the
// repository copy differs from the source and the rewrite notice fires.
func TestRepoADRsPrintsNothing(t *testing.T) {
	env, log := corpusEnvLogged(t, "v4.20.4")
	linkGlobalADR(t, env, "m-2026-11-09-001")
	path := filepath.Join(env.Global, "adrs", "DES-001.yaml")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	home := os.Getenv("HOME")
	s := strings.Replace(string(b), "context: tests for the upgrader", "context: tests for the upgrader in "+home+"/work", 1)
	require.NotEqual(t, string(b), s)
	require.NoError(t, os.WriteFile(path, []byte(s), 0o600))

	var out Outcome
	stderr := testhelpers.CaptureStderr(t, func() {
		out, err = runRepoADRs(context.Background(), env)
	})
	require.NoError(t, err)
	assert.Equal(t, 1, out.Items)
	assert.Empty(t, stderr, "the step logs; it never prints")
	assert.Contains(t, log.steps, "repo-adrs:redacted", "the rewrite notice reaches the log")
	got, err := os.ReadFile(filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "adrs", "DES-001.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(got), home, "the repository copy is redacted")
}

// TestRepoADRsContractEdgeCases: a contract with no mission_id must not make
// the empty ID a match for every ADR without one, and an unreadable contract
// is logged, not skipped in silence.
func TestRepoADRsContractEdgeCases(t *testing.T) {
	env, log := corpusEnvLogged(t, "v4.20.4")
	missions := filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "missions")
	require.NoError(t, os.MkdirAll(filepath.Join(missions, "m-no-id"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(missions, "m-no-id", "contract.yaml"), []byte("status: open\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(missions, "m-torn"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(missions, "m-torn", "contract.yaml"), []byte("mission_id: [\n"), 0o600))
	// The corpus global ADR has no mission_id: with the bug, missions[""] links it.
	out, err := runRepoADRs(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Items, "an ADR with no mission_id is not linked by a contract with none")
	assert.Contains(t, log.steps, "repo-adrs:contract-unreadable")
}

func TestRepoADRsNoGlobalADRs(t *testing.T) {
	env, _ := corpusEnvLogged(t, "v4.22.1")
	out, err := runRepoADRs(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Items)
}

func TestRepoADRsDivergentCopyIsResidual(t *testing.T) {
	env, _ := corpusEnvLogged(t, "v4.20.4")
	linkGlobalADR(t, env, "m-2026-11-09-001")
	dir := filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "adrs")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "DES-001.yaml"), []byte(
		"id: DES-001\ntitle: Different\nstatus: proposed\ncreated_at: \"2026-11-09T23:02:12Z\"\nupdated_at: \"2026-11-09T23:02:12Z\"\nauthor: tperson\ncontext: c\ndecision: d\nmission_id: m-2026-11-09-001\n"), 0o600))
	out, err := runRepoADRs(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Items)
	require.Len(t, out.Residuals, 1)
	assert.Equal(t, "repo-adrs", out.Residuals[0].Step)
	assert.Equal(t, "DES-001", out.Residuals[0].ID)
}
