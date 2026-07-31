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

	n, err := MissionUnsealedCount(repoRoot, repoRoot, missionID, sess)
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

// TestMissionIsWhollyLegacyUnreadableDirDoesNotVouch pins the fail-safe
// direction on an I/O failure: an unreadable sealed dir leaves the chunk state
// unknown, and unknown must not be read as "no chunks" — that would let the
// frozen legacy sources vouch for a mission whose lines may well be lost.
func TestMissionIsWhollyLegacyUnreadableDirDoesNotVouch(t *testing.T) {
	repo := t.TempDir()
	mid := "m-2026-07-20-030"
	dir := SealedMissionDir(repo, mid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"ts":"` + FormatLineTS(100) + `","event":"dispatch"}` + "\n"
	if err := os.WriteFile(MissionLegacyLogPath(repo, mid), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	// Readable: a frozen log and no chunk is a wholly pre-split mission.
	got, err := missionIsWhollyLegacy(repo, repo, mid)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("a frozen log with no chunk must read as wholly legacy")
	}

	// Search-but-not-list (mode 0o111) is the case that isolates the chunk
	// scan: ReadDir fails with EACCES while opening a file by name still
	// succeeds, so the legacy log stays readable and only the chunk state
	// becomes unknown. Mode 0o000 would break both and prove nothing.
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory modes do not deny access")
	}
	if err := os.Chmod(dir, 0o111); err != nil {
		t.Skipf("cannot restrict directory permissions here: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := os.ReadFile(MissionLegacyLogPath(repo, mid)); err != nil {
		t.Skipf("this filesystem denies open-by-name under mode 0o111: %v", err)
	}

	got, err = missionIsWhollyLegacy(repo, repo, mid)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("an unlistable sealed dir vouched for the mission; unknown must not read as no-chunks")
	}
}

// TestWriterStateUnreadableZoneReadsAsNoEvidence pins the ReadDir-error rule
// and, more importantly, the reason it is allowed to differ from
// missionHasAnyChunk's error-means-maybe.
//
// An unreadable live-missions tree yields "no evidence", which suppresses. That
// would be fail-unsafe if it were reachable, and it is not: the seal walks the
// same tree and fails the whole run with exit 2 before the vacuum executes
// (docs/audit-seal.md §Seal failure policy), and on the purge path
// SessionUnsealedCountAcross errors first and sets probeFailed. The suppression
// is unreachable rather than silent. Anyone tempted to "fix" this to true
// should read that argument first — hence the test that states it.
func TestWriterStateUnreadableZoneReadsAsNoEvidence(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory modes do not deny access")
	}
	const sess = "sess-unreadable"
	root := t.TempDir()
	logPath := LiveMissionLogPath(root, "m-2026-07-21-001", sess)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte(`{"ts":"`+FormatLineTS(100)+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, wrote := writerState(root, sess); !wrote {
		t.Fatal("readable zone with a live log must read as wrote-here")
	}

	if err := os.Chmod(LiveMissionsDir(root), 0o000); err != nil {
		t.Skipf("cannot revoke directory permissions here: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(LiveMissionsDir(root), 0o700) })
	if _, err := os.ReadDir(LiveMissionsDir(root)); err == nil {
		t.Skip("this filesystem still permits the read under mode 0o000")
	}

	present, wrote := writerState(root, sess)
	if !present {
		t.Error("the checkout itself is readable and must read as present")
	}
	if wrote {
		t.Error("an unreadable zone must yield no evidence, not manufactured evidence")
	}
}

// TestWriterStateIgnoresSealCreatedLocks closes the gap that let a
// self-defeating discriminator pass the whole suite. WriterZone answers "did
// this checkout write live mission logs?", and the seal itself MkdirAlls the
// live-missions tree and O_CREATEs <session>.lock for every mission it
// enumerates — including ones it finds only in the TRACKED tree. Keying on the
// directory therefore manufactured the evidence, and because the seal runs
// before the vacuum in the same invocation the probe was poisoned on the very
// first run.
//
// The existing tests all build the filesystem directly and never run a seal, so
// none of them could see it. This one plants exactly what a seal leaves behind.
func TestWriterStateIgnoresSealCreatedLocks(t *testing.T) {
	const sess = "sess-lock"
	root := t.TempDir()

	// Exactly the residue of `ethos audit seal` in a checkout that never wrote:
	// a per-mission directory holding only the flock.
	for _, mid := range []string{"m-2026-07-21-001", "m-2026-07-21-002"} {
		lock := LiveMissionLockPath(root, mid, sess)
		if err := os.MkdirAll(filepath.Dir(lock), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(lock, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	present, wrote := writerState(root, sess)
	if !present {
		t.Error("the checkout exists and must read as present")
	}
	if wrote {
		t.Error("seal-created locks counted as evidence the checkout wrote live logs")
	}

	// One real live log — which only the live writer creates — flips it.
	logPath := LiveMissionLogPath(root, "m-2026-07-21-002", sess)
	if err := os.WriteFile(logPath, []byte(`{"ts":"`+FormatLineTS(100)+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, wrote = writerState(root, sess); !wrote {
		t.Error("a live mission log must count as evidence the checkout wrote here")
	}

	// Another session's live log says nothing about this one.
	if _, wrote = writerState(root, "other-sess"); wrote {
		t.Error("another session's live log counted as this session's evidence")
	}
}
