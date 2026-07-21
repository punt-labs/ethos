package audit

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
	m, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch")
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
	m, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch")
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

func TestQuarantineIdempotent(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sealedDir := filepath.Join(repo, "sealed")
	writeChunk(t, sealedDir, SessionChunkFile(100, 300), 100, 200)
	gitCommitAll(t, repo, "seal")
	live := filepath.Join(repo, "live.jsonl")
	writeLive(t, live, 100, 200, 300)
	cn, _ := Classify(SessionChunkFile(100, 300), SessionNS)

	m1, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch")
	if err != nil {
		t.Fatalf("first quarantine: %v", err)
	}
	m2, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch")
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
	m, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch")
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
	if _, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch"); err != nil {
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
	if _, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch"); err != nil {
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
	if _, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch"); err != nil {
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
	if _, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch"); err != nil {
		t.Fatal(err)
	}
	// After quarantine, a scan no longer errors on the retired chunk (the
	// .corrupt is covered by the marker).
	if _, err := ScanSealedDir(sealedDir, SessionNS, ""); err != nil {
		t.Errorf("scan after quarantine errored: %v", err)
	}
}
