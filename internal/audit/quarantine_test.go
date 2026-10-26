package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/redact"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	env := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	for _, args := range [][]string{
		{"init", "-b", "main"}, {"config", "user.email", "t@e.com"}, {"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func gitCommitAll(t *testing.T, dir, msg string) {
	t.Helper()
	env := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", msg}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func writeLive(t *testing.T, path string, tss ...int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	var body []byte
	for _, ts := range tss {
		body = append(body, []byte(`{"ts":"`+FormatLineTS(ts)+`","tool":"Read"}`+"\n")...)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestQuarantineFullRecovery(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sealedDir := filepath.Join(repo, "sealed")
	// A committed chunk claiming [100,300], but whose bytes are corrupt: its
	// last line ts (200) disagrees with the filename <last> (300).
	writeChunk(t, sealedDir, SessionChunkFile(100, 300), 100, 200)
	gitCommitAll(t, repo, "seal")
	// The live file still holds the full range.
	live := filepath.Join(repo, "live.jsonl")
	writeLive(t, live, 100, 200, 300)

	cn, _ := Classify(SessionChunkFile(100, 300), SessionNS)
	m, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, noRule)
	if err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	if m.HasGap() {
		t.Errorf("full recovery should have no gap: %+v", m)
	}
	if m.VerifiedLast != 300 {
		t.Errorf("verified last = %d, want 300", m.VerifiedLast)
	}
	// The original chunk name is retired to .corrupt; a re-sealed content-named
	// chunk covers [100,300].
	if _, err := os.Stat(filepath.Join(sealedDir, SessionChunkFile(100, 300)+".corrupt")); err != nil {
		t.Errorf(".corrupt not present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sealedDir, SessionChunkFile(100, 300))); err != nil {
		t.Errorf("re-sealed chunk (full recovery reuses the name) missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sealedDir, cn.MarkerFile())); err != nil {
		t.Errorf("marker missing: %v", err)
	}
}

func TestQuarantinePartialRecoveryLeavesGap(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sealedDir := filepath.Join(repo, "sealed")
	// Corrupt chunk reached ts 300 (its bytes hold up to 300).
	writeChunk(t, sealedDir, SessionChunkFile(100, 300), 100, 200, 300)
	gitCommitAll(t, repo, "seal")
	// The live file only still holds [100,200] — 300 was lost.
	live := filepath.Join(repo, "live.jsonl")
	writeLive(t, live, 100, 200)

	cn, _ := Classify(SessionChunkFile(100, 300), SessionNS)
	m, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, noRule)
	if err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	if !m.HasGap() {
		t.Fatalf("partial recovery should record a gap: %+v", m)
	}
	if m.UnrecoveredFirst != 201 || m.UnrecoveredLast != 300 {
		t.Errorf("gap = [%d,%d], want [201,300]", m.UnrecoveredFirst, m.UnrecoveredLast)
	}
	if m.VerifiedLast != 300 {
		t.Errorf("verified last = %d, want 300 (corrupt bytes reached it)", m.VerifiedLast)
	}
	// The re-sealed chunk is content-named [100,200], NOT the retired [100,300].
	if _, err := os.Stat(filepath.Join(sealedDir, SessionChunkFile(100, 200))); err != nil {
		t.Errorf("content-named re-seal [100,200] missing: %v", err)
	}
}

func TestQuarantineTotalLossRecordsFullRangeGap(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sealedDir := filepath.Join(repo, "sealed")
	// The corrupt chunk claims [100,300] by its name but its bytes hold no
	// parseable timestamp at all — maximal corruption.
	if err := os.MkdirAll(sealedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	garbage := []byte("\x00not json at all\nstill garbage\n")
	if err := os.WriteFile(filepath.Join(sealedDir, SessionChunkFile(100, 300)), garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, repo, "seal")
	// The live file holds nothing in the chunk's range — total loss.
	live := filepath.Join(repo, "live.jsonl")
	writeLive(t, live, 50, 60)

	cn, _ := Classify(SessionChunkFile(100, 300), SessionNS)
	m, err := Quarantine(repo, sealedDir, cn, live, "unparseable", redact.Prose, keepLine, noRule)
	if err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	if !m.HasGap() {
		t.Fatalf("total loss must record a gap, not report full recovery: %+v", m)
	}
	if m.UnrecoveredFirst != 100 || m.UnrecoveredLast != 300 {
		t.Errorf("gap = [%d,%d], want the whole nominal range [100,300]",
			m.UnrecoveredFirst, m.UnrecoveredLast)
	}
}

func TestQuarantineIdempotent(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sealedDir := filepath.Join(repo, "sealed")
	writeChunk(t, sealedDir, SessionChunkFile(100, 300), 100, 200)
	gitCommitAll(t, repo, "seal")
	live := filepath.Join(repo, "live.jsonl")
	writeLive(t, live, 100, 200, 300)
	cn, _ := Classify(SessionChunkFile(100, 300), SessionNS)

	m1, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, noRule)
	if err != nil {
		t.Fatalf("first quarantine: %v", err)
	}
	m2, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, noRule)
	if err != nil {
		t.Fatalf("second quarantine (idempotent): %v", err)
	}
	if m1 != m2 {
		t.Errorf("idempotent quarantine differs: %+v vs %+v", m1, m2)
	}
}

func TestQuarantineResumeFromCorrupt(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sealedDir := filepath.Join(repo, "sealed")
	// Simulate a crash after retirement but before the marker: only the
	// .corrupt exists, no chunk, no marker.
	writeChunk(t, sealedDir, SessionChunkFile(100, 300)+".corrupt", 100, 200, 300)
	gitCommitAll(t, repo, "retired")
	live := filepath.Join(repo, "live.jsonl")
	writeLive(t, live, 100, 200, 300)

	cn, _ := Classify(SessionChunkFile(100, 300), SessionNS)
	m, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, noRule)
	if err != nil {
		t.Fatalf("resume quarantine: %v", err)
	}
	if m.HasGap() {
		t.Errorf("resume with full live should have no gap: %+v", m)
	}
	if _, err := os.Stat(filepath.Join(sealedDir, cn.MarkerFile())); err != nil {
		t.Errorf("resume did not write marker: %v", err)
	}
}

func TestQuarantineNeverOverwritesExistingCorrupt(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sealedDir := filepath.Join(repo, "sealed")
	// Fresh-damage state: a chunk AND an existing .corrupt (from a prior
	// event), no covering marker. Quarantine must NOT clobber the first
	// .corrupt — it retires the fresh chunk under a content-hashed name
	// (OPT-1: the never-overwrite .corrupt-<hash> sequence) and proceeds.
	writeChunk(t, sealedDir, SessionChunkFile(100, 300), 100, 200)                 // fresh corrupt chunk
	writeChunk(t, sealedDir, SessionChunkFile(100, 300)+".corrupt", 100, 200, 300) // prior evidence
	gitCommitAll(t, repo, "fresh damage")
	before, err := os.ReadFile(filepath.Join(sealedDir, SessionChunkFile(100, 300)+".corrupt"))
	if err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(repo, "live.jsonl")
	writeLive(t, live, 100, 200, 300)

	cn, _ := Classify(SessionChunkFile(100, 300), SessionNS)
	if _, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, noRule); err != nil {
		t.Fatalf("quarantine over existing .corrupt: %v", err)
	}
	// The prior .corrupt evidence survives untouched.
	after, err := os.ReadFile(filepath.Join(sealedDir, SessionChunkFile(100, 300)+".corrupt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("the prior .corrupt evidence was overwritten")
	}
	// The fresh chunk was retired under a distinct .corrupt-<hash> name.
	matches, _ := filepath.Glob(filepath.Join(sealedDir, SessionChunkFile(100, 300)+".corrupt-*"))
	if len(matches) != 1 {
		t.Errorf("fresh damage not retired under .corrupt-<hash>: %v", matches)
	}
}

func TestQuarantineNoOpRetiresFreshCorruptionAtCoveredName(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sealedDir := filepath.Join(repo, "sealed")
	live := filepath.Join(repo, "live.jsonl")
	writeLive(t, live, 100, 200, 300)
	// Complete a quarantine (marker present).
	writeChunk(t, sealedDir, SessionChunkFile(100, 300), 100, 200)
	gitCommitAll(t, repo, "seal")
	cn, _ := Classify(SessionChunkFile(100, 300), SessionNS)
	if _, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, noRule); err != nil {
		t.Fatal(err)
	}
	// The re-seal produced a content-named chunk at [100,200] (a covered name).
	// Now corrupt it: last-line ts disagrees with the filename <last>.
	reChunk := filepath.Join(sealedDir, SessionChunkFile(100, 200))
	if err := os.WriteFile(reChunk, []byte(`{"ts":"`+FormatLineTS(150)+`","tool":"Read"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A second quarantine (idempotent no-op) must NOT be blind to it: it
	// retires the fresh corruption under a .corrupt-<hash> and succeeds.
	if _, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, noRule); err != nil {
		t.Fatalf("no-op reconcile: %v", err)
	}
	// The scan is clean again — no valid chunk at [100,200] trips the check.
	if _, err := ScanSealedDir(sealedDir, SessionNS, ""); err != nil {
		t.Errorf("scan after no-op reconcile errored: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(sealedDir, SessionChunkFile(100, 200)+".corrupt-*"))
	if len(matches) < 1 {
		t.Errorf("fresh corruption at covered name not retired: %v", matches)
	}
}

func TestSealReadsQuarantinedChunkWithoutError(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sealedDir := filepath.Join(repo, "sealed")
	writeChunk(t, sealedDir, SessionChunkFile(100, 300), 100, 200)
	gitCommitAll(t, repo, "seal")
	live := filepath.Join(repo, "live.jsonl")
	writeLive(t, live, 100, 200, 300)
	cn, _ := Classify(SessionChunkFile(100, 300), SessionNS)
	if _, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, noRule); err != nil {
		t.Fatal(err)
	}
	// After quarantine, a scan no longer errors on the retired chunk (the
	// .corrupt is covered by the marker).
	if _, err := ScanSealedDir(sealedDir, SessionNS, ""); err != nil {
		t.Errorf("scan after quarantine errored: %v", err)
	}
}

// TestQuarantineMarkerReasonRedacted: the marker lands in the tracked
// sealed directory, and its reason is operator- or error-supplied text.
func TestQuarantineMarkerReasonRedacted(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sealedDir := filepath.Join(repo, "sealed")
	writeChunk(t, sealedDir, SessionChunkFile(100, 300), 100, 200)
	gitCommitAll(t, repo, "seal")
	live := filepath.Join(repo, "live.jsonl")
	writeLive(t, live, 100, 200, 300)

	cn, _ := Classify(SessionChunkFile(100, 300), SessionNS)
	m, err := Quarantine(repo, sealedDir, cn, live, "bad chunk in /home/alice/work GITHUB_TOKEN=abc mail a@b.org", redact.Prose, keepLine, noRule)
	if err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	const want = "bad chunk in ~[redacted-user]/work GITHUB_TOKEN=[redacted-secret] mail [redacted-email]"
	if m.Reason != want {
		t.Errorf("reason = %q, want %q", m.Reason, want)
	}
	onDisk, err := ReadMarker(filepath.Join(sealedDir, cn.MarkerFile()))
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.Reason != want {
		t.Errorf("on-disk reason = %q, want %q", onDisk.Reason, want)
	}
}

// keepLine is the identity guard: these tests do not exercise it.
func keepLine(l []byte) ([]byte, string) { return l, "" }

func noRule([]byte) string { return "" }

// leakyChunkBody is a corrupt chunk's bytes that name another user's home.
func leakyChunkBody() []byte {
	return []byte(`{"ts":"` + FormatLineTS(100) + `","cwd":"/home/bob/x"}` + "\n")
}

// homeCheck is the retired-bytes check of these tests: another user's home.
func homeCheck(b []byte) string {
	if bytes.Contains(b, []byte("/home/bob")) {
		return "names another user's home"
	}
	return ""
}

func gitOut(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// assertWithheld checks that the file at rel is the record of body (its hash,
// size and rule, no byte of body), that the index holds the same record and no
// byte of body, and that the original sits untouched in the local zone.
func assertWithheld(t *testing.T, repo, rel, stem string, body []byte) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(repo, rel))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	for _, want := range []string{hash, fmt.Sprint(len(body)), "names another user's home"} {
		if !bytes.Contains(got, []byte(want)) {
			t.Errorf("%s: record %q lacks %q", rel, got, want)
		}
	}
	if bytes.Contains(got, []byte("/home/bob")) {
		t.Errorf("%s: record keeps the home: %q", rel, got)
	}
	if staged := gitOut(t, repo, "show", ":"+rel); staged != string(got) {
		t.Errorf("%s: index holds %q, work tree %q", rel, staged, got)
	}
	orig, err := os.ReadFile(filepath.Join(QuarantineDir(repo), stem+"."+hash[:12]+".corrupt"))
	if err != nil {
		t.Fatalf("original not kept in the local zone: %v", err)
	}
	if !bytes.Equal(orig, body) {
		t.Errorf("local copy %q differs from the original %q", orig, body)
	}
}

func quarantineSetup(t *testing.T, commit bool) (repo, sealedDir, live, name string, cn ChunkName) {
	t.Helper()
	return quarantineSetupState(t, map[bool]string{true: "committed", false: "untracked"}[commit])
}

// quarantineSetupState builds the sealed dir with a leaking chunk in one of
// the index states: "untracked", "committed", or "staged-then-modified" (a
// clean chunk staged and never committed, then modified to leak).
func quarantineSetupState(t *testing.T, state string) (repo, sealedDir, live, name string, cn ChunkName) {
	t.Helper()
	repo = t.TempDir()
	initGitRepo(t, repo)
	sealedDir = filepath.Join(repo, "sealed")
	name = SessionChunkFile(100, 300)
	if err := os.MkdirAll(sealedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := leakyChunkBody()
	if state == "staged-then-modified" {
		body = []byte(`{"ts":"` + FormatLineTS(100) + `","cwd":"/x"}` + "\n")
	}
	if err := os.WriteFile(filepath.Join(sealedDir, name), body, 0o600); err != nil {
		t.Fatal(err)
	}
	switch state {
	case "committed":
		gitCommitAll(t, repo, "seal")
	case "staged-then-modified":
		gitOut(t, repo, "add", "--", "sealed")
		if err := os.WriteFile(filepath.Join(sealedDir, name), leakyChunkBody(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	live = filepath.Join(repo, "live.jsonl")
	writeLive(t, live, 100, 200, 300)
	cn, _ = Classify(name, SessionNS)
	return repo, sealedDir, live, name, cn
}

// TestQuarantineWithholds: a corrupt chunk that fails the check is never
// staged. The original moves to the untracked local zone, a record of its
// hash, size and rule is staged in its place, and what the index holds at the
// .corrupt name is that record, for a committed chunk (git shows it as a
// rename with modifications), for one never committed, and for one staged,
// never committed, then modified to leak (git rm --cached needs -f there).
func TestQuarantineWithholds(t *testing.T) {
	for _, state := range []string{"committed", "untracked", "staged-then-modified"} {
		t.Run(state, func(t *testing.T) {
			repo, sealedDir, live, name, cn := quarantineSetupState(t, state)
			if _, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, homeCheck); err != nil {
				t.Fatalf("Quarantine: %v", err)
			}
			assertWithheld(t, repo, filepath.Join("sealed", name+".corrupt"), cn.Stem(), leakyChunkBody())
			// The local zone is not staged.
			if got := gitOut(t, repo, "ls-files", "--cached", "--", ".punt-labs"); got != "" {
				t.Errorf("the local zone is staged: %q", got)
			}
		})
	}
}

// TestQuarantineWithholdsUnderAHash: a second event's chunk, retired beside
// the first evidence, is withheld the same way and the first is untouched.
func TestQuarantineWithholdsUnderAHash(t *testing.T) {
	repo, sealedDir, live, name, cn := quarantineSetup(t, false)
	first := filepath.Join(sealedDir, name+".corrupt")
	if err := os.WriteFile(first, []byte("earlier evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, homeCheck); err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	if got, _ := os.ReadFile(first); string(got) != "earlier evidence\n" {
		t.Errorf("the first .corrupt changed: %q", got)
	}
	sum := sha256.Sum256(leakyChunkBody())
	hashed := name + ".corrupt-" + hex.EncodeToString(sum[:])[:12]
	assertWithheld(t, repo, filepath.Join("sealed", hashed), cn.Stem(), leakyChunkBody())
}

// TestQuarantineResumeWithholdsAStagedLeak: a binary from before the withhold
// retired a leaking chunk with git mv, which stages the rename, so the leaking
// bytes sit at the .corrupt name in the work tree and the index. The next run
// withholds them before anything else is staged.
func TestQuarantineResumeWithholdsAStagedLeak(t *testing.T) {
	repo, sealedDir, live, name, cn := quarantineSetup(t, true)
	gitOut(t, repo, "mv", "--", filepath.Join("sealed", name), filepath.Join("sealed", name+".corrupt"))
	if _, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, homeCheck); err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	assertWithheld(t, repo, filepath.Join("sealed", name+".corrupt"), cn.Stem(), leakyChunkBody())
}

// TestQuarantineUnderAMarkerWithholdsALeakingCorrupt: a run that finds its
// marker already written still checks every .corrupt of the stem.
func TestQuarantineUnderAMarkerWithholdsALeakingCorrupt(t *testing.T) {
	repo, sealedDir, live, name, cn := quarantineSetup(t, false)
	if _, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, noRule); err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	// The first run kept the bytes (no rule). A stricter check now rejects them.
	if _, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, homeCheck); err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	assertWithheld(t, repo, filepath.Join("sealed", name+".corrupt"), cn.Stem(), leakyChunkBody())
}

// TestQuarantineResumeAfterAFailedAdd: an older binary staged the leaking
// bytes with git mv; a later run wrote the record into the work tree and
// crashed, or its git add failed, before the index took it. The re-run finds a
// clean work tree and a leaking index blob, and stages the record.
func TestQuarantineResumeAfterAFailedAdd(t *testing.T) {
	repo, sealedDir, live, name, cn := quarantineSetup(t, true)
	rel := filepath.Join("sealed", name+".corrupt")
	gitOut(t, repo, "mv", "--", filepath.Join("sealed", name), rel)
	body := leakyChunkBody()
	if err := writeRecord(filepath.Join(repo, rel), body, "names another user's home"); err != nil {
		t.Fatal(err)
	}
	if err := saveOriginal(repo, cn.Stem(), body); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, repo, "show", ":"+rel); got != string(body) {
		t.Fatalf("setup: the index should hold the leak, holds %q", got)
	}
	if _, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, homeCheck); err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	assertWithheld(t, repo, rel, cn.Stem(), body)
}

// TestQuarantineResumeAfterTheRecordIsWritten: a crash after the record was
// written at the .corrupt name and before the chunk was removed leaves both.
// The re-run finishes that retirement; it does not retire the same chunk a
// second time under a .corrupt-<hash> name.
func TestQuarantineResumeAfterTheRecordIsWritten(t *testing.T) {
	repo, sealedDir, live, name, cn := quarantineSetup(t, true)
	body := leakyChunkBody()
	if err := saveOriginal(repo, cn.Stem(), body); err != nil {
		t.Fatal(err)
	}
	if err := writeRecord(filepath.Join(sealedDir, name+".corrupt"), body, "names another user's home"); err != nil {
		t.Fatal(err)
	}
	if _, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, homeCheck); err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	entries, err := os.ReadDir(sealedDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".corrupt-") {
			t.Errorf("the chunk was retired twice: %s", e.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(sealedDir, name)); err == nil {
		// The re-seal may rewrite the name from the live file; it must not be the leak.
		if got, _ := os.ReadFile(filepath.Join(sealedDir, name)); bytes.Contains(got, []byte("/home/bob")) {
			t.Errorf("the leaking chunk is still in place")
		}
	}
	assertWithheld(t, repo, filepath.Join("sealed", name+".corrupt"), cn.Stem(), body)
}

// TestUntrackedOrModified_RenameLines: a chunk moved with git mv and then
// modified shows as "R old -> new" in status; it is matched on the new path.
func TestUntrackedOrModified_RenameLines(t *testing.T) {
	repo, sealedDir, _, name, _ := quarantineSetup(t, true)
	oldp, newp := filepath.Join(sealedDir, name), filepath.Join(sealedDir, name+".corrupt")
	gitOut(t, repo, "mv", "--", filepath.Join("sealed", name), filepath.Join("sealed", name+".corrupt"))
	if err := os.WriteFile(newp, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := UntrackedOrModified(repo, []string{oldp, newp})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range got {
		found = found || p == newp
	}
	if !found {
		t.Errorf("UntrackedOrModified(%v) = %v, want the renamed path included", []string{oldp, newp}, got)
	}
}

// captureNotices runs fn with os.Stderr captured and returns what it wrote.
func captureNotices(t *testing.T, fn func()) string {
	t.Helper()
	return testhelpers.CaptureStderr(t, fn)
}

// TestQuarantineRestageNoticeFollowsTheAdd: with the index locked the add
// fails, so the run errors and prints no withheld notice; the re-run, with the
// lock gone, stages the record and prints one.
func TestQuarantineRestageNoticeFollowsTheAdd(t *testing.T) {
	repo, sealedDir, live, name, cn := quarantineSetup(t, true)
	rel := filepath.Join("sealed", name+".corrupt")
	gitOut(t, repo, "mv", "--", filepath.Join("sealed", name), rel)
	body := leakyChunkBody()
	if err := writeRecord(filepath.Join(repo, rel), body, "names another user's home"); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(repo, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	held := captureNotices(t, func() {
		_, err = Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, homeCheck)
	})
	if err == nil {
		t.Fatal("Quarantine succeeded with the index locked")
	}
	if strings.Contains(held, "was withheld") {
		t.Errorf("a failed add printed a withheld notice: %q", held)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	again := captureNotices(t, func() {
		_, err = Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, homeCheck)
	})
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if n := strings.Count(again, "was withheld"); n != 1 {
		t.Errorf("re-run printed %d withheld notices: %q", n, again)
	}
	assertWithheld(t, repo, rel, cn.Stem(), body)
}

// TestQuarantineUnderAMarkerFinishesARecordedRetirement: a marker is present,
// a covered chunk has gone bad, and a crash left its record at .corrupt-<hash>
// with the chunk still in place. The re-run finishes that retirement; it does
// not write a second identical record.
func TestQuarantineUnderAMarkerFinishesARecordedRetirement(t *testing.T) {
	repo, sealedDir, live, name, cn := quarantineSetup(t, false)
	if _, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, noRule); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// The re-sealed chunk at the covered name goes bad and leaks.
	body := leakyChunkBody()
	chunk := filepath.Join(sealedDir, name)
	if err := os.WriteFile(chunk, body, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	hashed := name + ".corrupt-" + hex.EncodeToString(sum[:])[:12]
	if err := saveOriginal(repo, cn.Stem(), body); err != nil {
		t.Fatal(err)
	}
	if err := writeRecord(filepath.Join(sealedDir, hashed), body, "names another user's home"); err != nil {
		t.Fatal(err)
	}
	if _, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, homeCheck); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	entries, err := os.ReadDir(sealedDir)
	if err != nil {
		t.Fatal(err)
	}
	records := 0
	for _, e := range entries {
		if strings.Contains(e.Name(), ".corrupt-") {
			records++
		}
	}
	if records != 1 {
		t.Errorf("%d .corrupt-<hash> records, want 1", records)
	}
	assertWithheld(t, repo, filepath.Join("sealed", hashed), cn.Stem(), body)
}

func countWithheld(notices ...string) int {
	n := 0
	for _, s := range notices {
		n += strings.Count(s, "was withheld")
	}
	return n
}

// TestQuarantineNoticeFollowsStagingAcrossRuns: the withheld notice is printed
// where the record is written, and a re-run that finds a record the index does
// not hold yet prints one notice for it as it stages it. A failed add on an
// untracked chunk leaves the record written, so the first run says so; the
// re-run stages it and says so once.
func TestQuarantineNoticeFollowsStagingAcrossRuns(t *testing.T) {
	repo, sealedDir, live, name, cn := quarantineSetup(t, false)
	lock := filepath.Join(repo, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	first := captureNotices(t, func() {
		_, err = Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, homeCheck)
	})
	if err == nil {
		t.Fatal("Quarantine succeeded with the index locked")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	second := captureNotices(t, func() {
		_, err = Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, homeCheck)
	})
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	// The first run wrote the record and said so; the re-run staged it and says
	// so once.
	if countWithheld(first) != 1 || countWithheld(second) != 1 {
		t.Errorf("notices: first run %q, second %q; want one each", first, second)
	}
	assertWithheld(t, repo, filepath.Join("sealed", name+".corrupt"), cn.Stem(), leakyChunkBody())
	third := captureNotices(t, func() {
		_, err = Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, homeCheck)
	})
	if countWithheld(third) != 0 {
		t.Errorf("a settled quarantine printed a notice: %q", third)
	}
}

// TestQuarantineCrashAfterRemovalPrintsOnce: a crash after the chunk was
// removed leaves the record in the work tree and not in the index. The re-run
// stages it and prints one notice, without a marker (a fresh run resuming) and
// with one (a completed run whose record was lost from the index).
func TestQuarantineCrashAfterRemovalPrintsOnce(t *testing.T) {
	body := leakyChunkBody()
	t.Run("no marker", func(t *testing.T) {
		repo, sealedDir, live, name, cn := quarantineSetup(t, true)
		rel := filepath.Join("sealed", name+".corrupt")
		if err := saveOriginal(repo, cn.Stem(), body); err != nil {
			t.Fatal(err)
		}
		if err := writeRecord(filepath.Join(repo, rel), body, "names another user's home"); err != nil {
			t.Fatal(err)
		}
		gitOut(t, repo, "rm", "--cached", "-q", "--", filepath.Join("sealed", name))
		if err := os.Remove(filepath.Join(sealedDir, name)); err != nil {
			t.Fatal(err)
		}
		var err error
		out := captureNotices(t, func() {
			_, err = Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, homeCheck)
		})
		if err != nil {
			t.Fatal(err)
		}
		if n := countWithheld(out); n != 1 {
			t.Errorf("%d notices: %q", n, out)
		}
		assertWithheld(t, repo, rel, cn.Stem(), body)
	})
	t.Run("marker", func(t *testing.T) {
		repo, sealedDir, live, name, cn := quarantineSetup(t, false)
		if _, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, noRule); err != nil {
			t.Fatal(err)
		}
		rel := filepath.Join("sealed", name+".corrupt")
		if err := saveOriginal(repo, cn.Stem(), body); err != nil {
			t.Fatal(err)
		}
		if err := writeRecord(filepath.Join(repo, rel), body, "names another user's home"); err != nil {
			t.Fatal(err)
		}
		var err error
		out := captureNotices(t, func() {
			_, err = Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, homeCheck)
		})
		if err != nil {
			t.Fatal(err)
		}
		if n := countWithheld(out); n != 1 {
			t.Errorf("%d notices: %q", n, out)
		}
	})
}

// TestProbeFailedAddUnderMarker: under a marker a covered chunk goes bad, the
// add fails after the record is written and the chunk removed, and the re-run
// stages the record. One notice in total, not two.
func TestProbeFailedAddUnderMarker(t *testing.T) {
	repo, sealedDir, live, name, cn := quarantineSetup(t, false)
	if _, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, noRule); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sealedDir, name), leakyChunkBody(), 0o600); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(repo, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	first := captureNotices(t, func() {
		_, err = Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, homeCheck)
	})
	if err == nil {
		t.Fatal("Quarantine succeeded with the index locked")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	second := captureNotices(t, func() {
		_, err = Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, homeCheck)
	})
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if countWithheld(first) != 1 || countWithheld(second) != 1 {
		t.Errorf("notices: first run %q, second %q; want one each", first, second)
	}
}

// TestQuarantineUnderAMarkerFindsARecordOfAReSealedStem: the marker covers a
// re-sealed chunk whose stem differs from the marker's own (the live file
// held only ts 200 of 100..300). Damage under the marker retires that chunk,
// and a crash after its removal and before the add leaves its record unstaged.
// The re-run finds the record by the marker's coverage, stages it and says so
// once, and the index holds no leaking byte.
func TestQuarantineUnderAMarkerFindsARecordOfAReSealedStem(t *testing.T) {
	repo, sealedDir, _, _, cn := quarantineSetup(t, false)
	live := filepath.Join(repo, "live.jsonl")
	writeLive(t, live, 200)
	if _, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, homeCheck); err != nil {
		t.Fatalf("first run: %v", err)
	}
	reName := SessionChunkFile(200, 200)
	if _, err := os.Stat(filepath.Join(sealedDir, reName)); err != nil {
		t.Fatalf("setup: the re-sealed chunk %s is missing: %v", reName, err)
	}
	// Damage under the marker, then a crash after Remove and before GitAdd.
	body := leakyChunkBody()
	reCN, _ := Classify(reName, SessionNS)
	if err := saveOriginal(repo, reCN.Stem(), body); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	hashed := reCN.Stem() + ".jsonl.corrupt-" + hex.EncodeToString(sum[:])[:12]
	if err := writeRecord(filepath.Join(sealedDir, hashed), body, "names another user's home"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(sealedDir, reName)); err != nil {
		t.Fatal(err)
	}
	var err error
	out := captureNotices(t, func() {
		_, err = Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, homeCheck)
	})
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if n := countWithheld(out); n != 1 {
		t.Errorf("%d notices: %q", n, out)
	}
	rel := filepath.Join("sealed", hashed)
	if staged := gitOut(t, repo, "show", ":"+rel); strings.Contains(staged, "/home/bob") {
		t.Errorf("the index holds a leak: %q", staged)
	}
}

// TestQuarantineRestageNoticeSurvivesALaterFailure: the restage of a leaking
// index blob is a write that happened, so its notice is printed when the add
// succeeds, even though a later step (the live path is a directory) fails the
// run.
func TestQuarantineRestageNoticeSurvivesALaterFailure(t *testing.T) {
	repo, sealedDir, _, name, cn := quarantineSetup(t, true)
	rel := filepath.Join("sealed", name+".corrupt")
	gitOut(t, repo, "mv", "--", filepath.Join("sealed", name), rel)
	body := leakyChunkBody()
	if err := writeRecord(filepath.Join(repo, rel), body, "names another user's home"); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(repo, "live-dir")
	if err := os.Mkdir(live, 0o700); err != nil {
		t.Fatal(err)
	}
	var err error
	out := captureNotices(t, func() {
		_, err = Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, homeCheck)
	})
	if err == nil {
		t.Fatal("Quarantine succeeded with a directory as the live path")
	}
	if n := countWithheld(out); n != 1 {
		t.Errorf("%d withheld notices: %q", n, out)
	}
	if staged := gitOut(t, repo, "show", ":"+rel); strings.Contains(staged, "/home/bob") {
		t.Errorf("the index still holds the leak: %q", staged)
	}
}
