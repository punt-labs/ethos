package autorepair

import (
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

// A released binary names a sealed chunk for the first and last ts inside it,
// so every sealed chunk in the corpus must verify against its own name. A
// snapshot whose chunk names and contents come from different clocks is not
// state any binary wrote, and every reader of it fails.
func TestCorpusSealedChunksMatchTheirNames(t *testing.T) {
	n := 0
	err := filepath.WalkDir(filepath.Join("testdata", "corpus"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		var ns audit.Namespace
		switch dir := filepath.Base(filepath.Dir(filepath.Dir(path))); {
		case dir == "missions":
			ns = audit.MissionNS
		case dir == "sessions":
			ns = audit.SessionNS
		default:
			return nil
		}
		name, kind := audit.Classify(d.Name(), ns)
		if kind != audit.KindValid {
			return nil
		}
		n++
		_, err = audit.ReadChunkVerified(path, name.Last)
		assert.NoError(t, err, path)
		return nil
	})
	require.NoError(t, err)
	require.NotZero(t, n, "the corpus holds sealed chunks")
}
