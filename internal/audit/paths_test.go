package audit

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMissionUnsealedCountIgnoresLegacyGrowth is B4(a): a session's live line
// whose ts sits below a later-grown shared legacy log.jsonl max must still
// count as unsealed. The tail-selection watermark is the session's own sealed
// chunks (Watermark), never the legacy max — folding legacy in would strand the
// line from the seal forever. Pre-fix this returned 0 (stranded); it must be 1.
func TestMissionUnsealedCountIgnoresLegacyGrowth(t *testing.T) {
	repoRoot := t.TempDir()
	missionID := "m-2026-07-21-001"
	sess := "sessA"

	// sessA wrote one live line at ts=100, before the shared legacy grew.
	livePath := LiveMissionLogPath(repoRoot, missionID, sess)
	if err := os.MkdirAll(filepath.Dir(livePath), 0o700); err != nil {
		t.Fatal(err)
	}
	line := []byte(`{"ts":"` + FormatLineTS(100) + `","event":"update","actor":"a"}` + "\n")
	if err := os.WriteFile(livePath, line, 0o600); err != nil {
		t.Fatal(err)
	}

	// A shared legacy log.jsonl later grew to ts=500 (a sessionless append,
	// pre-fix) — well above sessA's live line.
	sealedDir := SealedMissionDir(repoRoot, missionID)
	if err := os.MkdirAll(sealedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"ts":"` + FormatLineTS(500) + `","event":"close","actor":"b"}` + "\n")
	if err := os.WriteFile(filepath.Join(sealedDir, "log.jsonl"), legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	n, err := MissionUnsealedCount(repoRoot, missionID, sess)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("MissionUnsealedCount = %d, want 1 (legacy growth must not strand the live line)", n)
	}
}

// TestFindSealedSessionDirSuffixCollision is B2: a session id must match only
// its own <date>-<id> directory. A bare suffix match let id "abc" resolve to
// "2026-07-21-x-abc", landing one session's chunks in another's tree.
func TestFindSealedSessionDirSuffixCollision(t *testing.T) {
	repoRoot := t.TempDir()
	base := SealedSessionsBase(repoRoot)
	mkdir := func(name string) {
		if err := os.MkdirAll(filepath.Join(base, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	mkdir("2026-07-21-abc")
	mkdir("2026-07-21-x-abc")

	got, err := FindSealedSessionDir(repoRoot, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "2026-07-21-abc"); got != want {
		t.Errorf("session abc resolved to %q, want %q", got, want)
	}

	got, err = FindSealedSessionDir(repoRoot, "x-abc")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "2026-07-21-x-abc"); got != want {
		t.Errorf("session x-abc resolved to %q, want %q", got, want)
	}
}

// TestFindSealedSessionDirNoFalseMatch is B2's fall-through case: with only a
// longer-suffixed directory present, a distinct session id must NOT match — it
// falls through to the create-new path.
func TestFindSealedSessionDirNoFalseMatch(t *testing.T) {
	repoRoot := t.TempDir()
	base := SealedSessionsBase(repoRoot)
	if err := os.MkdirAll(filepath.Join(base, "2026-07-21-x-abc"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := FindSealedSessionDir(repoRoot, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("session abc matched %q, want no match", got)
	}
}

// TestFindSealedSessionDirRejectsBadDate confirms the 10-char prefix must be a
// valid date, so a non-dated directory that happens to end in the id is not a
// session dir.
func TestFindSealedSessionDirRejectsBadDate(t *testing.T) {
	repoRoot := t.TempDir()
	base := SealedSessionsBase(repoRoot)
	if err := os.MkdirAll(filepath.Join(base, "not-a-date-abc"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := FindSealedSessionDir(repoRoot, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("non-dated dir matched %q, want no match", got)
	}
}
