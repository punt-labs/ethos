//go:build linux || darwin

package mcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// team for_repo with no repo reports a git that hung reading the remote as
// an error result naming the timeout, on the same path as no remote.
func TestHandleTeam_ForRepo_GitTimeout(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	require.NoError(t, err)
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexec "+sleep+" 60\n"), 0o755))
	h := testHandlerWithTeams(t)
	t.Setenv("PATH", bin)
	testhelpers.GitTimeout(t, 200*time.Millisecond)
	t.Chdir(t.TempDir())

	result, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{"method": "for_repo"}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "could not determine repo name")
	assert.Contains(t, resultText(t, result), "did not answer")
}
