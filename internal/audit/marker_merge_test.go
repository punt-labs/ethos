package audit

import (
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func randomMarker(r *rand.Rand) Marker {
	m := Marker{Chunk: "audit-x", VerifiedLast: r.Int63n(100), Reason: "r"}
	if r.Intn(2) == 0 {
		m.UnrecoveredFirst = 1 + r.Int63n(50)
		m.UnrecoveredLast = m.UnrecoveredFirst + r.Int63n(50)
	}
	if r.Intn(2) == 0 {
		m.RetiredSHA256 = strings.Repeat("a", 64)
	}
	return m
}

// A merged marker has a gap whenever either input had one, the gap contains
// both inputs' ranges, and VerifiedLast and the digest are never dropped.
func TestMergeMarkersNeverNarrows(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 5000; i++ {
		a, b := randomMarker(r), randomMarker(r)
		m := MergeMarkers(a, b)
		for _, in := range []Marker{a, b} {
			if in.HasGap() {
				require.True(t, m.HasGap(), "a=%+v b=%+v m=%+v", a, b, m)
				require.LessOrEqual(t, m.UnrecoveredFirst, in.UnrecoveredFirst, "a=%+v b=%+v", a, b)
				require.GreaterOrEqual(t, m.UnrecoveredLast, in.UnrecoveredLast, "a=%+v b=%+v", a, b)
			}
			require.GreaterOrEqual(t, m.VerifiedLast, in.VerifiedLast)
			if in.RetiredSHA256 != "" {
				require.NotEmpty(t, m.RetiredSHA256)
			}
		}
		if !a.HasGap() && !b.HasGap() {
			require.False(t, m.HasGap())
		}
	}
}

// writeMarker merges into the marker on disk; clearRetired drops the digest.
func TestWriteMarkerMergesWithTheExistingFile(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	path := filepath.Join(p.LocalRoot(), "trace", "sessions", "d", "audit-x.quarantine")
	require.NoError(t, writeMarker(p, path, Marker{Chunk: "audit-x", VerifiedLast: 9, UnrecoveredFirst: 5, UnrecoveredLast: 50, Reason: "a", RetiredSHA256: "d"}, false))
	require.NoError(t, writeMarker(p, path, Marker{Chunk: "audit-x", VerifiedLast: 3, UnrecoveredFirst: 7, UnrecoveredLast: 20, Reason: "b"}, false))
	m, err := ReadMarker(path)
	require.NoError(t, err)
	assert.Equal(t, Marker{Chunk: "audit-x", VerifiedLast: 9, UnrecoveredFirst: 5, UnrecoveredLast: 50, Reason: "b", RetiredSHA256: "d"}, m)
	require.NoError(t, writeMarker(p, path, Marker{Chunk: "audit-x", VerifiedLast: 9, Reason: "c"}, true))
	m, err = ReadMarker(path)
	require.NoError(t, err)
	assert.Empty(t, m.RetiredSHA256)
	assert.True(t, m.HasGap())
	require.Error(t, writeMarker(p, path, Marker{Chunk: "audit-y"}, false))
	_, err = os.Stat(path)
	require.NoError(t, err)
}

func TestHeldLossNameRoundTrips(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	for _, tc := range []struct {
		ns      Namespace
		session string
	}{{SessionNS, ""}, {MissionNS, "sess-1"}} {
		cn, kind := Classify(HeldLossFile(tc.ns, tc.session, sum), tc.ns)
		assert.Equal(t, KindHeldLoss, kind)
		assert.Equal(t, tc.session, cn.Session)
	}
	_, kind := Classify("audit-held-short.heldloss", SessionNS)
	assert.Equal(t, KindNearMiss, kind)
}
