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

// activeIn returns roster-active sessions that recorded checkout as the writer
// of their live zone — the ordinary case, where the committing checkout is also
// the one that wrote the live files.
func activeIn(checkout string, sessions ...string) []ActiveSession {
	out := make([]ActiveSession, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, ActiveSession{Session: s, Checkout: checkout})
	}
	return out
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
	if err := VacuumCrossCheck(repo, globalRoot, activeIn(repo, "sess-active"), &buf); err != nil {
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
	if err := VacuumCrossCheck(repo, globalRoot, activeIn(repo, "sess-ml"), &buf); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte("mission live log is gone")) {
		t.Errorf("vacuum reported sealed mission lines as lost: %q", buf.String())
	}
}

// TestVacuumCrossCheckWarnsOnDeletedLiveLogInWriterCheckout is the class a
// chunk must NOT suppress. A chunk vouches only through its watermark; the tail
// written after the last seal lives solely in the live file. So in the checkout
// that wrote it — where the live-missions zone stands and only THIS mission's
// file is missing — an absent live log is a deletion, and those post-watermark
// lines are genuinely gone.
//
// Suppressing on a sealed chunk alone made the whole chunk-derived half of the
// expected set unable to warn under any input, dropping the case the
// enumeration exists for (rsc, PR #413 M1).
func TestVacuumCrossCheckWarnsOnDeletedLiveLogInWriterCheckout(t *testing.T) {
	repo := t.TempDir()
	globalRoot := t.TempDir()
	const sess = "sess-deleted"
	mid := "m-2026-07-21-001"
	writeChunkFile(t, sealedMissionDir(repo, mid), audit.MissionChunkFile(sess, 100, 200), 100, 200)
	// This checkout wrote mission live logs — a sibling mission's file stands —
	// so the absent one is a deletion, not another checkout's ordinary absence.
	writeLiveMissionLog(t, repo, "m-2026-07-21-002", sess, 300)

	var buf bytes.Buffer
	if err := VacuumCrossCheck(repo, globalRoot, activeIn(repo, sess), &buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("mission "+mid)) ||
		!bytes.Contains(buf.Bytes(), []byte("mission live log is gone")) {
		t.Errorf("a deleted live log in the writer's checkout passed unremarked: %q", buf.String())
	}
}

// TestVacuumCrossCheckWarnsWhenWriterCheckoutIsGone is the crash ->
// checkout-deleted case, the loudest loss the design names. A deleted checkout
// took its live zone with it, so the absent zone there is NOT evidence the
// session never wrote — reading it that way would go silent on exactly the
// sequence the guard exists for. Only a writer that is still present can
// demonstrate it never wrote mission live logs.
func TestVacuumCrossCheckWarnsWhenWriterCheckoutIsGone(t *testing.T) {
	repo := t.TempDir()
	globalRoot := t.TempDir()
	const sess = "sess-gone"
	mid := "m-2026-07-21-001"
	writeChunkFile(t, sealedMissionDir(repo, mid), audit.MissionChunkFile(sess, 100, 200), 100, 200)
	// The roster recorded a checkout that no longer exists.
	gone := filepath.Join(t.TempDir(), "deleted-checkout")

	var buf bytes.Buffer
	if err := VacuumCrossCheck(repo, globalRoot, []ActiveSession{{Session: sess, Checkout: gone}}, &buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("mission "+mid)) ||
		!bytes.Contains(buf.Bytes(), []byte("mission live log is gone")) {
		t.Errorf("a deleted writer checkout passed unremarked: %q", buf.String())
	}
}

// writeLiveMissionLog writes one line to a mission's per-(mission, session)
// live log in a checkout's local zone.
func writeLiveMissionLog(t *testing.T, repo, mid, sess string, ts int64) {
	t.Helper()
	path := audit.LiveMissionLogPath(repo, mid, sess)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"ts":"` + audit.FormatLineTS(ts) + `","tool":"Bash"}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
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
	for i := 0; i < missions; i++ {
		mid := fmt.Sprintf("m-2026-07-21-%03d", i+1)
		ts := int64(100 + i*10)
		writeChunkFile(t, sealedMissionDir(repo, mid), audit.MissionChunkFile(sess, ts, ts+1), ts, ts+1)
	}

	var buf bytes.Buffer
	if err := VacuumCrossCheck(repo, globalRoot, activeIn(repo, sess), &buf); err != nil {
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
	if err := VacuumCrossCheck(repo, globalRoot, activeIn(repo, sess), &buf); err != nil {
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
	if err := VacuumCrossCheck(repo, globalRoot, activeIn(repo, sess), &buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("mission "+mid)) ||
		!bytes.Contains(buf.Bytes(), []byte("mission live log is gone")) {
		t.Errorf("vacuum did not warn on claimed-but-unsealed lost mission live log: %q", buf.String())
	}
}

// TestVacuumCrossCheckRosterCheckoutScoping is Fix 2: the live probes follow
// the checkout a roster recorded, never the checkout that happens to be
// committing. A session whose live files are alive in its own checkout draws no
// warning here, and a roster that recorded no checkout names no writer at all,
// so nothing can be concluded from an absence.
func TestVacuumCrossCheckRosterCheckoutScoping(t *testing.T) {
	const sess = "sess-elsewhere"
	mid := "m-2026-07-21-011"

	cases := []struct {
		name string
		// checkout resolves the recorded path once the temp dirs exist.
		checkout  func(committing, writer string) string
		writeLive bool
		wantWarn  bool
	}{
		{
			name:      "recorded checkout is another live one",
			checkout:  func(_, writer string) string { return writer },
			writeLive: true,
			wantWarn:  false,
		},
		{
			name:      "recorded checkout is another one, live file gone",
			checkout:  func(_, writer string) string { return writer },
			writeLive: false,
			wantWarn:  true,
		},
		{
			name:      "roster records no checkout",
			checkout:  func(string, string) string { return "" },
			writeLive: false,
			wantWarn:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			committing := t.TempDir()
			writer := t.TempDir()
			globalRoot := t.TempDir()
			// The session is bound to the mission but sealed no chunk, so only
			// the live file can account for its lines.
			if err := mission.WriteActiveMission(globalRoot, sess, mid); err != nil {
				t.Fatal(err)
			}
			if tc.writeLive {
				path := audit.LiveMissionLogPath(writer, mid, sess)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				line := `{"ts":"` + audit.FormatLineTS(100) + `","tool":"Bash"}` + "\n"
				if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			active := []ActiveSession{{Session: sess, Checkout: tc.checkout(committing, writer)}}

			var buf bytes.Buffer
			if err := VacuumCrossCheck(committing, globalRoot, active, &buf); err != nil {
				t.Fatal(err)
			}
			got := bytes.Contains(buf.Bytes(), []byte("mission live log is gone"))
			if got != tc.wantWarn {
				t.Errorf("warned = %v, want %v: %q", got, tc.wantWarn, buf.String())
			}
		})
	}
}

// TestVacuumCrossCheckNotesUnrecordedCheckouts pins the transition window: a
// roster predating the checkout field cannot be probed, and the guard says so
// once rather than covering fewer sessions than it appears to.
func TestVacuumCrossCheckNotesUnrecordedCheckouts(t *testing.T) {
	repo := t.TempDir()
	globalRoot := t.TempDir()
	active := []ActiveSession{{Session: "old-a"}, {Session: "old-b"}}

	var buf bytes.Buffer
	if err := VacuumCrossCheck(repo, globalRoot, active, &buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("2 active session(s) predate the roster checkout field")) {
		t.Errorf("unrecorded checkouts passed unremarked: %q", buf.String())
	}
	// One aggregate line, never one per session.
	if n := bytes.Count(buf.Bytes(), []byte("predate the roster checkout field")); n != 1 {
		t.Errorf("note printed %d times, want 1: %q", n, buf.String())
	}
}

// TestVacuumCrossCheckTombstoneUsesRecordedCheckout is Fix 4: the tombstone
// branch stats the checkout the purge recorded, not the committing one. A
// tombstone flagged in a checkout whose live file still stands must report the
// unsealed lines, not declare them gone.
func TestVacuumCrossCheckTombstoneUsesRecordedCheckout(t *testing.T) {
	committing := t.TempDir()
	gitRepoWithOrigin(t, committing, vacuumTestRepoID)
	writer := t.TempDir()
	globalRoot := t.TempDir()

	// The live audit file stands in the recorded checkout, holding one unsealed
	// line. It does not exist under the committing checkout.
	livePath := audit.LiveAuditPath(writer, "sess-tb")
	if err := os.MkdirAll(filepath.Dir(livePath), 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"ts":"` + audit.FormatLineTS(100) + `","tool":"Bash"}` + "\n"
	if err := os.WriteFile(livePath, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := audit.WriteTombstone(globalSessionsDir(t, globalRoot), audit.Tombstone{
		Session: "sess-tb", Repo: vacuumTestRepoID, Checkout: writer, UnsealedLines: true,
	}); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := VacuumCrossCheck(committing, globalRoot, nil, &buf); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte("live file is gone")) {
		t.Errorf("tombstone branch called a live file gone that stands in its recorded checkout: %q", buf.String())
	}
	if !bytes.Contains(buf.Bytes(), []byte("1 unsealed audit line(s) still on disk")) {
		t.Errorf("tombstone branch did not count the unsealed line at the recorded checkout: %q", buf.String())
	}
}
