package audit

import (
	"fmt"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// storePolicy names the layout for a store root S and the checkout C whose
// tracked tree is probed. Seal writes trace chunks under S (DES-079); in a
// linked worktree C is not S, so callers pass both.
func storePolicy(storeRoot, checkoutRoot string) (layout.Policy, error) {
	s, err := filepath.Abs(storeRoot)
	if err != nil {
		return layout.Policy{}, fmt.Errorf("resolving %s: %w", storeRoot, err)
	}
	c, err := filepath.Abs(checkoutRoot)
	if err != nil {
		return layout.Policy{}, fmt.Errorf("resolving %s: %w", checkoutRoot, err)
	}
	return layout.NewRepository(s, c, layout.RecordBlame)
}

// sealedSessionWatermark is the highest sealed timestamp for a session across
// every place sealed chunks live: the local trace archive the seal writes
// (DES-079) and the legacy tracked copies. Readers union both, so a chunk in
// either counts.
func sealedSessionWatermark(storeRoot, trackedRoot, sessionID string) (int64, error) {
	p, err := storePolicy(storeRoot, trackedRoot)
	if err != nil {
		return 0, err
	}
	dirs, err := StreamDirs(p, Stream{Namespace: SessionNS, Session: sessionID})
	if err != nil {
		return 0, err
	}
	return maxWatermark(dirs, SessionNS, "")
}

// sealedMissionDirs lists the directories that may hold a mission's sealed
// chunks: the local trace archive first, then the legacy tracked directory.
func sealedMissionDirs(storeRoot, trackedRoot, missionID string) ([]string, error) {
	p, err := storePolicy(storeRoot, trackedRoot)
	if err != nil {
		return nil, err
	}
	return StreamDirs(p, Stream{Namespace: MissionNS, Session: "-", Mission: filepath.Base(missionID)})
}

func maxWatermark(dirs []string, ns Namespace, session string) (int64, error) {
	var wm int64
	for _, d := range dirs {
		w, err := Watermark(d, ns, session)
		if err != nil {
			return 0, err
		}
		if w > wm {
			wm = w
		}
	}
	return wm, nil
}

// sealedMissionWatermark is the highest sealed timestamp for one session's
// lines of a mission across the local trace archive and the legacy tracked
// directory.
func sealedMissionWatermark(storeRoot, trackedRoot, missionID, sessionID string) (int64, error) {
	dirs, err := sealedMissionDirs(storeRoot, trackedRoot, missionID)
	if err != nil {
		return 0, err
	}
	return maxWatermark(dirs, MissionNS, sessionID)
}

// sealedMissionsBases lists the directories holding per-mission sealed
// directories: S's trace archive and tracked zone, and the probed checkout's
// tracked zone.
func sealedMissionsBases(storeRoot, trackedRoot string) []string {
	return []string{
		filepath.Join(LocalZoneBase(storeRoot), "trace", "missions"),
		SealedMissionsBase(storeRoot),
		SealedMissionsBase(trackedRoot),
	}
}
