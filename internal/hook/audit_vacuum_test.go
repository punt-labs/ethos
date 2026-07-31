package hook

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/internal/audit"
	"github.com/punt-labs/ethos/internal/mission"
)

// vacuumTestRepoID is the identity the vacuum tests give their checkout's origin
// remote. VacuumCrossCheck matches a tombstone's Repo (an identity) against the
// identity it derives from the checkout, not against the checkout path.
const vacuumTestRepoID = "punt-labs/ethos"

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

// gitRepoWithOrigin makes dir a git checkout whose origin remote resolves to id,
// so VacuumCrossCheck derives that identity from the checkout.
func gitRepoWithOrigin(t *testing.T, dir, id string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init")
	run("remote", "add", "origin", "git@github.com:"+id+".git")
}

func TestVacuumCrossCheckWarnsOnFlaggedTombstoneGone(t *testing.T) {
	repo := t.TempDir()
	gitRepoWithOrigin(t, repo, vacuumTestRepoID)
	globalRoot := t.TempDir()
	// A tombstone for a session purged with unsealed lines whose live file is
	// gone (no live file was ever written under repo).
	if err := audit.WriteTombstone(globalSessionsDir(t, globalRoot), audit.Tombstone{
		Session: "sess-lost", Repo: vacuumTestRepoID, UnsealedLines: true,
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
	gitRepoWithOrigin(t, repo, vacuumTestRepoID)
	globalRoot := t.TempDir()
	if err := audit.WriteTombstone(globalSessionsDir(t, globalRoot), audit.Tombstone{
		Session: "sess-other", Repo: "punt-labs/other", UnsealedLines: true,
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

// TestVacuumCrossCheckSilentOnSealedMissionLive is ethos-q6e2: a checkout that
// did not write a mission's live log must not call the lines lost. Sealed
// chunks are git-tracked and reach every checkout; the live log is per-checkout
// and reaches none of them. Warning on absence alone reported loss for every
// mission a long-lived session had touched, on every commit, in every other
// checkout.
func TestVacuumCrossCheckSilentOnSealedMissionLive(t *testing.T) {
	repo := t.TempDir()
	globalRoot := t.TempDir()
	mid := "m-2026-07-21-001"
	writeChunkFile(t, sealedMissionDir(repo, mid), audit.MissionChunkFile("sess-ml", 100, 200), 100, 200)
	// No live log under .punt-labs/local/ — this checkout never wrote one.

	var buf bytes.Buffer
	if err := VacuumCrossCheck(repo, globalRoot, []string{"sess-ml"}, &buf); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte("mission live log is gone")) {
		t.Errorf("vacuum reported sealed mission lines as lost: %q", buf.String())
	}
}

// TestVacuumCrossCheckNoLiveZoneReproduction is the reported symptom at scale:
// a fresh checkout of a repo whose tracked tree already holds many missions'
// sealed chunks — a worktree, a clone, a second machine — has no live zone at
// all. Before the fix this printed one "lines were lost" warning per mission
// (43 in the reported case) on every commit. It must print none.
func TestVacuumCrossCheckNoLiveZoneReproduction(t *testing.T) {
	repo := t.TempDir()
	globalRoot := t.TempDir()
	const sess = "c7e50ab0"
	const missions = 43
	for i := range missions {
		mid := fmt.Sprintf("m-2026-07-21-%03d", i+1)
		ts := int64(100 + i*10)
		writeChunkFile(t, sealedMissionDir(repo, mid), audit.MissionChunkFile(sess, ts, ts+1), ts, ts+1)
	}

	var buf bytes.Buffer
	if err := VacuumCrossCheck(repo, globalRoot, []string{sess}, &buf); err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(buf.Bytes(), []byte("mission live log is gone")); n != 0 {
		t.Errorf("mission-loss warnings = %d, want 0:\n%s", n, buf.String())
	}
}

// TestVacuumCrossCheckSilentOnLegacyMissionLog is Fix 3: a mission closed
// before DES-058 split the zones has its events in the tracked log.jsonl and
// never had a per-(mission, session) live log to lose.
func TestVacuumCrossCheckSilentOnLegacyMissionLog(t *testing.T) {
	repo := t.TempDir()
	globalRoot := t.TempDir()
	mid := "m-2026-07-20-013"
	sess := "sess-legacy"
	dir := sealedMissionDir(repo, mid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"ts":"` + audit.FormatLineTS(100) + `","event":"dispatch"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "log.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	// The session is bound to it through the claim sidecar, so it is enumerated.
	if err := mission.WriteActiveMission(globalRoot, sess, mid); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := VacuumCrossCheck(repo, globalRoot, []string{sess}, &buf); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte("mission live log is gone")) {
		t.Errorf("vacuum reported a pre-DES-058 mission as lost: %q", buf.String())
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
