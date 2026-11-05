//go:build unix

package hook

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stalledPayload is a JSON object whose writer pauses mid-payload, as Claude
// Code does while serializing a large Edit.
func stalledPayload(w *os.File, stall time.Duration) {
	body := `{"tool_name":"Edit","session_id":"s","new_string":"` + strings.Repeat("x", 100*1024) + `"}` + "\n"
	go func() {
		w.WriteString(body[:8192]) //nolint:errcheck // a short write shows up as a short read
		time.Sleep(stall)
		w.WriteString(body[8192:]) //nolint:errcheck // as above
		// The write end stays open: no EOF, as with Claude Code.
	}()
}

// A pause mid-payload longer than the old inter-chunk gap must not truncate:
// the whole value arrives within the overall bound.
func TestReadInput_StallMidPayload(t *testing.T) {
	t.Run("pollable pipe", func(t *testing.T) {
		r, w, err := os.Pipe()
		require.NoError(t, err)
		defer r.Close()
		defer w.Close()
		stalledPayload(w, 1300*time.Millisecond)
		data, err := ReadInput(r, 5*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "Edit", data["tool_name"])
	})
	t.Run("blocking inherited fd", func(t *testing.T) {
		var fds [2]int
		require.NoError(t, syscall.Pipe(fds[:]))
		r := os.NewFile(uintptr(fds[0]), "r")
		w := os.NewFile(uintptr(fds[1]), "w")
		defer r.Close()
		defer w.Close()
		stalledPayload(w, 1300*time.Millisecond)
		data, err := ReadInput(r, 5*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "Edit", data["tool_name"])
	})
}

// The bound a hook gives its stdin is long enough for a busy writer.
func TestHookInputTimeoutOutlastsAStall(t *testing.T) {
	assert.GreaterOrEqual(t, hookInputTimeout, 5*time.Second)
}
