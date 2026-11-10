package autorepair

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

// Every record kind the audit package writes in place of retired bytes is a
// pointer to evidence, never raw bytes: legacyCorrupts must skip each one. The
// table is built from the package's own constructors.
func TestLegacyCorruptsSkipsEveryRecordKind(t *testing.T) {
	sum := strings.Repeat("ef", 32)
	local, err := audit.NewLocalRetirementRecord(sum, 42, fixT0+2000)
	require.NoError(t, err)
	withheld, err := audit.NewWithheldRecord(sum, 42, fixT0+2000)
	require.NoError(t, err)
	withheldNoTS, err := audit.NewWithheldRecord(sum, 42, -1)
	require.NoError(t, err)
	kinds := map[string][]byte{"local retirement": local, "withheld": withheld, "withheld without ts": withheldNoTS}
	for name, rec := range kinds {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			cn := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0, Last: fixT0 + 2000}
			require.NoError(t, os.WriteFile(filepath.Join(dir, audit.CorruptName(cn.Stem())), rec, 0o600))
			got, err := legacyCorrupts(dir, audit.Stream{Namespace: audit.SessionNS, Session: damageSession})
			require.NoError(t, err)
			assert.Empty(t, got, "a record is not raw bytes")
		})
	}
	// Raw bytes still are.
	dir := t.TempDir()
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0, Last: fixT0 + 2000}
	require.NoError(t, os.WriteFile(filepath.Join(dir, audit.CorruptName(cn.Stem())), []byte(stableLine(fixT0, "A")), 0o600))
	got, err := legacyCorrupts(dir, audit.Stream{Namespace: audit.SessionNS, Session: damageSession})
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

// A tracked-tree withheld record under a covering marker is the only pointer to
// the original; the step must leave it alone.
func TestWithheldRecordUnderMarkerIsKept(t *testing.T) {
	env, _, _, _, _ := fixEnv(t)
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0, Last: fixT0 + 2000}
	pub := filepath.Join(env.Policy.RecordRoot(), "sessions", "2026-11-04-"+damageSession)
	require.NoError(t, os.MkdirAll(pub, 0o755))
	rec, err := audit.NewWithheldRecord(strings.Repeat("ef", 32), 4242, fixT0+2000)
	require.NoError(t, err)
	require.True(t, audit.IsWithheldRecord(rec))
	mb, err := audit.MarshalMarker(audit.Marker{Chunk: cn.Stem(), VerifiedLast: fixT0 + 2000, UnrecoveredFirst: fixT0, UnrecoveredLast: fixT0 + 2000, Reason: "tracked quarantine"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(pub, cn.MarkerFile()), mb, 0o644))
	cpath := filepath.Join(pub, audit.CorruptName(cn.Stem()))
	require.NoError(t, os.WriteFile(cpath, rec, 0o644))

	out := runStepT(t, runCorruptChunks, env)
	assert.Zero(t, out.Items, "nothing is repaired")
	got, err := os.ReadFile(cpath)
	require.NoError(t, err, "the withheld record stays")
	assert.Equal(t, rec, got)
}
