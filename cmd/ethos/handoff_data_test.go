//go:build linux || darwin

package main

import (
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
