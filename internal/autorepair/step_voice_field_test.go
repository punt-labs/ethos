package autorepair

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/identity"
)

func yamlMap(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, yaml.Unmarshal(data, &m))
	return m
}

func TestVoiceFieldStepGlobalIdentity(t *testing.T) {
	tr := s81113Copy(t, "damage/top-level-voice-field")
	env := tr.Env(t)
	path := filepath.Join(tr.Home, "identities", "vperson.yaml")
	before := yamlMap(t, s81113Read(t, path))
	require.Contains(t, before, "voice")

	out := s81113Run(t, "voice-field", env)
	assert.Equal(t, 1, out.Items)
	assert.False(t, out.Deferred)
	assert.Empty(t, out.Residuals)

	after := yamlMap(t, s81113Read(t, path))
	assert.NotContains(t, after, "voice", "no top-level voice remains")
	delete(before, "voice")
	assert.Equal(t, before, after, "other identity content is unchanged")

	vox, err := identity.NewStore(tr.Home).ExtGet("vperson", "vox", "")
	require.NoError(t, err)
	assert.Equal(t, "say", vox["provider"])
	assert.Equal(t, "Samantha", vox["voice"])

	require.NotEmpty(t, tr.Log.entries)
	for _, e := range tr.Log.entries {
		assert.Equal(t, "voice-field", e.step)
		assert.NoError(t, e.err)
		for _, p := range e.paths {
			assert.NotContains(t, p, "Samantha")
		}
	}
	snap := s81113Snapshot(t, tr)
	assert.Equal(t, 0, s81113Run(t, "voice-field", env).Items, "second run changes nothing")
	assert.Equal(t, snap, s81113Snapshot(t, tr), "second run leaves the tree identical")
}

func TestVoiceFieldStepRepositoryIdentity(t *testing.T) {
	tr := s81113Copy(t, "damage/top-level-voice-field")
	env := tr.Env(t)
	src := s81113Read(t, filepath.Join(tr.Home, "identities", "vperson.yaml"))
	repoPath := filepath.Join(tr.Repo, ".punt-labs", "ethos", "identities", "vperson.yaml")
	s81113Write(t, repoPath, string(src))
	require.NoError(t, os.Remove(filepath.Join(tr.Home, "identities", "vperson.yaml")))

	out := s81113Run(t, "voice-field", env)
	assert.Equal(t, 1, out.Items)
	assert.NotContains(t, yamlMap(t, s81113Read(t, repoPath)), "voice")
	// Layered resolution: extensions live in the global store.
	vox, err := identity.NewStore(tr.Home).ExtGet("vperson", "vox", "")
	require.NoError(t, err)
	assert.Equal(t, "Samantha", vox["voice"])
	after := s81113Snapshot(t, tr)
	assert.Equal(t, 0, s81113Run(t, "voice-field", env).Items)
	assert.Equal(t, after, s81113Snapshot(t, tr), "second run leaves the tree identical")
}

func TestVoiceFieldStepRepositoryResidualNamesIdentityFile(t *testing.T) {
	tr := s81113Copy(t, "damage/top-level-voice-field")
	path := filepath.Join(tr.Repo, ".punt-labs", "ethos", "identities", "rperson.yaml")
	s81113Write(t, path, "name: V\nhandle: rperson\nkind: human\nvoice: elevenlabs\n")
	require.NoError(t, os.Remove(filepath.Join(tr.Home, "identities", "vperson.yaml")))

	out := s81113Run(t, "voice-field", tr.Env(t))
	require.Len(t, out.Residuals, 1)
	assert.Equal(t, ".punt-labs/ethos/identities/rperson.yaml", out.Residuals[0].ID, "the residual names the identity file relative to the store")
	assert.True(t, s81113Exists(filepath.Join(tr.Repo, filepath.FromSlash(out.Residuals[0].ID))))
	assertNoAbsolute(t, tr, out)
}

func TestVoiceFieldStepSymlinkedIdentityIsResidualAndOthersProceed(t *testing.T) {
	tr := s81113Copy(t, "damage/top-level-voice-field")
	env := tr.Env(t)
	legacy := string(s81113Read(t, filepath.Join(tr.Home, "identities", "vperson.yaml")))
	dir := filepath.Join(tr.Repo, ".punt-labs", "ethos", "identities")
	s81113Write(t, filepath.Join(dir, "real.yaml"), "name: R\nhandle: real\nkind: human\n")
	require.NoError(t, os.Symlink(filepath.Join(dir, "real.yaml"), filepath.Join(dir, "alink.yaml")))
	s81113Write(t, filepath.Join(dir, "rperson.yaml"), legacy)

	out, err := runVoiceField(t.Context(), env)
	require.NoError(t, err, "a refused path is a residual, not an error")
	assert.Equal(t, 2, out.Items, "the global and the repository identity are still migrated")
	require.Len(t, out.Residuals, 1)
	assert.Equal(t, ".punt-labs/ethos/identities/alink.yaml", out.Residuals[0].ID)
	assertNoAbsolute(t, tr, out)
	assert.NotContains(t, yamlMap(t, s81113Read(t, filepath.Join(dir, "rperson.yaml"))), "voice")
}

func TestVoiceFieldStepKeepsExistingVoxValues(t *testing.T) {
	tr := s81113Copy(t, "damage/top-level-voice-field")
	s81113Write(t, filepath.Join(tr.Home, "identities", "vperson.ext", "vox.yaml"), "voice: Alex\n")
	s81113Run(t, "voice-field", tr.Env(t))
	vox, err := identity.NewStore(tr.Home).ExtGet("vperson", "vox", "")
	require.NoError(t, err)
	assert.Equal(t, "Alex", vox["voice"], "an extension value already set wins over the legacy field")
	assert.Equal(t, "say", vox["provider"])
}

func TestVoiceFieldStepUnmigratableVoiceIsResidual(t *testing.T) {
	tr := s81113Copy(t, "damage/top-level-voice-field")
	path := filepath.Join(tr.Home, "identities", "vperson.yaml")
	s81113Write(t, path, "name: V\nhandle: vperson\nkind: human\nvoice: elevenlabs\n")
	want := s81113Read(t, path)

	out := s81113Run(t, "voice-field", tr.Env(t))
	assert.Equal(t, 0, out.Items)
	assert.False(t, out.Deferred)
	require.Len(t, out.Residuals, 1)
	assert.Equal(t, "voice-field", out.Residuals[0].Step)
	assert.Equal(t, want, s81113Read(t, path), "an identity that cannot be converted is left unchanged")
}

func TestVoiceFieldStepCurrentCorpora(t *testing.T) {
	for _, fixture := range []string{"corpus/v3.11.0", "corpus/v4.8.0", "corpus/v4.17.0", "corpus/v4.22.1"} {
		t.Run(fixture, func(t *testing.T) {
			tr := s81113Copy(t, fixture)
			out := s81113Run(t, "voice-field", tr.Env(t))
			assert.Equal(t, 0, out.Items)
			assert.False(t, out.Deferred)
			assert.Empty(t, out.Residuals)
			assert.Empty(t, tr.Log.entries)
		})
	}
}

// assertNoAbsolute checks that no residual, which lands in the layout stamp,
// carries the home directory or the temporary tree.
func assertNoAbsolute(t *testing.T, tr s81113Tree, out Outcome) {
	t.Helper()
	for _, r := range out.Residuals {
		for _, bad := range []string{os.Getenv("HOME"), filepath.Dir(tr.Repo), tr.Repo, tr.Home} {
			assert.NotContains(t, r.ID+" "+r.Reason, bad)
		}
	}
}

// A symlinked global identity must be neither read nor rewritten through.
func TestVoiceFieldStepGlobalSymlinkedIdentityIsResidual(t *testing.T) {
	tr := s81113Copy(t, "damage/top-level-voice-field")
	link := filepath.Join(tr.Home, "identities", "vperson.yaml")
	outside := filepath.Join(filepath.Dir(tr.Home), "outside-id.yaml")
	s81113Write(t, outside, string(s81113Read(t, link)))
	want := s81113Read(t, outside)
	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Symlink(outside, link))

	out := s81113Run(t, "voice-field", tr.Env(t))
	assert.Equal(t, 0, out.Items)
	fi, err := os.Lstat(link)
	require.NoError(t, err)
	assert.NotZero(t, fi.Mode()&os.ModeSymlink, "the link is still a link")
	assert.Equal(t, want, s81113Read(t, outside), "the file outside is untouched")
	require.Len(t, out.Residuals, 1)
	assert.Equal(t, "identities/vperson.yaml", out.Residuals[0].ID)
	assertNoAbsolute(t, tr, out)
}

func TestVoiceFieldStepGlobalSymlinkedIdentitiesDirIsResidual(t *testing.T) {
	tr := s81113Copy(t, "damage/top-level-voice-field")
	dir := filepath.Join(tr.Home, "identities")
	outside := filepath.Join(filepath.Dir(tr.Home), "outside-ids")
	require.NoError(t, os.Rename(dir, outside))
	require.NoError(t, os.Symlink(outside, dir))
	want := s81113Read(t, filepath.Join(outside, "vperson.yaml"))

	out := s81113Run(t, "voice-field", tr.Env(t))
	assert.Equal(t, 0, out.Items)
	require.Len(t, out.Residuals, 1)
	assert.Equal(t, want, s81113Read(t, filepath.Join(outside, "vperson.yaml")))
	assertNoAbsolute(t, tr, out)
}

// The rewrite removes exactly the voice lines: sequences keep their indent and
// the comment above voice: stays.
func TestVoiceFieldStepRewriteKeepsEveryOtherByte(t *testing.T) {
	tr := s81113Copy(t, "damage/top-level-voice-field")
	path := filepath.Join(tr.Home, "identities", "vperson.yaml")
	in := "name: Voice Person # inline\nhandle: vperson\nkind: human\ntalents:\n  - engineering\n  - security\n# above voice\nvoice:\n  provider: say\n  voice: Samantha\nemail: \"v@example.com\"\n"
	s81113Write(t, path, in)
	s81113Write(t, filepath.Join(tr.Home, "identities", "vperson.ext", "vox.yaml"), "voice: Alex\n")

	out := s81113Run(t, "voice-field", tr.Env(t))
	assert.Equal(t, 1, out.Items)
	want := "name: Voice Person # inline\nhandle: vperson\nkind: human\ntalents:\n  - engineering\n  - security\n# above voice\nemail: \"v@example.com\"\n"
	assert.Equal(t, want, string(s81113Read(t, path)))
	vox, err := identity.NewStore(tr.Home).ExtGet("vperson", "vox", "")
	require.NoError(t, err)
	assert.Equal(t, "Alex", vox["voice"])
	assert.Equal(t, "say", vox["provider"])
}

// A legacy value the extension already overrides is dropped; the drop is logged
// by key, never by value.
func TestVoiceFieldStepLogsKeysItDropped(t *testing.T) {
	tr := s81113Copy(t, "damage/top-level-voice-field")
	s81113Write(t, filepath.Join(tr.Home, "identities", "vperson.ext", "vox.yaml"), "voice: Alex\n")
	s81113Run(t, "voice-field", tr.Env(t))
	var dropped []string
	for _, e := range tr.Log.entries {
		if e.action == "drop-superseded" {
			dropped = append(dropped, e.paths...)
			assert.NoError(t, e.err)
		}
	}
	assert.Contains(t, dropped, "vperson/voice")
	for _, p := range dropped {
		assert.NotContains(t, p, "Samantha")
		assert.NotContains(t, p, "Alex")
	}
}
