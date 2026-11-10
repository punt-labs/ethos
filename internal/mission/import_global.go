package mission

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// ImportFailure names a global mission ImportGlobalMissions could not move.
// Reason names the file and the cause, never a value from the file.
type ImportFailure struct {
	ID     string
	Reason string
}

// ImportResult is what ImportGlobalMissions did. Imported lists missions
// copied into the repository store and verified; Deleted lists the subset
// whose global files were removed, which requires exclusive ownership;
// Shared lists imported or already-present missions whose global copy was
// kept because another repository may own them too; Failed lists missions
// left in place; Warnings are the non-fatal notes of the ownership scan.
type ImportResult struct {
	Imported []string
	Deleted  []string
	Shared   []string
	Failed   []ImportFailure
	Warnings []string
}

// Test seams. importVerifyHook runs after the rename and before the
// repository copy is verified; importDeleteHook runs before each global
// file is removed. Both are nil in production.
var (
	importVerifyHook func(repoMissionDir string)
	importDeleteHook func(path string)
)

// SetImportVerifyHook installs a function that runs between the rename and
// the verification of each imported mission. It returns the restore.
func SetImportVerifyHook(fn func(repoMissionDir string)) func() {
	prev := importVerifyHook
	importVerifyHook = fn
	return func() { importVerifyHook = prev }
}

// SetImportDeleteHook installs a function that runs before each global file
// of an imported mission is deleted. It returns the restore.
func SetImportDeleteHook(fn func(path string)) func() {
	prev := importDeleteHook
	importDeleteHook = fn
	return func() { importDeleteHook = prev }
}

// ImportGlobalMissions imports every global mission this repository owns
// (see repoMissionIDs) into the repository store through the redactor and,
// where the ownership is exclusive, deletes the global copies. It is
// MigrateMission without its output: it prints nothing, keeps going past a
// mission it cannot move and reports each one in Failed.
//
// exclusive is the set of mission IDs whose ownership the caller has proved
// belongs to this repository alone; a mission owned here but not in that
// set is imported as a copy and its global files are kept, reported in
// Shared. A global file is deleted only after the repository copy has been
// read back and found equal to the redacted bytes that were staged; a
// mismatch is a Failure and the global files stay. The contract is deleted
// last so a crash between deletes leaves a candidate the next run finishes.
func ImportGlobalMissions(globalRoot, repoRoot, checkoutRoot string, exclusive map[string]bool) (ImportResult, error) {
	var res ImportResult
	if repoRoot == "" || globalRoot == "" {
		return res, fmt.Errorf("import global missions: roots must be set")
	}
	legacyDir := filepath.Join(globalRoot, "missions")
	repoDir := RepoStatePath(repoRoot, "missions")
	ids, err := enumerateMigrateCandidates(legacyDir, "")
	if err != nil {
		return res, fmt.Errorf("enumerating global missions in %s: %w", legacyDir, err)
	}
	if len(ids) == 0 {
		return res, nil
	}
	owned, err := repoMissionIDsWarn(repoRoot, checkoutRoot, func(msg string) {
		res.Warnings = append(res.Warnings, msg)
	})
	if err != nil {
		return res, fmt.Errorf("scanning repo sessions for mission references: %w", err)
	}
	r, err := NewPathRedactor(repoRoot)
	if err != nil {
		return res, fmt.Errorf("import global missions: %w", err)
	}
	for _, id := range ids {
		if _, ok := owned[id]; !ok {
			continue
		}
		expected, err := expectedImport(r, legacyDir, id)
		if err != nil {
			res.Failed = append(res.Failed, ImportFailure{ID: id, Reason: err.Error()})
			continue
		}
		decision, err := migrateOneMissionQ(legacyDir, repoDir, repoRoot, id, owned, false, true)
		if err != nil {
			res.Failed = append(res.Failed, ImportFailure{ID: id, Reason: err.Error()})
			continue
		}
		repoMissionDir := filepath.Join(repoDir, id)
		moved := false
		switch {
		case strings.HasPrefix(decision, "migrate "):
			moved = true
			if importVerifyHook != nil {
				importVerifyHook(repoMissionDir)
			}
		case strings.HasPrefix(decision, "noop "):
			// Already in the repository: the global files are stale only if
			// the repository copy is the redaction of them.
		case strings.HasPrefix(decision, "skip "):
			res.Failed = append(res.Failed, ImportFailure{ID: id, Reason: strings.TrimPrefix(decision, "skip "+id+": ")})
			continue
		}
		if err := verifyImport(repoMissionDir, expected); err != nil {
			// A copy that does not read back as expected is not an import,
			// whichever branch produced it; the global files stay.
			res.Failed = append(res.Failed, ImportFailure{ID: id, Reason: err.Error()})
			continue
		}
		if moved {
			res.Imported = append(res.Imported, id)
		}
		if !exclusive[id] {
			res.Shared = append(res.Shared, id)
			continue
		}
		if err := removeLegacyMission(legacyDir, id, importDeleteHook); err != nil {
			res.Failed = append(res.Failed, ImportFailure{ID: id, Reason: err.Error()})
			continue
		}
		res.Deleted = append(res.Deleted, id)
	}
	return res, nil
}

// expectedImport computes, for every artifact of global mission id, the
// bytes the import must leave in the repository: the redacted form of the
// global file. An artifact the global tree lacks is absent from the map.
func expectedImport(r PathRedactor, legacyDir, id string) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, a := range missionArtifacts {
		src := filepath.Join(legacyDir, id+a.legacySuffix)
		data, err := os.ReadFile(src)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, ioErr("reading", src, err)
		}
		var red []byte
		switch {
		case strings.HasSuffix(src, ".jsonl"):
			red, _, err = redactJSONLines(r.Content, r.Path, reflect.TypeOf(Event{}), data)
		default:
			red, _, err = redactYAMLDoc(r.Content, r.Path, artifactType(a.repoName), data)
		}
		if err != nil {
			return nil, fmt.Errorf("redacting %s: %w", filepath.Base(src), err)
		}
		if red, err = guardWrite(filepath.Join(legacyDir, a.repoName), red); err != nil {
			return nil, err
		}
		out[a.repoName] = red
	}
	return out, nil
}

// verifyImport reads every expected artifact back from the repository copy
// and requires it byte-equal to the redacted expectation. It names the
// artifact that differs, never its content.
func verifyImport(repoMissionDir string, expected map[string][]byte) error {
	for _, a := range missionArtifacts {
		want, ok := expected[a.repoName]
		if !ok {
			continue
		}
		got, err := os.ReadFile(filepath.Join(repoMissionDir, a.repoName))
		if err != nil {
			return fmt.Errorf("verifying the repository copy: %s: %w; global files retained", a.repoName, err)
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("verifying the repository copy: %s differs from the redacted source; global files retained", a.repoName)
		}
	}
	return nil
}
