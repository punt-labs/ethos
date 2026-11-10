package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// A same-name replacement overwrites the damaged source before the gap marker
// exists. Make the marker write fail to stand in for a crash between the two.
func TestSalvageInPlaceReplaceKeepsRawUntilMarker(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "s"}
	dir, err := ArchiveDir(p, s, "2026-11-04")
	require.NoError(t, err)
	other := ChunkName{Namespace: SessionNS, First: 200, Last: 200}
	require.NoError(t, WriteLocalAtomic(p, filepath.Join(dir, other.ChunkFile()), []byte(sessLine(200, "Survivor"))))
	cn := ChunkName{Namespace: SessionNS, First: 100, Last: 300}
	source := filepath.Join(dir, cn.ChunkFile())
	require.NoError(t, WriteLocalAtomic(p, source, []byte(sessLine(100, "A")+sessLine(200, "Yconflict")+"garbage\n"+sessLine(300, "C"))))
	require.NoError(t, os.Mkdir(filepath.Join(dir, cn.MarkerFile()), 0o700))
	_, err = SalvageChunk(p, s, source, cn, passGuard)
	t.Logf("salvage err: %v", err)
	require.Error(t, err)
	b, rerr := os.ReadFile(source)
	require.NoError(t, rerr)
	t.Logf("source after failed marker write: %q", string(b))
	assert.True(t, strings.Contains(string(b), "Yconflict") || strings.Contains(string(b), "garbage"),
		"raw bytes must survive until the gap marker exists")
	require.NoError(t, os.Remove(filepath.Join(dir, cn.MarkerFile())))
	res, err := SalvageChunk(p, s, source, cn, passGuard)
	require.NoError(t, err)
	t.Logf("rerun: %+v", res)
	data, err := ReadStream(p, s)
	require.NoError(t, err)
	t.Logf("gaps=%v", data.Gaps)
	assert.NotEmpty(t, data.Gaps, "the conflict loss is recorded somewhere")
}
