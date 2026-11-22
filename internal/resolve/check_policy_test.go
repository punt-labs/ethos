package resolve

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckPolicyOf(t *testing.T) {
	for _, tc := range []struct {
		name, local, want string
		localRoot, bad    bool
	}{
		{name: "missing", want: "main"},
		{name: "unrelated config", local: "agent: worker\n", want: "main"},
		{name: "branch", local: "checks: branch\n", want: "branch", localRoot: true},
		{name: "none", local: "checks: none\n", want: "none", localRoot: true},
		{name: "broken", local: "checks: [", bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, checkout := t.TempDir(), t.TempDir()
			if tc.local != "" {
				require.NoError(t, os.Mkdir(filepath.Join(checkout, ".punt-labs"), 0700))
				require.NoError(t, os.WriteFile(filepath.Join(checkout, ".punt-labs/ethos.yaml"), []byte(tc.local), 0600))
			}
			loc := Located{Store: store, Checkout: checkout, Config: &RepoConfig{Checks: "main"}}
			ref, root, err := CheckPolicyOf(loc)
			if tc.bad {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, ref)
			if tc.localRoot {
				store = checkout
			}
			require.Equal(t, filepath.Join(store, ".punt-labs/ethos"), root)
		})
	}
	ref, root, err := CheckPolicyOf(Located{})
	require.NoError(t, err)
	require.Empty(t, ref)
	require.Empty(t, root)
	for _, loc := range []Located{{StoreErr: errors.New("broken store")}, {CheckoutErr: errors.New("broken checkout")}} {
		_, _, err := CheckPolicyOf(loc)
		require.Error(t, err)
	}
}
