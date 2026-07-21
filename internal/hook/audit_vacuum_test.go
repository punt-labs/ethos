package hook

import (
	"bytes"
	"testing"

	"github.com/punt-labs/ethos/internal/audit"
)

func TestVacuumCrossCheckWarnsOnFlaggedTombstoneGone(t *testing.T) {
	repo := t.TempDir()
	globalSessions := t.TempDir()
	// A tombstone for a session purged with unsealed lines whose live file is
	// gone (no live file was ever written under repo).
	if err := audit.WriteTombstone(globalSessions, audit.Tombstone{
		Session: "sess-lost", Repo: repo, UnsealedLines: true,
	}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := VacuumCrossCheck(repo, globalSessions, nil, &buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("sess-lost")) ||
		!bytes.Contains(buf.Bytes(), []byte("live file is gone")) {
		t.Errorf("vacuum did not warn on lost session: %q", buf.String())
	}
}

func TestVacuumCrossCheckIgnoresOtherRepos(t *testing.T) {
	repo := t.TempDir()
	globalSessions := t.TempDir()
	if err := audit.WriteTombstone(globalSessions, audit.Tombstone{
		Session: "sess-other", Repo: "/some/other/repo", UnsealedLines: true,
	}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := VacuumCrossCheck(repo, globalSessions, nil, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("vacuum warned on another repo's tombstone: %q", buf.String())
	}
}

func TestVacuumCrossCheckRosterActiveMissingLive(t *testing.T) {
	repo := t.TempDir()
	globalSessions := t.TempDir()
	var buf bytes.Buffer
	// An active session bound to the repo whose live file does not exist.
	if err := VacuumCrossCheck(repo, globalSessions, []string{"sess-active"}, &buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("sess-active")) {
		t.Errorf("vacuum did not warn on active session with no live file: %q", buf.String())
	}
}

func TestVacuumCrossCheckWarnsOnLostMissionLive(t *testing.T) {
	repo := t.TempDir()
	globalSessions := t.TempDir()
	// A session that sealed a mission chunk (proving it wrote the live log)
	// whose per-(mission,session) live log is now gone — REQ-1: the vacuum
	// must warn in the mission namespace, not just the audit one.
	mid := "m-2026-07-21-001"
	sealedDir := sealedMissionDir(repo, mid)
	writeChunkFile(t, sealedDir, audit.MissionChunkFile("sess-ml", 100, 200), 100, 200)
	// The live log itself is absent under .punt-labs/local/.

	var buf bytes.Buffer
	if err := VacuumCrossCheck(repo, globalSessions, []string{"sess-ml"}, &buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("mission "+mid)) ||
		!bytes.Contains(buf.Bytes(), []byte("mission live log is gone")) {
		t.Errorf("vacuum did not warn on lost mission live log: %q", buf.String())
	}
}
