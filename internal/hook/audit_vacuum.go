package hook

import (
	"fmt"
	"io"

	"github.com/punt-labs/ethos/internal/audit"
)

// VacuumCrossCheck guards the seal's silent-vacuum case (docs/audit-seal.md
// §Seal failure policy): a seal that touches nothing must still notice a
// session whose unsealed audit lines were lost. It is per session and
// iterates two sources — each purge tombstone whose recorded repo is this
// repo and that carries an unsealed-lines flag, and each roster-active session
// bound to this repo — warning on stderr for any whose recorded live file is
// absent (a checkout deleted with unsealed lines) or still holds unsealed
// lines. It never blocks (the caller stays exit 0); it only refuses to let a
// lost live file pass unremarked.
//
// The tombstone branch is what keeps the crash -> purge -> checkout-deleted ->
// commit sequence from going silent: purge removed the roster entry the
// per-session check would otherwise have visited.
func VacuumCrossCheck(repoRoot, globalSessionsDir string, activeSessions []string, w io.Writer) error {
	tombstones, err := audit.ListTombstones(globalSessionsDir)
	if err != nil {
		return err
	}
	for _, t := range tombstones {
		if t.Repo != repoRoot || !t.Flagged() {
			continue
		}
		if !audit.SessionLiveFileExists(repoRoot, t.Session) {
			fmt.Fprintf(w,
				"warning: session %s was purged with unsealed audit lines and its live file is gone; "+
					"those lines are lost. Acknowledge with `ethos session purge --ack %s`\n",
				t.Session, t.Session)
			continue
		}
		n, cErr := audit.SessionUnsealedCount(repoRoot, t.Session)
		if cErr != nil {
			return cErr
		}
		fmt.Fprintf(w,
			"warning: session %s was purged with %d unsealed audit line(s) still on disk; "+
				"commit to seal them. Acknowledge with `ethos session purge --ack %s`\n",
			t.Session, n, t.Session)
	}

	// Roster-active sessions bound to this repo whose recorded live file has
	// vanished — a single deleted live file with no purge to leave a tombstone.
	for _, sessionID := range activeSessions {
		if !audit.SessionLiveFileExists(repoRoot, sessionID) {
			fmt.Fprintf(w,
				"warning: active session %s has no live audit file in this repo; "+
					"if it was deleted, unsealed lines were lost\n",
				sessionID)
		}
	}
	return nil
}
