package hook

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/require"
)

func TestPipeInput(t *testing.T) {
	for _, payload := range []string{`{"tool_input":{"file_path":"file.go"}}`, "", "{invalid"} {
		t.Run(payload, func(t *testing.T) {
			var out bytes.Buffer
			require.NoError(t, PipeInput(strings.NewReader(payload), &out))
			require.Equal(t, payload, out.String())
		})
	}
	t.Run("read error", func(t *testing.T) {
		failure := errors.New("read failure")
		var out bytes.Buffer
		require.ErrorIs(t, PipeInput(iotest.ErrReader(failure), &out), failure)
		require.Empty(t, out.String())
	})
	t.Run("write error", func(t *testing.T) {
		failure := errors.New("write failure")
		require.ErrorIs(t, PipeInput(strings.NewReader("{}"), pipeInputErrorWriter{failure}), failure)
	})
}

type pipeInputErrorWriter struct{ err error }

func (w pipeInputErrorWriter) Write([]byte) (int, error) { return 0, w.err }
