package resolve

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEffectiveEnablementPrecedence(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		file string
		on   bool
		text string
	}{
		{"", false, "ethos is off: no enablement marker"},
		{LocalEnabledFile, true, "ethos is on: " + LocalEnabledFile},
		{ProjectEnabledFile, true, "ethos is on: " + ProjectEnabledFile},
		{LocalDisabledFile, false, "ethos is off: " + LocalDisabledFile},
		{ProjectOptOutFile, false, "ethos is off: " + ProjectOptOutFile},
	} {
		if tc.file != "" {
			path := filepath.Join(root, tc.file)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, nil, 0o644))
		}
		got, err := EffectiveEnablement(root)
		require.NoError(t, err)
		assert.Equal(t, EnablementState{Enabled: tc.on, DecidingFile: tc.file}, got)
		assert.Equal(t, tc.text, got.String())
		optedOut, err := ProjectOptedOut(root)
		require.NoError(t, err)
		assert.Equal(t, tc.file == ProjectOptOutFile, optedOut)
	}
	state, err := EffectiveEnablement("")
	require.NoError(t, err)
	assert.Equal(t, EnablementState{}, state)
}

func TestEnablementRejectsMalformedCheckoutMarkers(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, ProjectOptOutFile)
	require.NoError(t, os.MkdirAll(marker, 0o755))
	_, err := EffectiveEnablement(root)
	require.ErrorContains(t, err, ProjectOptOutFile)
	require.NoError(t, os.Remove(marker))
	require.NoError(t, os.Remove(filepath.Dir(marker)))
	require.NoError(t, os.WriteFile(filepath.Dir(marker), nil, 0o644))
	_, err = EffectiveEnablement(root)
	require.ErrorContains(t, err, filepath.Dir(marker))
}

func TestProcessCheckoutWithoutResolvingStore(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	t.Chdir(root)
	t.Setenv("ETHOS_REPO_ROOT", "")
	require.NoError(t, os.Mkdir(filepath.Join(root, ".punt-labs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs", "ethos"), nil, 0o644))
	want, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	got, err := ProcessCheckout()
	require.NoError(t, err)
	assert.Equal(t, want, got)

	t.Setenv("ETHOS_REPO_ROOT", filepath.Join(root, "absent"))
	_, err = ProcessCheckout()
	require.Error(t, err)
}
