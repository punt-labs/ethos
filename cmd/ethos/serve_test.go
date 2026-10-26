package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServeOpenCodeSessionMetadataFlag(t *testing.T) {
	flag := serveCmd.Flags().Lookup("opencode-session-metadata")
	require.NotNil(t, flag)
	assert.Equal(t, "false", flag.DefValue)
	assert.Contains(t, flag.Usage, "not authentication")
	original := flag.Value.String()
	t.Cleanup(func() { require.NoError(t, flag.Value.Set(original)) })
	for _, value := range []string{"false", "true"} {
		require.NoError(t, flag.Value.Set(value))
		enabled, err := serveCmd.Flags().GetBool("opencode-session-metadata")
		require.NoError(t, err)
		assert.Equal(t, value == "true", enabled)
	}
}
