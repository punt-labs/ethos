package mission

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/redact"
)

// maxAuditLineBytes is the per-line ceiling for collectContractIDs.
// Audit lines are short JSONL objects; 16 MiB matches the buffer that
// decodeAuditEntries uses for the same file shape and tolerates
// pathological lines without rejecting them outright.
const maxAuditLineBytes = 16 * 1024 * 1024

// MigrateMission relocates a mission from the legacy global tree
// (<globalRoot>/missions/<id>.yaml + sibling .jsonl / .results.yaml /
// .reflections.yaml files) into the DES-054 per-repo tree
// (<repoRoot>/.punt-labs/ethos/missions/<id>/contract.yaml + log.jsonl /
// results.yaml / reflections.yaml).
//
// When missionID is empty, every legacy mission whose contract_id is
// referenced anywhere in this repo's session audit trail (sealed
// chunks, the frozen legacy file, or a not-yet-sealed live tail — see
// repoMissionIDs) is migrated; missions with no matching session are
// left alone (cross-repo policy).
//
// checkoutRoot names the current work tree, distinct from repoRoot (the
// store root — the main work tree) when the caller is running from a
// linked worktree. It exists so the ownership scan also covers the
// worktree's own state, which repoRoot alone cannot see: the gitignored
// live zone (PR #508 round 4, finding H1) and, since a linked worktree
// on an unmerged branch can carry sealed chunks the main tree's own
// working copy does not, the sealed zone too (PR #508 round 7, finding
// J1). Pass "" (or equal to repoRoot) when there is no separate
// checkout to offer, or when the caller does not track one — the scan
// then covers repoRoot only, unchanged from before this parameter
// existed.
//
// When missionID is non-empty, only that one mission is considered.
// The cross-repo policy still applies — an explicit mission-id with
// no repo session referencing it is reported as "skip <id>: no repo
// session" so an operator cannot accidentally relocate another
// repo's mission by naming the ID. A mission already migrated
// (contract.yaml present in the repo tree) is a no-op. A mission
// whose legacy contract is missing is reported as such and not an
// error.
//
// dryRun=true enumerates what would change without writing or
// deleting. The move is atomic at the per-mission directory level:
// sibling artifacts are staged under a sibling temp directory in
// <repoRoot>/.punt-labs/ethos/missions/ and renamed into place, then the legacy
// files are removed. On any error before the temp→final rename, the
// legacy tree is untouched and the staging directory is cleaned up.
//
// out receives one human-readable line per mission decision:
//
//	migrate <mission-id> -> .punt-labs/ethos/missions/<id>
//	skip <mission-id>: no repo session
//	skip <mission-id>: legacy contract missing
//	noop <mission-id>: already migrated
//
// A successful run with no candidates prints "nothing to migrate".
func MigrateMission(globalRoot, repoRoot, checkoutRoot, missionID string, dryRun bool, out io.Writer) error {
	if repoRoot == "" {
		return fmt.Errorf("migrate mission: repoRoot is empty")
	}
	if globalRoot == "" {
		return fmt.Errorf("migrate mission: globalRoot is empty")
	}

	legacyDir := filepath.Join(globalRoot, "missions")
	repoDir := RepoStatePath(repoRoot, "missions")

	candidates, err := enumerateMigrateCandidates(legacyDir, missionID)
	if err != nil {
		return fmt.Errorf("enumerating legacy missions in %s: %w", legacyDir, err)
	}
	if len(candidates) == 0 {
		fmt.Fprintln(out, "nothing to migrate")
		return nil
	}

	repoMissions, repoErr := repoMissionIDs(repoRoot, checkoutRoot)
	if repoErr != nil {
		return fmt.Errorf("scanning repo sessions for mission references: %w", repoErr)
	}

	var failures []string
	for _, id := range candidates {
		decision, err := migrateOneMission(legacyDir, repoDir, repoRoot, id, repoMissions, dryRun)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ethos: mission migrate: %s: %v\n", id, err)
			failures = append(failures, id)
			continue
		}
		fmt.Fprintln(out, decision)
	}
	if len(failures) > 0 {
		return fmt.Errorf("mission migrate: %d mission(s) failed: %s",
			len(failures), strings.Join(failures, ", "))
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
// contract_id anywhere in this repo's own session audit trail: sealed
// chunks (git-tracked, `audit-<first>-<last>.jsonl` under
// `<root>/.punt-labs/ethos/sessions/<dir>/`), the frozen pre-DES-058
// legacy file (`audit.jsonl`, read directly if a session directory
// still carries one), and the live tail of a session that has not
// sealed yet (`<root>/.punt-labs/local/ethos/sessions/<id>.audit.jsonl`,
// gitignored). Missing sessions trees are treated as empty — a fresh
// repo has no audit history and therefore no migration candidates.
//
// PR #508 round 3, finding G1: the original version of this function
// read ONLY the frozen legacy `audit.jsonl` path — correct before
// DES-058 introduced the live/sealed split, but `ethos audit seal`
// runs at every pre-commit in an ethos-enabled repo (the sealed chunks
// travel in the same commit as the work), which moves content OUT of
// that flat file and into dated chunks on essentially every commit. A
// session whose audit trail has ever been sealed — the NORMAL state
// for an actively-committed repo, not an edge case — was invisible to
// this scan, which silently reopened the exact gap this function
// exists to close for `Store.conflictScanIDs` (F4) and for
// `MigrateMission` alike.
//
// The scan is best-effort at the SEALED-CHUNK level: a malformed
// individual line is skipped (the permissive reader contract in
// collectContractIDs), and a session directory whose chunk names fail
// to classify is warned to stderr and skipped rather than failing the
// whole scan — this result feeds Store.Create's admission control on
// every call, and a single damaged historical chunk (the concern
// `ethos audit quarantine` exists to fix, asynchronously) must not
// block every future mission create in the repo. The frozen legacy
// file and the live tail keep their pre-existing, stricter contract: a
// genuine read error there still propagates, since each is a single,
// currently-relevant file rather than an unbounded pool of historical
// chunks.
// checkoutRoot names the current work tree (resolve.EnvRepoRoot /
// FindRepoRoot), distinct from repoRoot (the store root — the main
// work tree) when the caller is running from a linked worktree. Both
// the sealed scan and the live scan below cover repoRoot AND, when
// different, checkoutRoot — see collectSealedContractIDs and
// collectLiveContractIDs' own callers in repoMissionIDs. Pass "" when
// the caller has no separate checkout root to offer (legacy
// single-tree callers, or a caller already running from the main
// tree) — both scans then cover repoRoot only.
//
// "Git-tracked" is not "identical across every checkout": it means
// identical AT THE SAME COMMIT. A linked worktree on an unmerged
// branch has sealed chunks committed to that branch which the main
// tree's own working copy of .punt-labs/ethos/sessions/ does not
// carry — that divergence is exactly why the SEALED scan needs both
// roots too, not just the live one.
//
// PR #508 round 4, finding H1: a session running inside a linked
// worktree writes its live (not-yet-sealed) audit file under THAT
// worktree's own .punt-labs/local/ethos/sessions/, never under the
// main tree StoreRepoRoot points at — so a repoRoot-only live scan
// is blind to exactly the most recent, most likely-to-be-open
// sessions when the caller (or `mission create`/`dispatch`) is
// itself running from a worktree, which per the leader's own report
// is the common case, not an edge one.
//
// PR #508 round 7, finding J1: the round-4 fix above stopped one bit
// short — it widened the LIVE scan to both roots but left the SEALED
// scan reading repoRoot only, on the (measured-false) assumption that
// git-tracked meant checkout-independent. A mission whose only
// ownership evidence sealed onto an unmerged branch was invisible to
// admission control under that gap: it had left the live tail (it
// sealed) and never reached the main tree's sealed history (unmerged)
// — the exact F4 false-negative class this function exists to close,
// reopened one layer down. Fixed by widening the sealed scan the same
// way the live scan was already widened.
//
// This is the third bug this repo has had on "where does per-checkout
// state live relative to the store root" (ethos-yofr/ethos-5yej for
// identity/team/role resolution; PR #370's Bugbot finding — store.go's
// checkoutRoot/auditRoot fields exist to fix it — for the DES-058
// live-audit-zone split; and now this, twice, in the same function).
func repoMissionIDs(repoRoot, checkoutRoot string) (map[string]struct{}, error) {
	out := make(map[string]struct{})

	if err := collectSealedContractIDs(repoRoot, out); err != nil {
		return nil, err
	}
	if err := collectLiveContractIDs(repoRoot, out); err != nil {
		return nil, err
	}
	if checkoutRoot != "" && checkoutRoot != repoRoot {
		// PR #508 round 7, finding J1: git-tracked does not mean
		// "identical across every checkout" — it means "identical at
		// the same commit." A linked worktree on an unmerged branch
		// has sealed chunks committed to that branch which the main
		// tree's OWN working copy of .punt-labs/ethos/sessions/ does
		// not carry, symmetric with the live zone's per-checkout split
		// above. Scanning root only would leave a mission whose sole
		// ownership evidence sealed onto that unmerged branch
		// invisible to admission control — it has left the live tail
		// (it sealed) and never reached root's sealed tree (unmerged)
		// — reopening the exact F4 false-negative class this function
		// exists to close.
		if err := collectSealedContractIDs(checkoutRoot, out); err != nil {
			return nil, err
		}
		if err := collectLiveContractIDs(checkoutRoot, out); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// collectSealedContractIDs scans root's sealed session audit trail —
// sealed chunks (audit.ScanSealedDir) and the frozen pre-DES-058
// legacy audit.jsonl, both under RepoStatePath(root, "sessions") —
// for contract_id references, adding them to dst. A missing sessions
// tree is not an error — a fresh repo (or a checkout that has sealed
// nothing of its own) has no sealed history yet.
func collectSealedContractIDs(root string, dst map[string]struct{}) error {
	sessionsBase := RepoStatePath(root, "sessions")
	dirs, err := os.ReadDir(sessionsBase)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("reading %s: %w", sessionsBase, err)
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		sealedDir := filepath.Join(sessionsBase, d.Name())

		sc, scErr := audit.ScanSealedDir(sealedDir, audit.SessionNS, "")
		if scErr != nil {
			fmt.Fprintf(os.Stderr,
				"ethos: mission: scanning sealed audit chunks in %s: %v\n", sealedDir, scErr)
		} else {
			for _, c := range sc.Chunks {
				chunkPath := filepath.Join(sealedDir, c.ChunkFile())
				if cErr := collectContractIDs(chunkPath, dst); cErr != nil {
					fmt.Fprintf(os.Stderr, "ethos: mission: %s: %v\n", chunkPath, cErr)
				}
			}
		}

		legacyPath := filepath.Join(sealedDir, "audit.jsonl")
		if err := collectContractIDs(legacyPath, dst); err != nil {
			return fmt.Errorf("scanning %s: %w", legacyPath, err)
		}
	}
	return nil
}

// collectLiveContractIDs scans root's live (not-yet-sealed) session
// audit files for contract_id references, adding them to dst. A
// missing live-sessions directory is not an error — every session in
// scope has either sealed or never started.
func collectLiveContractIDs(root string, dst map[string]struct{}) error {
	liveDirs, err := os.ReadDir(audit.LiveSessionsDir(root))
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("reading %s: %w", audit.LiveSessionsDir(root), err)
	}
	for _, f := range liveDirs {
		if f.IsDir() {
			continue
		}
		id, ok := strings.CutSuffix(f.Name(), ".audit.jsonl")
		if !ok {
			continue
		}
		livePath := audit.LiveAuditPath(root, id)
		if err := collectContractIDs(livePath, dst); err != nil {
			return fmt.Errorf("scanning %s: %w", livePath, err)
		}
	}
	return nil
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
//
// The warning is prefixed "ethos: mission:", not "ethos: mission
// migrate:" (PR #508 round 4, finding H2): this function is shared by
// repoMissionIDs, which now runs from Store.conflictScanIDs during
// `mission create`/`dispatch` admission control, not only from
// `mission migrate`. An operator running `create` who sees a "mission
// migrate" warning would reasonably conclude a migration is running —
// the same class of misdirection as a stale comment describing a code
// path the function no longer takes, just aimed at an operator instead
// of a reader.
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
				"ethos: mission: %s: line %d: skipping malformed line: %v\n",
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
func migrateOneMission(legacyDir, repoDir, repoRoot, missionID string, repoMissions map[string]struct{}, dryRun bool) (string, error) {
	id := filepath.Base(missionID)
	if id == "" || id == "." || id == ".." {
		return "", fmt.Errorf("invalid mission id %q", missionID)
	}

	legacyContract := filepath.Join(legacyDir, id+".yaml")
	if _, err := os.Stat(legacyContract); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Sprintf("skip %s: legacy contract missing", id), nil
		}
		return "", ioErr("stat", legacyContract, err)
	}

	if _, ok := repoMissions[id]; !ok {
		return fmt.Sprintf("skip %s: no repo session", id), nil
	}

	repoMissionDir := filepath.Join(repoDir, id)
	repoContract := filepath.Join(repoMissionDir, "contract.yaml")
	if _, err := os.Stat(repoContract); err == nil {
		return fmt.Sprintf("noop %s: already migrated", id), nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", ioErr("stat", repoContract, err)
	}
	// Destination directory might already exist (e.g. from a prior
	// partial migration that wrote sibling artifacts but never
	// landed contract.yaml). os.Rename onto an existing dir fails
	// with a generic "file exists" — surface a clearer skip line
	// instead so the operator knows to either remove the stray
	// dir or move it aside manually (Copilot on PR #328).
	if info, err := os.Stat(repoMissionDir); err == nil && info.IsDir() {
		return fmt.Sprintf(
			"skip %s: repo-tree dir already present at %s (no contract.yaml; manual cleanup required)",
			id, relRepoPath(repoMissionDir)), nil
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", ioErr("stat", repoMissionDir, err)
	}

	if dryRun {
		return fmt.Sprintf("migrate %s -> %s (dry-run)", id, relRepoPath(repoMissionDir)), nil
	}

	if err := os.MkdirAll(repoDir, 0o700); err != nil {
		return "", ioErr("creating", repoDir, err)
	}

	r, err := NewPathRedactor(repoRoot)
	if err != nil {
		return "", fmt.Errorf("migrate %s: %w", id, err)
	}

	staging, err := os.MkdirTemp(repoDir, "."+id+".migrate-")
	if err != nil {
		return "", ioErr("creating staging dir in", repoDir, err)
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
		if err := copyIfExists(r, src, dst); err != nil {
			return "", fmt.Errorf("staging %s: %w", a.repoName, err)
		}
	}

	if err := os.Rename(staging, repoMissionDir); err != nil {
		return "", ioErr("renaming the staging dir to", repoMissionDir, err)
	}
	committed = true

	// Remove the legacy files now that the repo-tree copy is the
	// authoritative version. A removal failure is non-fatal: the
	// migration has committed; a leftover legacy file is benign
	// because resolveLayer reads repo-first. Log the failure to
	// stderr so the operator can clean up manually if desired,
	// but continue past it — the migration as a whole succeeded
	// (Bugbot MED on PR #328: previously returned a hard error
	// here, contradicting the "non-fatal" intent).
	for _, a := range missionArtifacts {
		legacy := filepath.Join(legacyDir, id+a.legacySuffix)
		if err := os.Remove(legacy); err != nil && !errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(os.Stderr,
				"ethos: mission migrate: %v (leftover is benign; repo-tree copy is authoritative)\n",
				ioErr("removing legacy", legacy, err))
		}
	}

	return fmt.Sprintf("migrate %s -> %s", id, relRepoPath(repoMissionDir)), nil
}

// copyIfExists copies src to dst through the redactor when src exists. A
// missing src is not an error — sibling artifacts (log.jsonl,
// results.yaml, reflections.yaml) are optional. The destination is
// created with mode 0o600 to match the contract file mode used by
// writeContract.
//
// The legacy tree is untracked and was written by an older ethos, the
// repo tree is tracked, so the copy is where unredacted content would
// cross over. The redaction is structural, not a regexp over the bytes:
// a credential pattern that ran to the next whitespace would swallow a
// closing quote and corrupt the file. A file that cannot be parsed
// cannot be redacted and is not copied.
func copyIfExists(r PathRedactor, src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return ioErr("reading", src, err)
	}
	hint := "fix the file and re-run `ethos mission migrate`"
	var red []byte
	var rewritten bool
	switch {
	case strings.HasSuffix(src, ".jsonl"):
		hint = "fix or remove the line and re-run `ethos mission migrate`"
		red, rewritten, err = redactJSONLines(r.Content, r.Path, reflect.TypeOf(Event{}), data)
	default:
		// The record's type gives the modes, as in the writer: enforcement
		// fields get the path pass only and IDs and enums are kept.
		red, rewritten, err = redactYAMLDoc(r.Content, r.Path, artifactType(filepath.Base(dst)), data)
		if err == nil && filepath.Base(dst) == "contract.yaml" {
			err = checkScrubbedContract(data, red)
		}
	}
	data = red
	if err != nil {
		return fmt.Errorf("redacting %s: %w; %s", filepath.Base(src), err, hint)
	}
	if err := writeSyncedFile(dst, data); err != nil {
		return err
	}
	if rewritten {
		announceRewritten(fmt.Sprintf("the migrated copy of %s", safeFieldName(filepath.Base(src))), redactNoRename).Say()
	}
	return nil
}

// writeSyncedFile creates path at 0o600 with data, fsynced.
func writeSyncedFile(path string, data []byte) error {
	var gErr error
	if data, gErr = guardWrite(path, data); gErr != nil {
		return gErr
	}
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return ioErr("creating", path, err)
	}
	if _, err := out.Write(data); err != nil {
		_ = out.Close()
		return ioErr("writing", path, err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return ioErr("syncing", path, err)
	}
	// Check Close() explicitly: a failed close after a successful
	// Sync would otherwise be silently dropped by the deferred
	// pattern, and the migration would report success on a file
	// that's not fully committed to disk (Copilot on PR #328).
	if err := out.Close(); err != nil {
		return ioErr("closing", path, err)
	}
	return nil
}

// redactMode says how the redactor treats a YAML value, taken from the
// redact tag of the struct field it decodes into.
type redactMode int

const (
	modeContent redactMode = iota // the content pass (f)
	modePath                      // the path pass (p) alone
	modeKeep                      // left byte-for-byte
)

// redactYAMLDoc re-encodes a YAML document with f applied to every
// string scalar and comment, and p to those under a `redact:"path"`
// field. Keys, numbers, booleans and structure are untouched. It reports
// whether anything changed; a caller that rewrites in place skips an
// unchanged file so its formatting is left alone. An empty document stays
// empty. A file holding more than one document is refused: re-encoding
// the first would drop the others.
//
// typ is the Go type the document decodes into, or nil when it has none.
// The modes come from its redact tags, the same ones the record writers
// read, so a value the system relies on (an ID, an enum, a path) is
// treated exactly as the writer treats it. Keys typ does not know take
// the mode of their parent. With p == f and typ == nil every string gets
// the one pass.
func redactYAMLDoc(f, p func(string) string, typ reflect.Type, data []byte) ([]byte, bool, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return data, false, nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, false, fmt.Errorf("parsing yaml: %w", safeErr(err))
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, false, fmt.Errorf("parsing yaml: %w", safeErr(err))
		}
		return nil, false, fmt.Errorf("parsing yaml: multiple documents (only one is supported)")
	}
	budget := maxYAMLNodes
	if err := expandAliases(&doc, &budget); err != nil {
		return nil, false, err
	}
	typ = recordType(typ, &doc)
	w := yamlRedactor{f: f, p: p}
	w.walk(&doc, typ, modeContent, "")
	if w.leak != "" {
		return nil, false, fmt.Errorf("redacting yaml: field %s %s; remove or rephrase it", safeField(w.leak), w.rule)
	}
	if !w.changed {
		return data, false, nil
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, false, fmt.Errorf("encoding yaml: %w", err)
	}
	if err := checkPathFieldsDoc(p, typ, data, out); err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// recordType is the type a document decodes into. A legacy results or
// reflections file is a bare list of records, where the current file is a
// mapping holding that list; typ names the mapping.
func recordType(typ reflect.Type, doc *yaml.Node) reflect.Type {
	if typ == nil || typ.Kind() != reflect.Struct || typ.NumField() != 1 || typ.Field(0).Type.Kind() != reflect.Slice {
		return typ
	}
	if len(doc.Content) == 1 && doc.Content[0].Kind == yaml.SequenceNode {
		return typ.Field(0).Type
	}
	return typ
}

// maxYAMLNodes bounds the nodes expandAliases may create, so a document
// whose aliases nest exponentially is refused rather than expanded.
const maxYAMLNodes = 100000

// expandAliases replaces every alias in n with an independent copy of the
// node it names and drops the anchors. The walk redacts each node by the
// field it sits under, and a node shared between two fields cannot be in
// two modes: `write_set: *shared` would be redacted as prose along with
// the `success_criteria: &shared` it points to, and the write set would
// change. The decoded value of the document is the same afterwards.
func expandAliases(n *yaml.Node, budget *int) error {
	if *budget--; *budget < 0 {
		return fmt.Errorf("parsing yaml: aliases expand to more than %d nodes", maxYAMLNodes)
	}
	if n.Kind == yaml.AliasNode {
		if n.Alias == nil {
			return fmt.Errorf("parsing yaml: alias %q has no anchor", n.Value)
		}
		*n = cloneYAMLNode(n.Alias)
	}
	n.Anchor = ""
	for _, c := range n.Content {
		if err := expandAliases(c, budget); err != nil {
			return err
		}
	}
	return nil
}

func cloneYAMLNode(n *yaml.Node) yaml.Node {
	c := *n
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, k := range n.Content {
		kc := cloneYAMLNode(k)
		c.Content[i] = &kc
	}
	return c
}

// checkPathFieldsDoc refuses a redacted document in which a path-tagged
// field of typ differs from the path pass applied alone to the original:
// those fields are acted on, so no other pass may have touched them,
// however the document reached them (a merge key, an alias, a legacy
// name). A document that does not decode into typ cannot be judged and is
// refused; with no typ there are no path fields.
func checkPathFieldsDoc(p func(string) string, typ reflect.Type, orig, red []byte) error {
	if typ == nil {
		return nil
	}
	before := reflect.New(typ).Interface()
	if err := yaml.Unmarshal(orig, before); err != nil {
		return fmt.Errorf("original does not decode as %s, so its path fields cannot be checked: %w", typ, safeErr(err))
	}
	after := reflect.New(typ).Interface()
	if err := yaml.Unmarshal(red, after); err != nil {
		return fmt.Errorf("redacted yaml does not decode as %s: %w", typ, safeErr(err))
	}
	want := redact.RecordPaths(before, func(s string) string { return s }, p)
	if diffs := redact.PathDiffs(want, after); len(diffs) > 0 {
		return fmt.Errorf("redaction altered enforcement fields: %s", strings.Join(diffs, ", "))
	}
	return nil
}

type yamlRedactor struct {
	f, p    func(string) string
	changed bool
	leak    string // the first field whose redacted value still names a home
	rule    string // what it breaks
}

// safeField is a field name fit for an error: a key can itself be the leak,
// and one that is not can hold a control character, so the name is quoted
// with %q.
func safeField(name string) string {
	if processLeaks(name) {
		return "(a field whose name is a home path)"
	}
	return fmt.Sprintf("%q", name)
}

// check records name as the leaking field if v, a value the writer is about
// to emit, still names a home directory.
func (w *yamlRedactor) check(v, name string, path bool) {
	if r := LeakRuleFor(v, path); w.leak == "" && r != "" {
		w.leak, w.rule = name, r
	}
}

func (w *yamlRedactor) pass(mode redactMode) func(string) string {
	if mode == modePath {
		return w.p
	}
	return w.f
}

// comments applies the content pass to n's head, line and foot comments.
// A comment is prose, never a value the system acts on, even beside a path
// or kept field; and yaml.v3 attaches a trailing comment to the preceding
// key, so the field's own pass would let it leak.
func (w *yamlRedactor) comments(n *yaml.Node) {
	for _, c := range []*string{&n.HeadComment, &n.LineComment, &n.FootComment} {
		if v := w.f(*c); v != *c {
			*c, w.changed = v, true
		}
		w.check(*c, "comment", false)
	}
}

func (w *yamlRedactor) walk(n *yaml.Node, t reflect.Type, mode redactMode, name string) {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	w.comments(n)
	// A custom tag is text the writer emits as written.
	if !strings.HasPrefix(n.Tag, "!!") {
		w.check(n.Tag, name, false)
	}
	switch n.Kind {
	case yaml.ScalarNode:
		if mode == modeKeep {
			// Kept, not rewritten, but still judged: a kept field that
			// names a home refuses the file.
			w.check(n.Value, name, false)
			return
		}
		if n.Tag != "!!str" {
			// Not rewritten, but still judged: an unknown tag decodes to
			// the string as written, and a !!binary holds one in base64.
			w.check(n.Value, name, false)
			if n.Tag == "!!binary" {
				if raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(n.Value), "")); err == nil {
					w.check(string(raw), name, false)
				}
			}
			return
		}
		if v := w.pass(mode)(n.Value); v != n.Value {
			n.Value, w.changed = v, true
		}
		w.check(n.Value, name, mode == modePath)
	case yaml.MappingNode:
		w.walkMapping(n, t, mode, name)
	case yaml.SequenceNode:
		var et reflect.Type
		if t != nil && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) {
			et = t.Elem()
		}
		for i, c := range n.Content {
			w.walk(c, et, mode, fmt.Sprintf("%s[%d]", name, i))
		}
	default:
		for _, c := range n.Content {
			w.walk(c, t, mode, name)
		}
	}
}

func (w *yamlRedactor) walkMapping(n *yaml.Node, t reflect.Type, mode redactMode, name string) {
	fields := yamlFields(t)
	// Keys the type does not name are renamed by the pass; the plan keeps two
	// of them from merging into one name (redact.PlanKeys).
	var scalarKeys []string
	for i := 0; i+1 < len(n.Content); i += 2 {
		if k := n.Content[i]; k.Kind == yaml.ScalarNode {
			scalarKeys = append(scalarKeys, k.Value)
		}
	}
	keyNames, _ := redact.PlanKeys(scalarKeys, func(k string) string {
		if _, known := fields[k]; known || mode == modeKeep {
			return k
		}
		return w.pass(mode)(k)
	}, false)
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, val := n.Content[i], n.Content[i+1]
		km, vt := mode, reflect.Type(nil)
		if t != nil && t.Kind() == reflect.Map {
			vt = t.Elem()
		}
		if sf, ok := fields[k.Value]; ok && k.Kind == yaml.ScalarNode {
			vt = sf.Type
			switch tag := sf.Tag.Get("redact"); {
			case mode == modeKeep:
			case tag == "keep":
				km = modeKeep
			case tag == "path":
				km = modePath
			}
		}
		w.comments(k)
		if k.Kind != yaml.ScalarNode {
			// A key can be a sequence or a mapping; its scalars are data too.
			w.walk(k, nil, mode, name)
		}
		if _, known := fields[k.Value]; !known && mode != modeKeep && k.Kind == yaml.ScalarNode {
			// A key the type does not name is data, and can hold a home as a
			// value can.
			if v := keyNames[k.Value]; v != k.Value && k.Tag == "!!str" {
				k.Value, w.changed = v, true
			}
			w.check(k.Value, "(a key)", false)
		}
		if km != modeKeep && k.Kind == yaml.ScalarNode && redact.IsSecretKey(k.Value) && secretYAMLValue(val) {
			*val = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: redact.SecretToken}
			w.changed = true
			continue
		}
		sub := k.Value
		if name != "" {
			sub = name + "." + k.Value
		}
		w.walk(val, vt, km, sub)
	}
}

// yamlFields maps each YAML key of struct type t to its field. Other
// types, and nil, have none. Inputs.UnmarshalYAML also reads the legacy key
// bead as the ticket, so it is the same field here.
func yamlFields(t reflect.Type) map[string]reflect.StructField {
	out := structFields(t, "yaml")
	if t == reflect.TypeOf(Inputs{}) {
		out["bead"] = out["ticket"]
	}
	return out
}

// structFields maps each key of struct type t, named by its tag (yaml or
// json), to its field. Other types, and nil, have none.
func structFields(t reflect.Type, tag string) map[string]reflect.StructField {
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}
	out := make(map[string]reflect.StructField, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(sf.Tag.Get(tag), ",")
		switch name {
		case "-":
			continue
		case "":
			name = strings.ToLower(sf.Name)
		}
		out[name] = sf
	}
	return out
}

// checkScrubbedContract refuses a redacted contract file that no longer
// validates, as the contract writer does. It judges only a contract that
// was valid to begin with: an archetype may relax rules Validate applies
// without one, and a file that was already invalid cannot be made worse. A
// contract that does not decode cannot be judged at all and is refused.
func checkScrubbedContract(orig, red []byte) error {
	var before, after Contract
	if err := yaml.Unmarshal(orig, &before); err != nil {
		return fmt.Errorf("contract does not decode, so its redaction cannot be checked: %w", safeErr(err))
	}
	if before.Validate() != nil {
		return nil
	}
	if err := yaml.Unmarshal(red, &after); err != nil {
		return fmt.Errorf("redacted contract does not parse: %w", safeErr(err))
	}
	return checkRedactedContract(after)
}

// secretYAMLValue reports whether the value under a credential-named key
// is replaced whole: a non-empty string or number, or a non-empty list.
// Booleans, nulls, empty values, mappings and already-redacted values are
// not.
func secretYAMLValue(v *yaml.Node) bool {
	switch v.Kind {
	case yaml.ScalarNode:
		switch v.Tag {
		case "!!bool", "!!null":
			return false
		}
		return v.Value != "" && v.Value != redact.SecretToken
	case yaml.SequenceNode:
		return len(v.Content) > 0
	}
	return false
}

// redactJSONLines applies f to every string value of every line of a
// JSONL body, and to nothing else; a string under a credential-named key
// is replaced whole. A line that f leaves alone keeps its original
// bytes, and a changed line keeps its key order, so redacting a sealed
// log changes only the lines that leaked and only the leaked strings.
// It reports whether any line changed.
//
// typ is the Go type each line decodes into, or nil. Its redact tags give
// the modes, as in redactYAMLDoc: p alone for a path field, nothing for a
// keep field.
func redactJSONLines(f, p func(string) string, typ reflect.Type, data []byte) ([]byte, bool, error) {
	lines := bytes.Split(data, []byte("\n"))
	changed := false
	for i, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		v, err := readOrdered(dec)
		if err != nil {
			return nil, false, fmt.Errorf("line %d: parsing json: %w", i+1, safeErr(err))
		}
		if _, err := dec.Token(); err != io.EOF {
			return nil, false, fmt.Errorf("line %d: parsing json: trailing content", i+1)
		}
		st := &jsonState{}
		red, c := redactOrdered(f, p, v, "", typ, modeContent, "", st)
		if st.leak != "" {
			return nil, false, fmt.Errorf("line %d: redacting json: field %s %s; remove or rephrase it", i+1, safeField(st.leak), st.rule)
		}
		if !c {
			continue
		}
		var b bytes.Buffer
		if err := writeOrdered(&b, red); err != nil {
			return nil, false, fmt.Errorf("line %d: encoding json: %w", i+1, err)
		}
		lines[i], changed = b.Bytes(), true
	}
	if !changed {
		return data, false, nil
	}
	return bytes.Join(lines, []byte("\n")), true, nil
}

// ojson is a decoded JSON value that remembers object key order.
type ojson struct {
	kind byte // 'o' object, 'a' array, 's' string, 'l' literal (number, bool, null)
	keys []string
	vals []*ojson
	str  string
	lit  string
}

func readOrdered(dec *json.Decoder) (*ojson, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := &ojson{kind: 'o'}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("object key %v is not a string", kt)
				}
				v, err := readOrdered(dec)
				if err != nil {
					return nil, err
				}
				o.keys, o.vals = append(o.keys, k), append(o.vals, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			a := &ojson{kind: 'a'}
			for dec.More() {
				v, err := readOrdered(dec)
				if err != nil {
					return nil, err
				}
				a.vals = append(a.vals, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return a, nil
		}
		return nil, fmt.Errorf("unexpected delimiter %v", t)
	case string:
		return &ojson{kind: 's', str: t}, nil
	case json.Number:
		return &ojson{kind: 'l', lit: t.String()}, nil
	case bool:
		return &ojson{kind: 'l', lit: fmt.Sprint(t)}, nil
	case nil:
		return &ojson{kind: 'l', lit: "null"}, nil
	}
	return nil, fmt.Errorf("unexpected token %v", tok)
}

// jsonState carries the first field whose redacted value still names a home.
type jsonState struct{ leak, rule string }

func (st *jsonState) check(v, name string, path bool) {
	if r := LeakRuleFor(v, path); st.leak == "" && r != "" {
		st.leak, st.rule = name, r
	}
}

// redactOrdered returns v with f applied to every string, or p under a
// path field and nothing under a keep field. key is the object key v sits
// under, which makes a non-empty string under a credential-named key a
// secret by name; t is the Go type v decodes into, or nil.
func redactOrdered(f, p func(string) string, v *ojson, key string, t reflect.Type, mode redactMode, name string, st *jsonState) (*ojson, bool) {
	if mode == modeKeep {
		return v, false
	}
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	pass := f
	if mode == modePath {
		pass = p
	}
	switch v.kind {
	case 's':
		r := pass(v.str)
		if key != "" && v.str != "" && redact.IsSecretKey(key) {
			r = redact.SecretToken
		}
		st.check(r, name, mode == modePath)
		return &ojson{kind: 's', str: r}, r != v.str
	case 'l':
		if key != "" && redact.IsSecretKey(key) && v.lit != "null" && v.lit != "true" && v.lit != "false" {
			return &ojson{kind: 's', str: redact.SecretToken}, true
		}
	case 'o', 'a':
		if v.kind == 'a' && key != "" && len(v.vals) > 0 && redact.IsSecretKey(key) {
			return &ojson{kind: 's', str: redact.SecretToken}, true
		}
		fields := structFields(t, "json")
		var et reflect.Type
		if t != nil {
			switch {
			case v.kind == 'a' && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array), t.Kind() == reflect.Map:
				et = t.Elem()
			}
		}
		out := &ojson{kind: v.kind, keys: v.keys, vals: make([]*ojson, len(v.vals))}
		changed := false
		var keyNames map[string]string
		if v.kind == 'o' {
			keyNames, _ = redact.PlanKeys(v.keys, func(k string) string {
				if _, known := fields[k]; known {
					return k
				}
				return pass(k)
			}, false)
		}
		for i, c := range v.vals {
			k, ct, cm, cn := "", et, mode, fmt.Sprintf("%s[%d]", name, i)
			if v.kind == 'o' {
				k = v.keys[i]
				// A key the type does not name is data, and can hold a home
				// as a value can.
				if _, known := fields[k]; !known {
					if nk := keyNames[k]; nk != k {
						if &out.keys[0] == &v.keys[0] {
							out.keys = append([]string(nil), v.keys...)
						}
						out.keys[i], k, changed = nk, nk, true
					}
					st.check(k, "(a key)", false)
				}
				cn = k
				if name != "" {
					cn = name + "." + k
				}
				if sf, ok := fields[k]; ok {
					ct = sf.Type
					switch sf.Tag.Get("redact") {
					case "keep":
						cm = modeKeep
					case "path":
						cm = modePath
					}
				}
			}
			rc, cc := redactOrdered(f, p, c, k, ct, cm, cn, st)
			out.vals[i], changed = rc, changed || cc
		}
		return out, changed
	}
	return v, false
}

func writeOrdered(b *bytes.Buffer, v *ojson) error {
	switch v.kind {
	case 's':
		s, err := json.Marshal(v.str)
		if err != nil {
			return err
		}
		b.Write(s)
	case 'l':
		b.WriteString(v.lit)
	case 'a':
		b.WriteByte('[')
		for i, c := range v.vals {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeOrdered(b, c); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case 'o':
		b.WriteByte('{')
		for i, c := range v.vals {
			if i > 0 {
				b.WriteByte(',')
			}
			k, err := json.Marshal(v.keys[i])
			if err != nil {
				return err
			}
			b.Write(k)
			b.WriteByte(':')
			if err := writeOrdered(b, c); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	}
	return nil
}

// relRepoPath formats the repo-tree mission directory as a relative
// path suitable for the migrate status line ("e.g.
// .punt-labs/ethos/missions/<id>"). Falls back to the absolute path
// on a filepath.Rel failure.
func relRepoPath(absPath string) string {
	needle := string(filepath.Separator) + ".punt-labs" + string(filepath.Separator) + "ethos" + string(filepath.Separator)
	idx := strings.Index(absPath, needle)
	if idx < 0 {
		return absPath
	}
	return absPath[idx+1:]
}
