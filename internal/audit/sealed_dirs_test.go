package audit

import (
	"os"
	"path/filepath"
	"testing"
)

func writeLiveLines(t *testing.T, path string, tss ...int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	var body []byte
	for _, ts := range tss {
		body = append(body, []byte(`{"ts":"`+FormatLineTS(ts)+`","event":"update","actor":"a"}`+"\n")...)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSessionUnsealedCountReadsTraceAndLegacy: sealed chunks under the local
// trace archive (where seal writes since DES-079) and under the legacy tracked
// tree both lower the unsealed count.
func TestSessionUnsealedCountReadsTraceAndLegacy(t *testing.T) {
	tests := []struct {
		name  string
		chunk func(root string) string
	}{
		{"trace", func(root string) string {
			return filepath.Join(LocalZoneBase(root), "trace", "sessions", "2026-07-21-s1")
		}},
		{"legacy tracked", func(root string) string {
			return filepath.Join(SealedSessionsBase(root), "2026-07-21-s1")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeLiveLines(t, LiveAuditPath(root, "s1"), 100, 200)
			n, err := SessionUnsealedCount(root, "s1")
			if err != nil || n != 2 {
				t.Fatalf("before seal: n=%d err=%v, want 2", n, err)
			}
			writeChunk(t, tc.chunk(root), SessionChunkFile(100, 200), 100, 200)
			n, err = SessionUnsealedCount(root, "s1")
			if err != nil || n != 0 {
				t.Fatalf("after seal: n=%d err=%v, want 0", n, err)
			}
		})
	}
}

// TestMissionSealedStateReadsTrace: the mission probes read chunks the seal
// wrote under trace/missions, not only the legacy tracked directory.
func TestMissionSealedStateReadsTrace(t *testing.T) {
	root := t.TempDir()
	mid, sess := "m-2026-07-21-001", "s1"
	writeLiveLines(t, LiveMissionLogPath(root, mid, sess), 100, 200)
	writeChunk(t, filepath.Join(LocalZoneBase(root), "trace", "missions", mid),
		MissionChunkFile(sess, 100, 100), 100)

	n, err := MissionUnsealedCount(root, root, mid, sess)
	if err != nil || n != 1 {
		t.Fatalf("MissionUnsealedCount = %d, %v; want 1 (ts 200 is past the chunk)", n, err)
	}
	got, err := ExpectedMissionLiveFiles(root, AssumedWriter(root), sess, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MissionID != mid || !got[0].Sealed {
		t.Fatalf("ExpectedMissionLiveFiles = %+v, want the trace-sealed mission, Sealed", got)
	}
}
