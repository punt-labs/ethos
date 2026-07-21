package hook

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/internal/audit"
	"github.com/punt-labs/ethos/internal/mission"
)

// globalSessionsDir returns the sessions subdir VacuumCrossCheck derives from
// a global root, creating it so tombstone writes land where the check reads.
func globalSessionsDir(t *testing.T, globalRoot string) string {
	t.Helper()
	dir := filepath.Join(globalRoot, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestVacuumCrossCheckWarnsOnFlaggedTombstoneGone(t *testing.T) {
	repo := t.TempDir()
	globalRoot := t.TempDir()
	// A tombstone for a session purged with unsealed lines whose live file is
	// gone (no live file was ever written under repo).
	if err := audit.WriteTombstone(globalSessionsDir(t, globalRoot), audit.Tombstone{
		Session: "sess-lost", Repo: repo, UnsealedLines: true,
	}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := VacuumCrossCheck(repo, globalRoot, nil, &buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("sess-lost")) ||
		!bytes.Contains(buf.Bytes(), []byte("live file is gone")) {
		t.Errorf("vacuum did not warn on lost session: %q", buf.String())
	}
}

func TestVacuumCrossCheckIgnoresOtherRepos(t *testing.T) {
	repo := t.TempDir()
	globalRoot := t.TempDir()
	if err := audit.WriteTombstone(globalSessionsDir(t, globalRoot), audit.Tombstone{
		Session: "sess-other", Repo: "/some/other/repo", UnsealedLines: true,
	}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := VacuumCrossCheck(repo, globalRoot, nil, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("vacuum warned on another repo's tombstone: %q", buf.String())
	}
}

func TestVacuumCrossCheckRosterActiveMissingLive(t *testing.T) {
	repo := t.TempDir()
	globalRoot := t.TempDir()
	var buf bytes.Buffer
	// An active session bound to the repo whose live file does not exist.
	if err := VacuumCrossCheck(repo, globalRoot, []string{"sess-active"}, &buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("sess-active")) {
		t.Errorf("vacuum did not warn on active session with no live file: %q", buf.String())
	}
}

func TestVacuumCrossCheckWarnsOnLostMissionLive(t *testing.T) {
	repo := t.TempDir()
	globalRoot := t.TempDir()
	// A session that sealed a mission chunk (proving it wrote the live log)
	// whose per-(mission,session) live log is now gone — REQ-1: the vacuum
	// must warn in the mission namespace, not just the audit one.
	mid := "m-2026-07-21-001"
	sealedDir := sealedMissionDir(repo, mid)
	writeChunkFile(t, sealedDir, audit.MissionChunkFile("sess-ml", 100, 200), 100, 200)
	// The live log itself is absent under .punt-labs/local/.

	var buf bytes.Buffer
	if err := VacuumCrossCheck(repo, globalRoot, []string{"sess-ml"}, &buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("mission "+mid)) ||
		!bytes.Contains(buf.Bytes(), []byte("mission live log is gone")) {
		t.Errorf("vacuum did not warn on lost mission live log: %q", buf.String())
	}
}

// TestVacuumCrossCheckWarnsOnClaimedButUnsealedMissionLive is the REQ-1
// residual case: a Tier B session that claimed a mission (mission-claim
// sidecar) but sealed NO chunk, whose live mission log was then deleted. The
// chunk-derived half of the expected set is empty, so only the mission-record
// binding union enumerates it. Without the union this loss is silent.
func TestVacuumCrossCheckWarnsOnClaimedButUnsealedMissionLive(t *testing.T) {
	repo := t.TempDir()
	globalRoot := t.TempDir()
	mid := "m-2026-07-21-009"
	sess := "sess-claimed"
	// The session claimed the mission — sidecar present — but never sealed a
	// chunk under missions/<id>/. Its live log is absent (worktree deleted).
	if err := mission.WriteActiveMission(globalRoot, sess, mid); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := VacuumCrossCheck(repo, globalRoot, []string{sess}, &buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("mission "+mid)) ||
		!bytes.Contains(buf.Bytes(), []byte("mission live log is gone")) {
		t.Errorf("vacuum did not warn on claimed-but-unsealed lost mission live log: %q", buf.String())
	}
}
