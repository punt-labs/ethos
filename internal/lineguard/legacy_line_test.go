package lineguard

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/mission"
)

// RedactLegacyLine redacts with the redactor its caller built. It must not
// build a second one, whose failure would write to stderr from a path (the
// repair steps) that never prints.
func TestRedactLegacyLineWritesNothingToStderr(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	r, err := mission.NewPathRedactor(t.TempDir())
	require.NoError(t, err)
	// A home the second redactor would reject; the one in hand is fine.
	t.Setenv("HOME", "/")
	t.Setenv("USERPROFILE", "/")

	rd, wr, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stderr
	os.Stderr = wr
	line := []byte(`{"tool":"Bash","tool_input":{"command":"echo hi"}}`)
	_, _, rerr := RedactLegacyLine(line, t.TempDir(), r)
	os.Stderr = old
	require.NoError(t, wr.Close())
	got, err := io.ReadAll(rd)
	require.NoError(t, err)

	require.NoError(t, rerr)
	assert.Empty(t, string(got))
}
