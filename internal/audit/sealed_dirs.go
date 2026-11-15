package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

// storePolicy names the layout of root's repository. root is a checkout, not
// necessarily the store: in a linked worktree seal writes under the main
// checkout's .punt-labs/local (DES-079), so the store is found through git's
// common dir. internal/resolve holds the canonical rule (storeRoot) but imports
// this package, so the rule is repeated here: a checkout whose git dir is the
// common dir is its own store; a linked worktree of a non-bare repository uses
// the work tree holding the common .git; anything else, and a directory outside
// a repository, is its own store.
func storePolicy(root string) (layout.Policy, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return layout.Policy{}, fmt.Errorf("resolving %s: %w", root, err)
	}
	store, err := storeRootOf(abs)
	if err != nil {
		return layout.Policy{}, err
	}
	return layout.NewRepository(store, abs, layout.RecordBlame)
}

func storeRootOf(root string) (string, error) {
	out, absent, err := gitenv.Read(gitenv.Spec{
		Git: "git", Dir: root, Env: gitenv.Parsed(),
		Args: []string{"rev-parse", "--absolute-git-dir", "--git-common-dir"},
	}, gitenv.AbsentNotRepo)
	if err != nil {
		return "", fmt.Errorf("finding the store of %s: %w", root, err)
	}
	if absent {
		return root, nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return "", fmt.Errorf("finding the store of %s: git answered %q, not two paths", root, out)
	}
	gitDir, common := lines[0], lines[1]
	if !filepath.IsAbs(common) {
		common = filepath.Join(root, common)
	}
	gi, gerr := os.Stat(gitDir)
	ci, cerr := os.Stat(common)
	if gerr == nil && cerr == nil && os.SameFile(gi, ci) {
		return root, nil
	}
	common = filepath.Clean(common)
	if filepath.Base(common) != ".git" {
		return root, nil
	}
	return filepath.Dir(common), nil
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
