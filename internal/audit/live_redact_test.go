package audit

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func liveLine(ts int64, tool string) string {
	return `{"ts":"` + FormatLineTS(ts) + `","tool":"` + tool + `"}` + "\n"
}

// maskGuard rewrites the tool "Secret" to "Masked", standing in for the real
// hook guard that audit cannot import.
func maskGuard(raw []byte) ([]byte, string, bool) {
	return []byte(strings.ReplaceAll(string(raw), "Secret", "Masked")), "masked", false
}

func TestRedactLive(t *testing.T) {
	for _, ns := range []Namespace{SessionNS, MissionNS} {
		name := "session"
		s := Stream{Namespace: ns, Session: "s"}
		if ns == MissionNS {
			name, s.Mission = "mission", "m-2026-11-04-001"
		}
		t.Run(name, func(t *testing.T) {
			p := repositoryPolicy(t, layout.RecordBlame)
			dir, err := ArchiveDir(p, s, "2026-11-04")
			require.NoError(t, err)
			cn := ChunkName{Namespace: ns, First: 100, Last: 100}
			if ns == MissionNS {
				cn.Session = s.Session
			}
			require.NoError(t, WriteLocalAtomic(p, dir+"/"+cn.ChunkFile(), []byte(liveLine(100, "Read"))))
			torn := `{"ts":"torn`
			live := liveLine(100, "Bash") + liveLine(200, "Secret") + liveLine(300, "Read") + torn
			require.NoError(t, ReplaceLocalFile(p, s.LivePath(p), []byte(live)))

			_, err = ReadStream(p, s)
			require.ErrorContains(t, err, "identity conflict", "fixture: the unrepaired stream is refused")

			res, err := RedactLive(context.Background(), p, s, maskGuard, 0)
			require.NoError(t, err)
			assert.Equal(t, 1, res.Rewritten)
			require.Len(t, res.Dropped, 1)
			assert.Equal(t, int64(100), res.Dropped[0].TS)
			assert.NotEqual(t, res.Dropped[0].ArchiveDigest, res.Dropped[0].LiveDigest)
			assert.Len(t, res.Dropped[0].ArchiveDigest, 64)

			b, err := os.ReadFile(s.LivePath(p))
			require.NoError(t, err)
			assert.Equal(t, liveLine(200, "Masked")+liveLine(300, "Read")+torn, string(b), "torn tail and clean lines survive byte for byte")
			_, err = ReadStream(p, s)
			require.NoError(t, err)

			again, err := RedactLive(context.Background(), p, s, maskGuard, 0)
			require.NoError(t, err)
			assert.False(t, again.Changed(), "second run changes nothing")
		})
	}
}

func TestRedactLiveEqualArchivedLineStays(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "s"}
	dir, err := ArchiveDir(p, s, "2026-11-04")
	require.NoError(t, err)
	cn := ChunkName{Namespace: SessionNS, First: 100, Last: 100}
	require.NoError(t, WriteLocalAtomic(p, dir+"/"+cn.ChunkFile(), []byte(liveLine(100, "Read"))))
	require.NoError(t, ReplaceLocalFile(p, s.LivePath(p), []byte(liveLine(100, "Read"))))
	res, err := RedactLive(context.Background(), p, s, maskGuard, 0)
	require.NoError(t, err)
	assert.False(t, res.Changed())
}

func TestTryWithStreamLockBusy(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "s"}
	require.NoError(t, WithStreamLock(p, s, func() error {
		ran := false
		busy, err := TryWithStreamLock(p, s, func() error { ran = true; return nil })
		require.NoError(t, err)
		assert.True(t, busy)
		assert.False(t, ran)
		return nil
	}))
	ran := false
	busy, err := TryWithStreamLock(p, s, func() error { ran = true; return nil })
	require.NoError(t, err)
	assert.False(t, busy)
	assert.True(t, ran)
}

func secretLive(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString(liveLine(int64(i)*100, "Secret"))
	}
	return b.String()
}

// TestRedactLiveContextDoneWritesNothing: a context that ends mid-stream
// leaves the live file untouched and returns the context's error.
func TestRedactLiveContextDoneWritesNothing(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "s"}
	live := secretLive(5)
	require.NoError(t, ReplaceLocalFile(p, s.LivePath(p), []byte(live)))
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	guard := func(raw []byte) ([]byte, string, bool) {
		calls++
		if calls == 2 {
			cancel()
		}
		return maskGuard(raw)
	}
	_, err := RedactLive(ctx, p, s, guard, 0)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 2, calls, "no line is judged after the context ends")
	b, err := os.ReadFile(s.LivePath(p))
	require.NoError(t, err)
	assert.Equal(t, live, string(b), "nothing was written")
}

// TestRedactLiveChunksResume: each limited call rewrites the next lines and
// records the prefix it finished; a later call does not judge them again.
func TestRedactLiveChunksResume(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "s"}
	require.NoError(t, ReplaceLocalFile(p, s.LivePath(p), []byte(secretLive(5))))
	var judged []string
	guard := func(raw []byte) ([]byte, string, bool) {
		judged = append(judged, string(raw))
		return maskGuard(raw)
	}
	var done []bool
	for i := 0; i < 3; i++ {
		res, err := RedactLive(context.Background(), p, s, guard, 2)
		require.NoError(t, err)
		done = append(done, res.Complete)
		b, err := os.ReadFile(s.LivePath(p))
		require.NoError(t, err)
		masked := strings.Count(string(b), "Masked")
		assert.Equal(t, min(2*(i+1), 5), masked, "call %d rewrote the next chunk", i+1)
	}
	assert.Equal(t, []bool{false, false, true}, done)
	assert.Len(t, judged, 5, "every line was judged exactly once across the calls")

	judged = nil
	res, err := RedactLive(context.Background(), p, s, guard, 2)
	require.NoError(t, err)
	assert.True(t, res.Complete)
	assert.Empty(t, judged, "a finished file is not judged again")
}

// TestRedactLiveCursorIgnoredWhenPrefixChanged: a live file that no longer
// begins with the recorded prefix is judged from its start.
func TestRedactLiveCursorIgnoredWhenPrefixChanged(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "s"}
	require.NoError(t, ReplaceLocalFile(p, s.LivePath(p), []byte(secretLive(4))))
	_, err := RedactLive(context.Background(), p, s, maskGuard, 2)
	require.NoError(t, err)
	require.NoError(t, ReplaceLocalFile(p, s.LivePath(p), []byte(secretLive(4)))) // replaced by older bytes
	res, err := RedactLive(context.Background(), p, s, maskGuard, 2)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Rewritten, "the first chunk is judged again")
}
