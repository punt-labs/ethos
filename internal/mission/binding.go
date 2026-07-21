//go:build !windows

package mission

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SessionBoundMissions returns the mission IDs a session is bound to through
// mission records — the union of the `ethos mission claim` active-mission
// sidecar and the Tier B delegation records naming the session as worker or
// parent. This is the binding half of the audit vacuum guard's expected-set
// union (docs/audit-seal.md §Seal failure policy): a session that claimed a
// mission or dispatched under it but sealed no chunk yet has no chunk-derived
// entry, so without this set its lost live mission log goes unenumerated and
// its purge unrefused.
//
// Records are enumerated, never globbed. The sidecar is one known path per
// session; delegation records are read from each tracked missions/<id>/
// delegations/ directory. globalRoot is ~/.punt-labs/ethos; repoRoot is the
// checkout. The result is sorted and deduplicated.
func SessionBoundMissions(globalRoot, repoRoot, sessionID string) ([]string, error) {
	if sessionID == "" {
		return nil, nil
	}
	seen := make(map[string]struct{})
	var ids []string
	add := func(id string) {
		id = filepath.Base(strings.TrimSpace(id))
		if id == "" || id == "." || id == string(filepath.Separator) {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	claimed, err := ReadActiveMission(globalRoot, sessionID)
	if err != nil {
		return nil, err
	}
	add(claimed)

	if err := addDelegationBoundMissions(repoRoot, sessionID, add); err != nil {
		return nil, err
	}

	sort.Strings(ids)
	return ids, nil
}

// addDelegationBoundMissions calls add with the mission ID of every tracked
// Tier B delegation whose worker or parent session is sessionID. It walks the
// per-mission delegations directories under the repo's mission tree and reads
// each record — enumeration, not a glob over live files.
func addDelegationBoundMissions(repoRoot, sessionID string, add func(string)) error {
	base := RepoStatePath(repoRoot, "missions")
	missions, err := os.ReadDir(base)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("reading missions dir %s: %w", base, err)
	}
	for _, m := range missions {
		if !m.IsDir() {
			continue
		}
		missionID := m.Name()
		delegDir := filepath.Join(base, missionID, "delegations")
		entries, dErr := os.ReadDir(delegDir)
		if dErr != nil {
			if errors.Is(dErr, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("reading delegations dir %s: %w", delegDir, dErr)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			recordPath := filepath.Join(delegDir, e.Name(), "record.yaml")
			d, lErr := LoadDelegation(recordPath)
			if lErr != nil {
				if errors.Is(lErr, fs.ErrNotExist) {
					continue
				}
				return fmt.Errorf("loading delegation %s: %w", recordPath, lErr)
			}
			if d.Session == sessionID || d.ParentSession == sessionID {
				add(missionID)
				break
			}
		}
	}
	return nil
}
