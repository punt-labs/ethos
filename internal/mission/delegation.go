//go:build !windows

package mission

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// Tier values for a Delegation. DES-054 v5: tier-A is ungoverned
// (bare Agent spawn, audit only); tier-B is contract-governed.
const (
	TierA = "A"
	TierB = "B"
)

// Delegation verdict values. Tier A delegations close only with the
// "aborted" verdict (or stay "open" forever — they have no evaluator);
// Tier B delegations close with one of pass/fail/error/aborted.
//
// Distinct from Result.Verdict (in result.go): a Result is the
// worker's structured handoff for a mission round and reuses
// VerdictPass/VerdictFail/VerdictEscalate. A Delegation is a single
// Agent spawn and uses its own enum because Tier A delegations have
// no evaluator and therefore no pass/fail path. The DelegationVerdict
// prefix keeps the two enums distinct at every call site.
const (
	DelegationVerdictOpen    = "open"
	DelegationVerdictPass    = "pass"
	DelegationVerdictFail    = "fail"
	DelegationVerdictError   = "error"
	DelegationVerdictAborted = "aborted"
)

// MaxDelegationDepthDefault is the depth ceiling applied when
// `.punt-labs/ethos.yaml` does not set `max_delegation_depth`.
// DES-054 v5: every Agent spawn walks the parent_delegation chain
// and refuses when adding one more level would exceed this limit.
// 16 leaves room for orchestrator → worker → reviewer → fixer →
// follow-up chains without running into a runaway recursive spawn
// pattern.
const MaxDelegationDepthDefault = 16

// DelegationTemplate is the per-spawn entry on a Tier B contract.
// When a parent delegation's contract carries a DelegationTemplate
// whose SpawnPattern matches the agent_type of a new Agent call,
// the new spawn inherits the parent contract (Tier B by inheritance)
// per DES-054 v5 §"PreToolUse-on-Agent" dispatch rule (a).
//
// SpawnPattern is a regular expression anchored at both ends.
// Empty pattern never matches. Patterns are user-authored; a
// malformed pattern surfaces at admission time, not at hook fire
// time, so an unparseable regex on disk does not silently let
// every spawn pass.
type DelegationTemplate struct {
	Role             string   `yaml:"role" json:"role"`
	SpawnPattern     string   `yaml:"spawn_pattern" json:"spawn_pattern"`
	InheritsContract bool     `yaml:"inherits_contract,omitempty" json:"inherits_contract,omitempty"`
	ExtractInto      []string `yaml:"extract_into,omitempty" json:"extract_into,omitempty"`
}

// Delegation is the on-disk record for one Agent spawn. Tier B
// delegations live at
// `<repo>/.ethos/missions/<mission-id>/delegations/<NN>/record.yaml`;
// Tier A delegations live at
// `<repo>/.ethos/sessions/<YYYY-MM-DD>-<session-id>/adhoc/<NNN>/record.yaml`.
// One Delegation per spawn — the unit of operator intent. tool calls
// produced inside the spawn join the session audit log under this
// delegation_id.
//
// The record skeleton is written at PreToolUse-on-Agent time, before
// the worker process starts. Verdict and ClosedAt are set later by
// either the SubagentStop hook (Tier B success → pass), an explicit
// `ethos mission` close, or the depth/hash-gate refusal path which
// sets verdict=aborted before the worker ever runs.
type Delegation struct {
	ID               string    `yaml:"id" json:"id"`
	Tier             string    `yaml:"tier" json:"tier"`
	Mission          string    `yaml:"mission,omitempty" json:"mission,omitempty"`
	ContractID       string    `yaml:"contract_id,omitempty" json:"contract_id,omitempty"`
	ParentDelegation string    `yaml:"parent_delegation,omitempty" json:"parent_delegation,omitempty"`
	ParentSession    string    `yaml:"parent_session,omitempty" json:"parent_session,omitempty"`
	Session          string    `yaml:"session,omitempty" json:"session,omitempty"`
	AgentType        string    `yaml:"agent_type" json:"agent_type"`
	SpawnPattern     string    `yaml:"spawn_pattern,omitempty" json:"spawn_pattern,omitempty"`
	CreatedAt        string    `yaml:"created_at" json:"created_at"`
	ClosedAt         string    `yaml:"closed_at,omitempty" json:"closed_at,omitempty"`
	Verdict          string    `yaml:"verdict" json:"verdict"`
	PromptHash       string    `yaml:"prompt_hash,omitempty" json:"prompt_hash,omitempty"`
	Reason           string    `yaml:"reason,omitempty" json:"reason,omitempty"`
}

// MatchSpawnPattern reports whether agentType matches any of the
// templates' spawn_pattern values. The first matching template is
// returned; an empty pattern never matches. A pattern that fails
// to compile is treated as no-match and surfaces a stderr warning
// so the operator sees the bad regex — fail closed, never silently
// admit.
//
// Empty templates returns (nil, false). agentType empty returns
// (nil, false) — an Agent call with no agent_type cannot inherit a
// contract; the dispatch falls through to the MISSION_ID-explicit
// branch or to Tier A.
func MatchSpawnPattern(templates []DelegationTemplate, agentType string) (*DelegationTemplate, bool) {
	if agentType == "" {
		return nil, false
	}
	for i := range templates {
		t := &templates[i]
		if t.SpawnPattern == "" {
			continue
		}
		re, err := regexp.Compile("^(?:" + t.SpawnPattern + ")$")
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"ethos: delegation: bad spawn_pattern %q in role %q: %v\n",
				t.SpawnPattern, t.Role, err)
			continue
		}
		if re.MatchString(agentType) {
			return t, true
		}
	}
	return nil, false
}

// DelegationLockPath returns the per-delegation flock path under
// `~/.punt-labs/ethos/delegations/<delegation-id>.lock`. The lock
// directory is always under the global tree per DES-054 concurrency
// model: locks reference live inodes that must not move when the
// delegation record itself migrates trees, and two checkouts of the
// same repo must lock the same inode.
func (s *Store) DelegationLockPath(delegationID string) string {
	return filepath.Join(s.root, "delegations", filepath.Base(delegationID)+".lock")
}

// AcquireDelegationLock opens (and creates if needed) the per-
// delegation lock file at `<globalRoot>/delegations/<id>.lock` and
// acquires an exclusive flock on it. The returned release closure
// runs LOCK_UN + Close exactly once; subsequent calls are no-ops.
//
// globalRoot is `~/.punt-labs/ethos` — locks must live in the global
// tree per DES-054 v5 §"Storage Layout" so two checkouts of the same
// repo lock the same inode. Storing the lock under the repo tree
// would mean two checkouts cross-write delegations because each has
// its own .ethos/delegations/ directory. The path matches the
// (*Store).DelegationLockPath method on the same global root.
//
// Used by the PreToolUse-on-Agent dispatch path (DES-054 v5): the
// hook allocates a delegation_id and holds this lock across the
// skeleton write so the prompt.md and record.yaml writes appear
// atomic to any reader.
//
// Acquisition failures surface with both the lock path and the
// underlying syscall so an operator can locate the contended file.
// On flock error the file descriptor is closed before return — no
// leaked fd on the error path.
//
// Concurrency: blocks until the lock is available. Callers that
// must not block should run AcquireDelegationLock in a goroutine
// against their own timeout; the helper does not expose a non-
// blocking mode because every caller in DES-054 v5 wants the
// blocking semantics.
func AcquireDelegationLock(globalRoot, delegationID string) (func(), error) {
	if strings.TrimSpace(globalRoot) == "" {
		return nil, fmt.Errorf("globalRoot is required for delegation lock")
	}
	if strings.TrimSpace(delegationID) == "" {
		return nil, fmt.Errorf("delegationID is required for delegation lock")
	}
	dir := filepath.Join(globalRoot, "delegations")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating delegation lock directory %s: %w", dir, err)
	}
	lockPath := filepath.Join(dir, filepath.Base(delegationID)+".lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening delegation lock %s: %w", lockPath, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("acquiring exclusive delegation lock %s: %w", lockPath, err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
	return release, nil
}

// MissionLockPath returns the per-mission flock path. Same location
// as the internal lockPath but exported so the PreToolUse-on-Agent
// hook can acquire a shared (read) lock without going through any
// other Store method that would take exclusive.
func (s *Store) MissionLockPath(missionID string) string {
	return s.lockPath(missionID)
}

// DelegationSkeleton is the caller-supplied payload for
// WriteDelegationSkeleton. The fields shadow the Delegation type but
// carry only what the PreToolUse-on-Agent dispatch path knows at
// skeleton-write time: parent delegation, agent type, prompt hash,
// session ids, tier, and (optionally) a prompt body to stash next to
// record.yaml. verdict and opened_at are stamped by the writer.
//
// Yaml tags are present so the type can be round-tripped through
// fixtures and golden files; the writer marshals into the on-disk
// Delegation shape, not this struct, so the field names on disk match
// the Delegation type that LoadDelegation expects.
type DelegationSkeleton struct {
	Tier             string `yaml:"tier" json:"tier"`
	ParentDelegation string `yaml:"parent_delegation,omitempty" json:"parent_delegation,omitempty"`
	ParentSession    string `yaml:"parent_session,omitempty" json:"parent_session,omitempty"`
	Session          string `yaml:"session,omitempty" json:"session,omitempty"`
	AgentType        string `yaml:"agent_type" json:"agent_type"`
	SpawnPattern     string `yaml:"spawn_pattern,omitempty" json:"spawn_pattern,omitempty"`
	PromptHash       string `yaml:"prompt_hash,omitempty" json:"prompt_hash,omitempty"`
	Prompt           []byte `yaml:"-" json:"-"`
}

// DelegationDir returns the on-disk per-delegation directory under a
// mission. delegationID is run through filepath.Base for defense in
// depth — a tainted ID with path separators can only ever resolve to
// a leaf under <repo>/.ethos/missions/<mission-id>/delegations/.
//
// The full delegation ID (after filepath.Base) is the directory leaf.
// The DES-054 v5 design diagram uses <NN> for compactness, but using
// the trailing numeric segment alone collides across day boundaries:
// a mission spanning two days would produce d-2026-05-22-001 AND
// d-2026-05-23-001 (the counter resets per date namespace) which
// both share the leaf "001" → directory collision (Bugbot HIGH on
// PR #327). The full ID is unambiguous and still nests cleanly.
func DelegationDir(repoRoot, missionID, delegationID string) string {
	return filepath.Join(
		repoRoot, ".ethos", "missions",
		filepath.Base(missionID), "delegations",
		filepath.Base(delegationID),
	)
}

// WriteDelegationSkeleton writes a freshly-constructed Delegation
// record to <repoRoot>/.ethos/missions/<missionID>/delegations/<NN>/
// record.yaml, plus an optional prompt.md sibling. The caller must
// already hold AcquireDelegationLock for the delegation ID, and
// should hold the mission shared lock from AcquireMissionLock so
// concurrent Tier B spawns do not race on the missions directory
// creation.
//
// Atomic via temp+rename in the same directory — a partial write
// leaves no record.yaml on disk. The parent directory is created
// with 0o700. opened_at is stamped at write time; the caller does
// not need to populate it.
//
// Returns the absolute path of the written record so the caller can
// stash it for the depth/hash-gate refusal cleanup paths.
func WriteDelegationSkeleton(repoRoot, missionID, delegationID string, payload DelegationSkeleton) (string, error) {
	if strings.TrimSpace(repoRoot) == "" {
		return "", fmt.Errorf("repoRoot is required for delegation skeleton")
	}
	if strings.TrimSpace(missionID) == "" {
		return "", fmt.Errorf("missionID is required for delegation skeleton")
	}
	if strings.TrimSpace(delegationID) == "" {
		return "", fmt.Errorf("delegationID is required for delegation skeleton")
	}
	dir := DelegationDir(repoRoot, missionID, delegationID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating delegation directory %s: %w", dir, err)
	}
	// Order: prompt.md FIRST, then record.yaml. A half-built skeleton
	// (writer crash between the two writes) must have no record.yaml on
	// disk — readers branch on record.yaml's presence, so its absence
	// signals "skeleton not yet committed" and avoids a torn read.
	if len(payload.Prompt) > 0 {
		if err := writeAtomicFile(dir, "prompt-*.md.tmp",
			filepath.Join(dir, "prompt.md"), payload.Prompt); err != nil {
			return "", fmt.Errorf("writing delegation prompt: %w", err)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	d := Delegation{
		ID:               filepath.Base(delegationID),
		Tier:             payload.Tier,
		Mission:          filepath.Base(missionID),
		ParentDelegation: payload.ParentDelegation,
		ParentSession:    payload.ParentSession,
		Session:          payload.Session,
		AgentType:        payload.AgentType,
		SpawnPattern:     payload.SpawnPattern,
		PromptHash:       payload.PromptHash,
		CreatedAt:        now,
		Verdict:          DelegationVerdictOpen,
	}
	data, err := yaml.Marshal(&d)
	if err != nil {
		return "", fmt.Errorf("marshaling delegation: %w", err)
	}
	recordPath := filepath.Join(dir, "record.yaml")
	if err := writeAtomicFile(dir, "record-*.yaml.tmp", recordPath, data); err != nil {
		return "", fmt.Errorf("writing delegation record: %w", err)
	}
	return recordPath, nil
}

// writeAtomicFile writes data to destPath atomically via os.CreateTemp
// in dir, Chmod(0o600), Sync, Close, Rename. Sync errors propagate so
// a failed fsync surfaces — djb evaluator gate: a half-written file
// is unacceptable. The temp file is removed on every error path.
//
// pattern is the os.CreateTemp pattern (e.g. "record-*.yaml.tmp"); the
// random component avoids the predictable ".tmp" suffix that a second
// writer could trample.
func writeAtomicFile(dir, pattern, destPath string, data []byte) error {
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	if n, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("writing %s: %w", tmpPath, err)
	} else if n < len(data) {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("short write to %s: %d of %d bytes", tmpPath, n, len(data))
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("syncing %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("closing %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming %s -> %s: %w", tmpPath, destPath, err)
	}
	return nil
}

// AcquireMissionLock opens (and creates if needed) the per-mission
// lock file at <repoRoot>/.ethos/missions/<missionID>/.lock and
// acquires a SHARED flock on it. The returned release closure runs
// LOCK_UN + Close exactly once; subsequent calls are no-ops.
//
// The shared lock complements AcquireDelegationLock (exclusive) so
// two Tier B spawns under one mission can both hold the mission lock
// concurrently while their per-delegation exclusive locks do not
// contend. A separate writer that needs the mission tree quiescent —
// for example a hypothetical mission close that wants no in-flight
// skeletons — can take LOCK_EX on the same file and will wait for
// every shared holder to release.
//
// Acquisition order when nested with other locks:
//
//	global → repo → per-mission(shared) → per-delegation(exclusive)
//
// Release reverse via defer LIFO.
//
// Acquisition failures surface with both the lock path and the
// underlying syscall so an operator can locate the contended file.
// On flock error the file descriptor is closed before return — no
// leaked fd on the error path.
func AcquireMissionLock(repoRoot, missionID string) (func(), error) {
	if strings.TrimSpace(repoRoot) == "" {
		return nil, fmt.Errorf("repoRoot is required for mission lock")
	}
	if strings.TrimSpace(missionID) == "" {
		return nil, fmt.Errorf("missionID is required for mission lock")
	}
	dir := filepath.Join(repoRoot, ".ethos", "missions", filepath.Base(missionID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating mission lock directory %s: %w", dir, err)
	}
	lockPath := filepath.Join(dir, ".lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening mission lock %s: %w", lockPath, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("acquiring shared mission lock %s: %w", lockPath, err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
	return release, nil
}

// LoadDelegation reads a delegation record from disk. Returns
// (nil, fs.ErrNotExist) for a missing file so the caller can branch
// on errors.Is. Symlink rejection guards against the same local-
// attacker vector that rejectSymlink covers for contract files.
func LoadDelegation(recordPath string) (*Delegation, error) {
	if strings.TrimSpace(recordPath) == "" {
		return nil, fmt.Errorf("recordPath is required")
	}
	if err := rejectSymlink(recordPath); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(recordPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fs.ErrNotExist
		}
		return nil, fmt.Errorf("reading delegation record %s: %w", recordPath, err)
	}
	var d Delegation
	if err := yaml.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("decoding delegation record %s: %w", recordPath, err)
	}
	return &d, nil
}

// CloseDelegation rewrites a delegation record with the given
// verdict and closed_at=<now>. Used by:
//
//   - PreToolUse-on-Agent's max_delegation_depth refusal path (verdict=aborted).
//   - SubagentStart's hash-gate refusal cleanup (verdict=aborted).
//   - SubagentStop's normal-close path (verdict=pass for Tier B success).
//
// The caller must hold AcquireDelegationLock for d.ID. Idempotent
// against itself: a second close on an already-closed delegation
// overwrites the verdict and ClosedAt — the caller is responsible
// for not re-closing legitimately-pass'd records (the refusal paths
// only fire on records that were just written and never advanced).
//
// Atomic via os.CreateTemp + Chmod(0o600) + Sync + Close + Rename in
// the record's own directory. A predictable ".tmp" suffix is avoided
// so a second writer cannot trample the first. djb evaluator gate:
// a half-written close is unacceptable — the on-disk record either
// keeps its prior contents or carries the new verdict in full.
func CloseDelegation(recordPath, verdict, reason string) error {
	d, err := LoadDelegation(recordPath)
	if err != nil {
		return fmt.Errorf("close delegation: loading record: %w", err)
	}
	d.Verdict = verdict
	d.ClosedAt = time.Now().UTC().Format(time.RFC3339)
	if reason != "" {
		d.Reason = reason
	}
	data, err := yaml.Marshal(d)
	if err != nil {
		return fmt.Errorf("close delegation: marshaling record: %w", err)
	}
	if err := writeAtomicFile(filepath.Dir(recordPath), "record-*.yaml.tmp", recordPath, data); err != nil {
		return fmt.Errorf("close delegation: %w", err)
	}
	return nil
}

// CloseDelegationSkeleton rewrites a Tier B delegation record's
// verdict and closed_at fields and writes it back atomically via
// temp+rename in the same directory. Used by the refusal paths that
// run AFTER WriteDelegationSkeleton has put a record on disk but
// BEFORE the worker spawn proceeds:
//
//   - PreToolUse-on-Agent: max_delegation_depth exceeded (verdict=aborted).
//   - SubagentStart: hash-gate refusal for a Tier B verifier (verdict=aborted).
//
// verdict must be one of DelegationVerdict{Pass,Fail,Error,Aborted};
// DelegationVerdictOpen is rejected because closing to "open" is not
// a state transition. closedAt is the timestamp to stamp; pass time.
// Now().UTC().Format(time.RFC3339) at the call site so the timestamp
// reflects the refusal moment, not the load moment.
//
// Atomicity: the rewrite goes through os.CreateTemp in the record's
// own directory and os.Rename. djb evaluates this as security — a
// half-written close is unacceptable. The temp file is removed on
// any failure path before return; the on-disk record either keeps
// its prior contents or has the new verdict in full, never partial.
//
// Returns fs.ErrNotExist when the record does not exist on disk so
// the caller can branch on errors.Is for the "skeleton was never
// written" case (the refusal fired before the skeleton write, which
// is an order-of-operations bug in the caller — surface it loudly).
func CloseDelegationSkeleton(repoRoot, missionID, delegationID, verdict, closedAt string) error {
	if strings.TrimSpace(repoRoot) == "" {
		return fmt.Errorf("repoRoot is required for close delegation skeleton")
	}
	if strings.TrimSpace(missionID) == "" {
		return fmt.Errorf("missionID is required for close delegation skeleton")
	}
	if strings.TrimSpace(delegationID) == "" {
		return fmt.Errorf("delegationID is required for close delegation skeleton")
	}
	switch verdict {
	case DelegationVerdictPass,
		DelegationVerdictFail,
		DelegationVerdictError,
		DelegationVerdictAborted:
	default:
		return fmt.Errorf(
			"close delegation skeleton: invalid verdict %q (must be one of pass|fail|error|aborted)",
			verdict,
		)
	}
	if strings.TrimSpace(closedAt) == "" {
		return fmt.Errorf("closedAt is required for close delegation skeleton")
	}
	dir := DelegationDir(repoRoot, missionID, delegationID)
	recordPath := filepath.Join(dir, "record.yaml")
	d, err := LoadDelegation(recordPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("close delegation skeleton: %s: %w", recordPath, fs.ErrNotExist)
		}
		return fmt.Errorf("close delegation skeleton: loading %s: %w", recordPath, err)
	}
	d.Verdict = verdict
	d.ClosedAt = closedAt
	data, err := yaml.Marshal(d)
	if err != nil {
		return fmt.Errorf("close delegation skeleton: marshaling record: %w", err)
	}
	if err := writeAtomicFile(dir, "record-*.yaml.tmp", recordPath, data); err != nil {
		return fmt.Errorf("close delegation skeleton: %w", err)
	}
	return nil
}

// DelegationDepth walks the parent_delegation chain starting from
// d's parent and returns the chain length. A delegation with no
// parent has depth 0 (it is the root of its chain).
//
// The walker reads from disk; loader is the caller-supplied function
// that maps a delegation ID to a *Delegation. Decoupling from the
// store keeps depth computation testable without staging a Store.
//
// max is the configured ceiling from the call site (typically the
// value returned by resolve.ResolveMaxDelegationDepth). The walker
// uses max+1 as a cycle-detection backstop: a chain longer than
// max+1 is treated as a runaway recursive spawn pattern and surfaces
// as an error rather than spinning. Hard-coding the cap to
// MaxDelegationDepthDefault would shadow a configured limit higher
// than the default — a repo with max_delegation_depth: 32 must be
// able to walk 32 ancestors without the walker tripping at 17.
// A non-positive max collapses to MaxDelegationDepthDefault+1 so a
// caller that forgets to thread the config through still gets a
// safe backstop.
//
// Returns an error if any ancestor cannot be loaded — the walker
// fails closed because a missing ancestor could be a deleted record
// that should still count against the depth budget, and silently
// treating it as zero would let runaway spawn patterns pass.
//
// A malformed parent_delegation that points back at itself is a
// corruption, not a normal state, and surfaces as a cycle error
// before the depth backstop fires.
func DelegationDepth(d *Delegation, loader func(id string) (*Delegation, error), max int) (int, error) {
	if d == nil {
		return 0, fmt.Errorf("delegation is nil")
	}
	if loader == nil {
		return 0, fmt.Errorf("loader is required")
	}
	backstop := max + 1
	if max <= 0 {
		backstop = MaxDelegationDepthDefault + 1
	}
	depth := 0
	currentID := d.ParentDelegation
	seen := make(map[string]struct{})
	for currentID != "" {
		if _, dup := seen[currentID]; dup {
			return 0, fmt.Errorf("delegation chain cycle at %q", currentID)
		}
		seen[currentID] = struct{}{}
		if depth >= backstop {
			return 0, fmt.Errorf(
				"delegation chain exceeds %d ancestors (current: %q); "+
					"a runaway recursive spawn pattern is likely",
				backstop, currentID,
			)
		}
		parent, err := loader(currentID)
		if err != nil {
			return 0, fmt.Errorf("walking parent_delegation %q: %w", currentID, err)
		}
		if parent == nil {
			return 0, fmt.Errorf("parent delegation %q not found", currentID)
		}
		depth++
		currentID = parent.ParentDelegation
	}
	return depth, nil
}
