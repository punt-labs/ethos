package audit

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func passGuard(raw []byte) ([]byte, string, bool) { return raw, "", false }

func sessLine(ts int64, tool string) string {
	return `{"ts":"` + FormatLineTS(ts) + `","tool":"` + tool + `"}` + "\n"
}

// HIGH-2: QuarantineLocal keeps the decodable lines of the damaged bytes, as
// SalvageChunk does, instead of recovering only from the live file.
func TestQuarantineLocalKeepsDecodableDamagedLines(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "s"}
	dir, err := ArchiveDir(p, s, "2026-11-04")
	require.NoError(t, err)
	cn := ChunkName{Namespace: SessionNS, First: 100, Last: 300}
	source := filepath.Join(dir, cn.ChunkFile())
	require.NoError(t, WriteLocalAtomic(p, source, []byte(sessLine(100, "Secret")+"garbage\n"+sessLine(300, "Read"))))

	m, err := QuarantineLocal(p, s, source, dir, cn, "fixture", maskGuard)
	require.NoError(t, err)
	assert.True(t, m.HasGap(), "the undecodable interior line is a recorded loss even though both bounds are recovered")
	assert.Equal(t, int64(300), m.VerifiedLast)
	data, err := ReadStream(p, s)
	require.NoError(t, err)
	require.Len(t, data.Lines, 2, "both decodable lines of the damaged bytes survive")
	assert.Contains(t, string(data.Lines[0].Raw), `"tool":"Masked"`, "the recovered line is redacted")
	files, err := filepath.Glob(filepath.Join(p.LocalRoot(), "quarantine", "*", "*"))
	require.NoError(t, err)
	assert.Empty(t, files, "the raw bytes are deleted once the redacted copy is installed")
}

// HIGH-3: resuming with the raw bytes gone and no replacement installed records
// a gap for the retired range; it never writes a clean completion. The live
// file holds the bounds, so only the retirement record knows an interior line
// existed, and that content is gone.
func TestQuarantineLocalResumeWithoutRawRecordsGap(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "s"}
	dir, err := ArchiveDir(p, s, "2026-11-04")
	require.NoError(t, err)
	cn := ChunkName{Namespace: SessionNS, First: 100, Last: 300}
	source := filepath.Join(dir, cn.ChunkFile())
	require.NoError(t, WriteLocalAtomic(p, s.LivePath(p), []byte(sessLine(100, "A")+sessLine(300, "C"))))
	require.NoError(t, WriteLocalAtomic(p, source, []byte(sessLine(200, "B")+"garbage\n")))
	_, err = QuarantineLocal(p, s, source, dir, cn, "fixture", passGuard)
	require.NoError(t, err)
	// Simulate an old binary's interrupted run: marker lost, replacement lost,
	// raw bytes already gone; only the retirement record remains.
	require.NoError(t, os.Remove(filepath.Join(dir, cn.MarkerFile())))
	for _, f := range chunkPaths(t, dir) {
		require.NoError(t, os.Remove(f))
	}
	m, err := QuarantineLocal(p, s, source, dir, cn, "fixture", passGuard)
	require.NoError(t, err)
	assert.True(t, m.HasGap(), "content-only loss is recorded, not a clean completion")
	assert.Equal(t, int64(100), m.UnrecoveredFirst)
	assert.Equal(t, int64(300), m.UnrecoveredLast)
	data, err := ReadStream(p, s)
	require.NoError(t, err)
	assert.Len(t, data.Gaps, 1)
}

func chunkPaths(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "audit-*.jsonl"))
	require.NoError(t, err)
	return m
}

// MEDIUM-7: a salvage interrupted between installing the replacement and
// removing the source resumes without reporting its own replacement as a
// conflict or a loss.
func TestSalvageChunkResumeAfterInstallIsNotALoss(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "s"}
	dir, err := ArchiveDir(p, s, "2026-11-04")
	require.NoError(t, err)
	cn := ChunkName{Namespace: SessionNS, First: 100, Last: 200}
	source := filepath.Join(dir, cn.ChunkFile())
	raw := sessLine(100, "A") + sessLine(200, "B") + sessLine(300, "C") // last ts disagrees with the name
	require.NoError(t, WriteLocalAtomic(p, source, []byte(raw)))
	res, err := SalvageChunk(p, s, source, cn, passGuard)
	require.NoError(t, err)
	assert.False(t, res.Lost, "every line decoded; nothing was lost")
	// The crash window: the source reappears after the replacement is installed.
	require.NoError(t, WriteLocalAtomic(p, source, []byte(raw)))
	res, err = SalvageChunk(p, s, source, cn, passGuard)
	require.NoError(t, err)
	assert.False(t, res.Lost, "the installed replacement is this chunk's own content, not conflicting evidence")
	assert.Empty(t, res.Conflicts)
	_, err = os.Stat(filepath.Join(dir, cn.MarkerFile()))
	assert.True(t, os.IsNotExist(err), "no false gap marker")
	data, err := ReadStream(p, s)
	require.NoError(t, err)
	assert.Len(t, data.Lines, 3)
	assert.Empty(t, data.Gaps)
}

// MEDIUM-9: a damaged chunk in the record tree gets a shared availability
// marker at its published location before the bytes are removed.
func TestSalvageChunkRecordTreeWritesSharedMarker(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "s"}
	rec := filepath.Join(p.RecordRoot(), "sessions", "2026-11-04-s")
	require.NoError(t, os.MkdirAll(rec, 0o755))
	cn := ChunkName{Namespace: SessionNS, First: 100, Last: 200}
	source := filepath.Join(rec, cn.ChunkFile())
	require.NoError(t, os.WriteFile(source, []byte(sessLine(100, "A")+"garbage\n"), 0o644))
	res, err := SalvageChunk(p, s, source, cn, passGuard)
	require.NoError(t, err)
	assert.True(t, res.Replaced)
	_, err = os.Stat(source)
	assert.True(t, os.IsNotExist(err), "the damaged published bytes are removed")
	shared, err := ReadMarker(filepath.Join(rec, cn.MarkerFile()))
	require.NoError(t, err, "a shared marker is written beside the removed chunk")
	assert.True(t, shared.HasGap(), "the published copy no longer offers the range")
	assert.Empty(t, shared.RetiredSHA256, "a published marker carries no local retirement pointer")
	local, err := ArchiveDir(p, s, "2026-11-04")
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(local, ChunkName{Namespace: SessionNS, First: 100, Last: 100}.ChunkFile()))
	assert.NoError(t, err, "the recovered line lands in the local archive")
	data, err := ReadStream(p, s)
	require.NoError(t, err)
	assert.Len(t, data.Lines, 1)
}

// LOW-12: a live line whose redacted form equals the archived payload is
// already archived, not a conflict.
func TestRedactLiveRedactedDuplicateIsNotAConflict(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "s"}
	dir, err := ArchiveDir(p, s, "2026-11-04")
	require.NoError(t, err)
	cn := ChunkName{Namespace: SessionNS, First: 100, Last: 100}
	require.NoError(t, WriteLocalAtomic(p, filepath.Join(dir, cn.ChunkFile()), []byte(sessLine(100, "Masked"))))
	require.NoError(t, ReplaceLocalFile(p, s.LivePath(p), []byte(sessLine(100, "Secret"))))
	res, err := RedactLive(context.Background(), p, s, maskGuard, 0)
	require.NoError(t, err)
	assert.Empty(t, res.Dropped, "not a conflict: its redacted form is what the archive holds")
	assert.Equal(t, 1, res.Rewritten, "the live copy is brought to the archived form")
	_, err = ReadStream(p, s)
	require.NoError(t, err)
}

// LOW-13: a line that survives identically in another chunk is not a loss.
func TestSalvageChunkLineHeldElsewhereIsNotLost(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "s"}
	dir, err := ArchiveDir(p, s, "2026-11-04")
	require.NoError(t, err)
	other := ChunkName{Namespace: SessionNS, First: 100, Last: 100}
	require.NoError(t, WriteLocalAtomic(p, filepath.Join(dir, other.ChunkFile()), []byte(sessLine(100, "A"))))
	cn := ChunkName{Namespace: SessionNS, First: 100, Last: 200}
	source := filepath.Join(dir, cn.ChunkFile())
	// Decodes whole, but its last ts disagrees with the name, so the chunk is
	// damaged while every line is intact.
	require.NoError(t, WriteLocalAtomic(p, source, []byte(sessLine(100, "A")+sessLine(200, "B")+sessLine(150, "C"))))
	res, err := SalvageChunk(p, s, source, cn, passGuard)
	require.NoError(t, err)
	assert.False(t, res.Lost, "ts 100 is held identically elsewhere; nothing was lost")
	assert.Equal(t, 3, res.Salvaged)
	data, err := ReadStream(p, s)
	require.NoError(t, err)
	assert.Len(t, data.Lines, 3)
	assert.Empty(t, data.Gaps)
}

// LOW-14: the replacement is read back before the source is removed; a
// replacement that fails verification leaves the source in place.
func TestSalvageChunkVerifiesReplacementBeforeRemoval(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "s"}
	dir, err := ArchiveDir(p, s, "2026-11-04")
	require.NoError(t, err)
	cn := ChunkName{Namespace: SessionNS, First: 100, Last: 200}
	source := filepath.Join(dir, cn.ChunkFile())
	require.NoError(t, WriteLocalAtomic(p, source, []byte(sessLine(100, "A")+"garbage\n")))
	// A guard whose output decodes but whose ts no longer parses makes the
	// replacement fail ReadChunkVerified.
	bad := func(raw []byte) ([]byte, string, bool) {
		return []byte(`{"ts":"not-a-ts","tool":"A"}`), "", false
	}
	_, err = SalvageChunk(p, s, source, cn, bad)
	require.Error(t, err)
	_, statErr := os.Stat(source)
	assert.NoError(t, statErr, "the damaged source stays until a verified replacement exists")
}
