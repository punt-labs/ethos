package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolvedBinaryPath(t *testing.T) {
	p, err := resolvedBinaryPath()
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(p), "resolved path should be absolute: %s", p)
}
