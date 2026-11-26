//go:build linux || darwin

package main

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunHandoffRefreshDataRequiresCheckoutAndDeclaredData(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	out, _, err := execHandler(t, "handoff", "refresh-data")
	require.ErrorContains(t, err, "selected handoff policy declares no data")
	require.Empty(t, out)

	t.Chdir(se.home)
	out, stderr, err := execHandler(t, "handoff", "refresh-data")
	require.Error(t, err)
	require.Contains(t, stderr, "handoff refresh-data must run inside a repo")
	require.Empty(t, out)
}

func TestRunHandoffCacheClearRequiresCheckout(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	out, _, err := execHandler(t, "handoff", "cache", "clear")
	require.Empty(t, out)
	if runtime.GOOS == "linux" {
		require.NoError(t, err)
	} else {
		require.ErrorContains(t, err, "not supported")
	}

	t.Chdir(se.home)
	out, stderr, err := execHandler(t, "handoff", "cache", "clear")
	require.Error(t, err)
	require.Contains(t, stderr, "handoff cache clear must run inside a repo")
	require.Empty(t, out)
}
