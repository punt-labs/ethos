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
			env, _ := corpusEnv(t, v)
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

			out, err = runRepoADRs(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, 0, out.Items, "second run changes nothing")
		})
		t.Run(v+"/unlinked", func(t *testing.T) {
			env, _ := corpusEnv(t, v)
			out, err := runRepoADRs(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, 0, out.Items)
			_, statErr := os.Stat(filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "adrs", "DES-001.yaml"))
			assert.True(t, os.IsNotExist(statErr), "unlinked ADR stays global")
		})
	}
}

func TestRepoADRsLinkedByReference(t *testing.T) {
	env, _ := corpusEnv(t, "v4.20.4")
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

func TestRepoADRsNoGlobalADRs(t *testing.T) {
	env, _ := corpusEnv(t, "v4.22.1")
	out, err := runRepoADRs(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Items)
}

func TestRepoADRsDivergentCopyIsResidual(t *testing.T) {
	env, _ := corpusEnv(t, "v4.20.4")
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
