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

// ImportFailure names a global mission ImportGlobalMissions could not copy.
// Reason names the file and the cause, never a value from the file.
type ImportFailure struct {
	ID     string
	Reason string
}

// ReasonRepoCopyDiverged is the reason for a mission already in the
// repository whose copy no longer equals the redaction of the global files:
// it was edited after the import, which is legitimate and changes nothing.
const ReasonRepoCopyDiverged = "repository copy has diverged; global copy kept"

// ImportResult is what ImportGlobalMissions did. Imported lists missions
// newly copied into the repository store and verified; Present lists
// missions already there whose copy still equals the redacted global files
// (nothing to do); Diverged lists missions already there whose copy differs
// from that redaction; Failed lists missions that could not be copied or
// whose fresh copy did not read back as written; Warnings are the non-fatal
// notes of the ownership scan. The global files are never changed.
type ImportResult struct {
	Imported []string
	Present  []string
	Diverged []string
	Failed   []ImportFailure
	Warnings []string
}

// importVerifyHook is a test seam that runs after the rename and before the
// repository copy is verified, inside the lock nest. Nil in production.
var importVerifyHook func(repoMissionDir string)

// SetImportVerifyHook installs a function that runs between the rename and
// the verification of each imported mission. It returns the restore.
func SetImportVerifyHook(fn func(repoMissionDir string)) func() {
	prev := importVerifyHook
	importVerifyHook = fn
	return func() { importVerifyHook = prev }
}

// ImportGlobalMissions copies every global mission this repository owns (see
// repoMissionIDs) into the repository store through the redactor. It never
// deletes or changes a global file: a mission may be in use by another
// repository, and nothing in a global record says which. It is
// MigrateMission without its output: it prints nothing, keeps going past a
// mission it cannot copy and reports each one in Failed.
//
// Each copy is verified under the same lock nest as the rename: the redacted
// expectation is computed, the copy staged and renamed, and every artifact
// read back and compared, all while the global and repository mission locks
// are held, so a writer to the global mission cannot change it between the
// read and the check. A fresh copy that does not read back as written is a
// Failure. A copy already present is compared the same way: equal is
// Present, different is Diverged.
func ImportGlobalMissions(globalRoot, repoRoot, checkoutRoot string) (ImportResult, error) {
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
		var verdict importVerdict
		decision, err := migrateOneMissionQ(legacyDir, repoDir, repoRoot, id, owned, false, true,
			func(repoMissionDir string, fresh bool) error {
				v, err := verifyUnderLock(r, legacyDir, id, repoMissionDir, fresh)
				verdict = v
				return err
			})
		switch {
		case err != nil:
			res.Failed = append(res.Failed, ImportFailure{ID: id, Reason: err.Error()})
		case strings.HasPrefix(decision, "skip "):
			res.Failed = append(res.Failed, ImportFailure{ID: id, Reason: strings.TrimPrefix(decision, "skip "+id+": ")})
		case verdict == verdictImported:
			res.Imported = append(res.Imported, id)
		case verdict == verdictPresent:
			res.Present = append(res.Present, id)
		case verdict == verdictDiverged:
			res.Diverged = append(res.Diverged, id)
		}
	}
	return res, nil
}

type importVerdict int

const (
	verdictNone importVerdict = iota
	verdictImported
	verdictPresent
	verdictDiverged
)

// verifyUnderLock runs inside the mission lock nest. It computes the
// redacted expectation from the global files and compares every artifact of
// the repository copy to it. For a fresh copy a difference is an error (the
// copy did not read back as written); for a copy that was already present a
// difference is divergence, which is a verdict, not an error.
func verifyUnderLock(r PathRedactor, legacyDir, id, repoMissionDir string, fresh bool) (importVerdict, error) {
	expected, err := expectedImport(r, legacyDir, id)
	if err != nil {
		return verdictNone, err
	}
	if importVerifyHook != nil {
		importVerifyHook(repoMissionDir)
	}
	artifact, err := compareImport(repoMissionDir, expected)
	if err != nil {
		return verdictNone, err
	}
	switch {
	case artifact == "" && fresh:
		return verdictImported, nil
	case artifact == "":
		return verdictPresent, nil
	case fresh:
		return verdictNone, fmt.Errorf("verifying the repository copy: %s differs from the redacted source; global files retained", artifact)
	default:
		return verdictDiverged, nil
	}
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

// compareImport reads every expected artifact back from the repository copy
// and returns the name of the first one that is not byte-equal to the
// expectation, or "" when all match. An unreadable artifact is an error.
// Names, never content, are reported.
func compareImport(repoMissionDir string, expected map[string][]byte) (string, error) {
	for _, a := range missionArtifacts {
		want, ok := expected[a.repoName]
		if !ok {
			continue
		}
		got, err := os.ReadFile(filepath.Join(repoMissionDir, a.repoName))
		if err != nil {
			return "", fmt.Errorf("verifying the repository copy: %s: %w; global files retained", a.repoName, err)
		}
		if !bytes.Equal(got, want) {
			return a.repoName, nil
		}
	}
	return "", nil
}
