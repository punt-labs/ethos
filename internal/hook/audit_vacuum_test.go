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
