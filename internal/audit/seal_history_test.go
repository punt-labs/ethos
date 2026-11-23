package audit

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

type historyBudget struct {
	context.Context
	rows int
}

func (b *historyBudget) Err() error {
	if b.rows == 0 {
		return context.DeadlineExceeded
	}
	b.rows--
	return b.Context.Err()
}

func TestReadStreamForSealResumesAndInvalidatesChangedBytes(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordFull)
	s := Stream{Namespace: SessionNS, Session: "s"}
	dir, err := ArchiveDir(p, s, "2026-11-04")
	require.NoError(t, err)
	path := filepath.Join(dir, SessionChunkFile(100, 200))
	old := liveLine(100, "Read") + "\n" + liveLine(200, "Read")
	require.NoError(t, WriteLocalAtomic(p, path, []byte(old)))
	require.NoError(t, ReplaceLocalFile(p, s.LivePath(p), []byte(old+liveLine(300, "Read"))))

	check := func(rows int, persist bool) (StreamData, error) {
		return ReadStreamForSeal(&historyBudget{Context: context.Background(), rows: rows}, p, s, dir, persist)
	}
	_, err = check(2, false)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	_, err = check(2, true)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	data, err := check(2, true)
	require.NoError(t, err)
	require.Len(t, data.Lines, 1)
	require.Equal(t, int64(300), data.Lines[0].TS)
	_, err = check(1, true)
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	changed := liveLine(100, "Edit") + "\n" + liveLine(200, "Read")
	require.Len(t, changed, len(old))
	require.NoError(t, os.WriteFile(path, []byte(changed), 0o600))
	require.NoError(t, os.Chtimes(path, info.ModTime(), info.ModTime()))
	_, err = check(1, true)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	_, err = check(3, true)
	require.ErrorContains(t, err, "audit identity conflict")
}

func TestVerifyHistoryChunkRefusesInvalidEvidence(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	path := filepath.Join(t.TempDir(), "chunk.jsonl")
	cache := filepath.Join(p.LocalRoot(), "cursors", "history.json")
	verify := func() error {
		_, _, err := verifyHistoryChunk(context.Background(), p, path, 100, cache, true)
		return err
	}
	require.ErrorIs(t, verify(), os.ErrNotExist)
	for _, tc := range []struct {
		body string
		want string
	}{
		{"", "empty"},
		{"\n\n", "empty"},
		{"{", "missing final newline"},
		{"{\n", "line 1"},
		{liveLine(200, "Read"), "filename <last>"},
		{`{"ts":"` + FormatLineTS(100) + `","tool":"Read","tool":"Read"}` + "\n", "duplicate"},
	} {
		require.NoError(t, os.WriteFile(path, []byte(tc.body), 0o600))
		require.ErrorContains(t, verify(), tc.want)
	}

	valid := []byte(liveLine(100, "Read"))
	require.NoError(t, os.WriteFile(path, valid, 0o600))
	require.NoError(t, writeHistoryCheckpoint(p, cache, verifiedHistory{
		Content: digest(valid),
		Rows:    []verifiedHistoryLine{{Start: 0, End: len(valid), TS: 100}},
	}))
	require.NoError(t, verify(), "invalid cache offsets do not hide evidence or make valid history fail")
	require.NoError(t, os.WriteFile(cache, []byte("damaged checkpoint"), 0o600))
	require.NoError(t, verify(), "invalid checkpoint bytes are rebuilt")
}

func TestReadStreamForSealChecksDeadlineBetweenCachedChunks(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordFull)
	s := Stream{Namespace: SessionNS, Session: "s"}
	dir, err := ArchiveDir(p, s, "2026-11-04")
	require.NoError(t, err)
	for _, ts := range []int64{100, 200} {
		path := filepath.Join(dir, SessionChunkFile(ts, ts))
		require.NoError(t, WriteLocalAtomic(p, path, []byte(liveLine(ts, "Read"))))
	}
	_, err = ReadStreamForSeal(context.Background(), p, s, dir, true)
	require.NoError(t, err)
	ctx := &historyBudget{Context: context.Background(), rows: 1}
	_, err = ReadStreamForSeal(ctx, p, s, dir, true)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
