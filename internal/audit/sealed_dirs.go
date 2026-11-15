package audit

import (
	"fmt"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// storePolicy names the layout of a checkout used as its own store, the view
// the unsealed-line probes have of a root.
func storePolicy(root string) (layout.Policy, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return layout.Policy{}, fmt.Errorf("resolving %s: %w", root, err)
	}
	return layout.NewRepository(abs, abs, layout.RecordBlame)
}

// sealedSessionWatermark is the highest sealed timestamp for a session across
// every place sealed chunks live: the local trace archive the seal writes
// (DES-079) and the legacy tracked copies. Readers union both, so a chunk in
// either counts.
func sealedSessionWatermark(root, sessionID string) (int64, error) {
	p, err := storePolicy(root)
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
func sealedMissionDirs(root, missionID string) ([]string, error) {
	p, err := storePolicy(root)
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
func sealedMissionWatermark(root, missionID, sessionID string) (int64, error) {
	dirs, err := sealedMissionDirs(root, missionID)
	if err != nil {
		return 0, err
	}
	return maxWatermark(dirs, MissionNS, sessionID)
}

// sealedMissionsBases lists the directories holding per-mission sealed
// directories: the local trace archive and the legacy tracked zone.
func sealedMissionsBases(root string) []string {
	return []string{
		filepath.Join(LocalZoneBase(root), "trace", "missions"),
		SealedMissionsBase(root),
	}
}
