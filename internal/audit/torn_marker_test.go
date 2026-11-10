package audit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// A torn marker may have recorded a loss; its true range is unknown, so no
// write replaces it. Both marker writers refuse and leave the bytes alone.
func TestWriteMarkerRefusesATornMarker(t *testing.T) {
	torn := []byte(`{"chunk":"audit-x","verified_last":30,"unrecovered_first":10,"unrecovered_last":90,"rea`)
	next := Marker{Chunk: "audit-x", VerifiedLast: 30, Reason: "clean"}

	t.Run("writeMarker", func(t *testing.T) {
		p := repositoryPolicy(t, layout.RecordBlame)
		path := filepath.Join(p.LocalRoot(), "trace", "sessions", "d", "audit-x.quarantine")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, torn, 0o600))
		err := writeMarker(p, path, next, false)
		require.ErrorIs(t, err, ErrTornMarker)
		got, rerr := os.ReadFile(path)
		require.NoError(t, rerr)
		assert.Equal(t, torn, got, "the damaged marker is byte-identical")
	})
	t.Run("writeMarkerAtomic", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "audit-x.quarantine")
		require.NoError(t, os.WriteFile(path, torn, 0o600))
		err := writeMarkerAtomic(dir, "audit-x.quarantine", next)
		require.ErrorIs(t, err, ErrTornMarker)
		got, rerr := os.ReadFile(path)
		require.NoError(t, rerr)
		assert.Equal(t, torn, got, "the damaged marker is byte-identical")
	})
}

// A marker at the stem's name that covers another chunk is a typed error.
func TestWriteMarkerOtherChunkIsTyped(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	path := filepath.Join(p.LocalRoot(), "trace", "sessions", "d", "audit-x.quarantine")
	require.NoError(t, writeMarker(p, path, Marker{Chunk: "audit-x", VerifiedLast: 1}, false))
	require.ErrorIs(t, writeMarker(p, path, Marker{Chunk: "audit-y"}, false), ErrMarkerCoversOtherChunk)
}
