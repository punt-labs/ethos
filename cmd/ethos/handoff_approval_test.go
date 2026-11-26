package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLiveApprovalInteraction(t *testing.T) {
	require.ErrorContains(t, checkLiveApprovalTerminal(false, nil), "interactive TTY")
	for _, marker := range []string{"CLAUDECODE=1", "OPENCODE=1", "OPENCODE_CLIENT=cli"} {
		require.ErrorContains(t, checkLiveApprovalTerminal(true, []string{marker}), "agent environment")
	}
	require.NoError(t, checkLiveApprovalTerminal(true, []string{"PATH=/usr/bin"}))
	require.Error(t, confirmLiveApproval(strings.NewReader("n\n")))
	require.Error(t, confirmLiveApproval(strings.NewReader("yes")))
	require.NoError(t, confirmLiveApproval(strings.NewReader("yes\n")))
}
