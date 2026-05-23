package mission

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxAuditLineBytes is the per-line ceiling for collectContractIDs.
// Audit lines are short JSONL objects; 16 MiB matches the buffer that
// decodeAuditEntries uses for the same file shape and tolerates
// pathological lines without rejecting them outright.
const maxAuditLineBytes = 16 * 1024 * 1024

// MigrateMission relocates a mission from the legacy global tree
// (<globalRoot>/missions/<id>.yaml + sibling .jsonl / .results.yaml /
// .reflections.yaml files) into the DES-054 per-repo tree
// (<repoRoot>/.ethos/missions/<id>/contract.yaml + log.jsonl /
// results.yaml / reflections.yaml).
//
// When missionID is empty, every legacy mission whose contract_id is
// referenced by an audit entry in this repo's
// <repoRoot>/.ethos/sessions/*/audit.jsonl is migrated; missions with
// no matching session are left alone (cross-repo policy).
//
// When missionID is non-empty, only that one mission is considered. A
// mission already migrated (contract.yaml present in the repo tree) is
// a no-op. A mission whose legacy contract is missing is reported as
// such and not an error.
//
// dryRun=true enumerates what would change without writing or
// deleting. The move is atomic at the per-mission directory level:
// sibling artifacts are staged under a sibling temp directory in
// <repoRoot>/.ethos/missions/ and renamed into place, then the legacy
// files are removed. On any error before the temp→final rename, the
// legacy tree is untouched and the staging directory is cleaned up.
//
// out receives one human-readable line per mission decision:
//
//	migrate <mission-id> -> .ethos/missions/<id>
//	skip <mission-id>: no repo session
//	skip <mission-id>: legacy contract missing
//	noop <mission-id>: already migrated
//
// A successful run with no candidates prints "nothing to migrate".
func MigrateMission(globalRoot, repoRoot, missionID string, dryRun bool, out io.Writer) error {
	if repoRoot == "" {
		return fmt.Errorf("migrate mission: repoRoot is empty")
	}
	if globalRoot == "" {
		return fmt.Errorf("migrate mission: globalRoot is empty")
	}

	legacyDir := filepath.Join(globalRoot, "missions")
	repoDir := filepath.Join(repoRoot, ".ethos", "missions")

	candidates, err := enumerateMigrateCandidates(legacyDir, missionID)
	if err != nil {
		return fmt.Errorf("enumerating legacy missions in %s: %w", legacyDir, err)
	}
	if len(candidates) == 0 {
		fmt.Fprintln(out, "nothing to migrate")
		return nil
	}

	repoMissions, repoErr := repoMissionIDs(repoRoot)
	if repoErr != nil {
		return fmt.Errorf("scanning repo sessions for mission references: %w", repoErr)
	}

	for _, id := range candidates {
		decision, err := migrateOneMission(legacyDir, repoDir, id, repoMissions, dryRun)
		if err != nil {
			return fmt.Errorf("migrating mission %s: %w", id, err)
		}
		fmt.Fprintln(out, decision)
	}
	return nil
}

// enumerateMigrateCandidates returns the mission IDs to consider.
// When the caller named one, the slice contains that ID alone (the
// existence check is deferred to migrateOneMission so a missing
// legacy file still produces a "skip" line). Otherwise every
// <legacyDir>/<id>.yaml file is a candidate.
//
// Filters sibling artifact files (.results.yaml, .reflections.yaml)
// and dotfiles (.counter-*, .create.lock) the same way Store.List
// does — see isContractFile.
func enumerateMigrateCandidates(legacyDir, missionID string) ([]string, error) {
	if missionID != "" {
		// Defense in depth: a caller-supplied ID is reduced to its
		// final path element so traversal-laced input cannot escape
		// the missions directory.
		return []string{filepath.Base(missionID)}, nil
	}
	entries, err := os.ReadDir(legacyDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", legacyDir, err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !isContractFile(name) {
			continue
		}
		ids = append(ids, strings.TrimSuffix(name, ".yaml"))
	}
	sort.Strings(ids)
	return ids, nil
}

// repoMissionIDs returns the set of mission IDs referenced as
// contract_id in any <repoRoot>/.ethos/sessions/*/audit.jsonl file.
// Missing sessions tree is treated as empty — a fresh repo has no
// audit history and therefore no migration candidates.
//
// The scan is best-effort: a malformed audit line is skipped (the
// permissive reader contract in audit_reader.go), not an error.
func repoMissionIDs(repoRoot string) (map[string]struct{}, error) {
	out := make(map[string]struct{})
	sessionsBase := filepath.Join(repoRoot, ".ethos", "sessions")
	dirs, err := os.ReadDir(sessionsBase)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return nil, fmt.Errorf("reading %s: %w", sessionsBase, err)
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		path := filepath.Join(sessionsBase, d.Name(), "audit.jsonl")
		if err := collectContractIDs(path, out); err != nil {
			return nil, fmt.Errorf("scanning %s: %w", path, err)
		}
	}
	return out, nil
}

// collectContractIDs adds every distinct contract_id from a JSONL
// audit file to dst. Missing file is a no-op. A malformed line is
// skipped with a stderr warning — the audit log reader contract is
// permissive so a single bad line does not poison the whole scan.
//
// Uses bufio.Scanner per-line because json.NewDecoder.Decode does NOT
// advance the underlying reader past a SyntaxError: a single bad
// token would make the loop spin forever. The pattern mirrors
// decodeAuditEntries in audit_reader.go.
func collectContractIDs(path string, dst map[string]struct{}) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxAuditLineBytes)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec struct {
			ContractID string `json:"contract_id"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			fmt.Fprintf(os.Stderr,
				"ethos: mission migrate: %s: line %d: skipping malformed line: %v\n",
				path, lineNo, err)
			continue
		}
		if rec.ContractID != "" {
			dst[rec.ContractID] = struct{}{}
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("scanning %s: %w", path, err)
	}
	return nil
}

// migrateArtifact pairs a legacy filename suffix with the repo-tree
// filename it maps to. The contract is the trust anchor and must be
// the last file written so the rename is the commit point — see
// migrateOneMission for the ordering.
type migrateArtifact struct {
	legacySuffix string
	repoName     string
}

// missionArtifacts lists the four files a mission may carry. The
// contract is required; the other three are present only when a
// round, result, or reflection has been written.
var missionArtifacts = []migrateArtifact{
	{legacySuffix: ".yaml", repoName: "contract.yaml"},
	{legacySuffix: ".jsonl", repoName: "log.jsonl"},
	{legacySuffix: ".results.yaml", repoName: "results.yaml"},
	{legacySuffix: ".reflections.yaml", repoName: "reflections.yaml"},
}

// migrateOneMission migrates a single mission. Returns a short
// status line describing the decision: skip (no repo session, legacy
// contract missing), noop (already migrated), or migrate (relocated
// into the repo tree).
//
// Ordering:
//  1. Resolve the legacy contract path; if missing, "skip".
//  2. Cross-repo check: if missionID is not in repoMissions, "skip".
//  3. If repo-tree contract.yaml exists, "noop" (idempotent).
//  4. Stage all four artifacts in a sibling temp directory.
//  5. Rename temp → <repoDir>/<id>. This is the commit point.
//  6. Remove the legacy files (best-effort — a stale legacy file
//     after a successful rename is benign because the repo-tree
//     contract wins on next read, but the legacy file is removed to
//     keep the migration one-shot per mission).
//
// On any error before step 5, the staging directory is removed and
// the legacy tree is untouched.
func migrateOneMission(legacyDir, repoDir, missionID string, repoMissions map[string]struct{}, dryRun bool) (string, error) {
	id := filepath.Base(missionID)
	if id == "" || id == "." || id == ".." {
		return "", fmt.Errorf("invalid mission id %q", missionID)
	}

	legacyContract := filepath.Join(legacyDir, id+".yaml")
	if _, err := os.Stat(legacyContract); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Sprintf("skip %s: legacy contract missing", id), nil
		}
		return "", fmt.Errorf("stat %s: %w", legacyContract, err)
	}

	if _, ok := repoMissions[id]; !ok {
		return fmt.Sprintf("skip %s: no repo session", id), nil
	}

	repoMissionDir := filepath.Join(repoDir, id)
	repoContract := filepath.Join(repoMissionDir, "contract.yaml")
	if _, err := os.Stat(repoContract); err == nil {
		return fmt.Sprintf("noop %s: already migrated", id), nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("stat %s: %w", repoContract, err)
	}

	if dryRun {
		return fmt.Sprintf("migrate %s -> %s (dry-run)", id, relRepoPath(repoMissionDir)), nil
	}

	if err := os.MkdirAll(repoDir, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", repoDir, err)
	}

	staging, err := os.MkdirTemp(repoDir, "."+id+".migrate-")
	if err != nil {
		return "", fmt.Errorf("creating staging dir: %w", err)
	}
	// Ensure the staging dir is cleaned up if anything below this
	// point fails before the rename.
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(staging)
		}
	}()

	for _, a := range missionArtifacts {
		src := filepath.Join(legacyDir, id+a.legacySuffix)
		dst := filepath.Join(staging, a.repoName)
		if err := copyIfExists(src, dst); err != nil {
			return "", fmt.Errorf("staging %s: %w", a.repoName, err)
		}
	}

	if err := os.Rename(staging, repoMissionDir); err != nil {
		return "", fmt.Errorf("rename %s -> %s: %w", staging, repoMissionDir, err)
	}
	committed = true

	// Remove the legacy files now that the repo-tree copy is the
	// authoritative version. A removal failure is non-fatal: the
	// migration has committed; a leftover legacy file is benign
	// because resolveLayer reads repo-first. Surface as part of the
	// status line in a future revision if it becomes a problem.
	for _, a := range missionArtifacts {
		legacy := filepath.Join(legacyDir, id+a.legacySuffix)
		if err := os.Remove(legacy); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("removing legacy %s: %w", legacy, err)
		}
	}

	return fmt.Sprintf("migrate %s -> %s", id, relRepoPath(repoMissionDir)), nil
}

// copyIfExists copies src to dst when src exists. A missing src is
// not an error — sibling artifacts (log.jsonl, results.yaml,
// reflections.yaml) are optional. The copy is whole-file: open both
// fds, io.Copy, fsync, close. The destination is created with mode
// 0o600 to match the contract file mode used by writeContract.
func copyIfExists(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("opening %s: %w", src, err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copying %s -> %s: %w", src, dst, err)
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", dst, err)
	}
	return nil
}

// relRepoPath formats the repo-tree mission directory as a relative
// path suitable for the migrate status line ("e.g.
// .ethos/missions/<id>"). Falls back to the absolute path on a
// filepath.Rel failure.
func relRepoPath(absPath string) string {
	idx := strings.Index(absPath, string(filepath.Separator)+".ethos"+string(filepath.Separator))
	if idx < 0 {
		return absPath
	}
	return absPath[idx+1:]
}
