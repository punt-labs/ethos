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
// delegation lock file at `<repoRoot>/.ethos/delegations/<id>.lock`
// and acquires an exclusive flock on it. The returned release closure
// runs LOCK_UN + Close exactly once; subsequent calls are no-ops.
//
// Used by the PreToolUse-on-Agent dispatch path (DES-054 v5): the
// hook allocates a delegation_id and holds this lock across the
// skeleton write so the prompt.md and record.yaml writes appear
// atomic to any reader. The skeleton writer is intentionally not
// invoked here — that lands in a later mission.
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
func AcquireDelegationLock(repoRoot, delegationID string) (func(), error) {
	if strings.TrimSpace(repoRoot) == "" {
		return nil, fmt.Errorf("repoRoot is required for delegation lock")
	}
	if strings.TrimSpace(delegationID) == "" {
		return nil, fmt.Errorf("delegationID is required for delegation lock")
	}
	dir := filepath.Join(repoRoot, ".ethos", "delegations")
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

// WithMissionFlockShared executes fn while holding a SHARED flock on
// the mission's lock file. DES-054 v5: per-mission flock is shared
// during the per-delegation skeleton write so two Tier B spawns under
// the same mission do not serialize against each other; only the
// per-delegation exclusive lock serializes the skeleton write itself.
//
// Acquisition order when nested inside other locks:
//   global → repo → per-mission(shared) → per-delegation(exclusive)
// Release reverse via defer LIFO.
//
// Returns the wrapped fn error verbatim; flock acquisition failures
// are wrapped with the mission ID and the lock path so an operator
// can locate the contended file.
func (s *Store) WithMissionFlockShared(missionID string, fn func() error) error {
	if strings.TrimSpace(missionID) == "" {
		return fmt.Errorf("missionID is required")
	}
	if err := os.MkdirAll(s.missionsDir(), 0o700); err != nil {
		return fmt.Errorf("creating missions directory: %w", err)
	}
	lockFile := s.MissionLockPath(missionID)
	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("opening mission lock %s: %w", lockFile, err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		return fmt.Errorf("acquiring shared mission lock %s: %w", lockFile, err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	return fn()
}

// WithDelegationFlock executes fn while holding an EXCLUSIVE flock on
// the per-delegation lock file. DES-054 v5: every read/write of a
// delegation record skeleton goes through this lock so the prompt.md
// and record.yaml writes appear atomic to any reader.
//
// Lock acquisition is wrapped so a caller forgetting to handle the
// flock error gets a single, named diagnostic rather than a generic
// "permission denied" pointing at an opaque fd.
func (s *Store) WithDelegationFlock(delegationID string, fn func() error) error {
	if strings.TrimSpace(delegationID) == "" {
		return fmt.Errorf("delegationID is required")
	}
	lockPath := s.DelegationLockPath(delegationID)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return fmt.Errorf("creating delegations directory: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("opening delegation lock %s: %w", lockPath, err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquiring exclusive delegation lock %s: %w", lockPath, err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	return fn()
}

// DelegationRecordPath returns the absolute on-disk location of a
// delegation's record.yaml. The caller supplies the parent shape:
//
//   - Tier B: a mission record lives at
//     `<repoRoot>/.ethos/missions/<mission-id>/delegations/<NN>/record.yaml`,
//     where <NN> is the delegation's two-digit sequence under the
//     mission. The caller passes the parent missionID and the spawn
//     sequence (typically the suffix of the delegation ID, e.g. "01").
//   - Tier A: an ad-hoc record lives at
//     `<repoRoot>/.ethos/sessions/<sessionDir>/adhoc/<NNN>/record.yaml`,
//     where <sessionDir> is the date-prefixed session directory and
//     <NNN> is the spawn sequence under the session.
//
// repoRoot empty surfaces an error rather than silently falling back
// to ~/.punt-labs/ethos — phase 2 records always land in-repo. The
// legacy fallback for audit reads stays in audit_paths.go; that path
// is for read-only history, not new writes.
func DelegationRecordPath(repoRoot string, d *Delegation, sequenceDir, parentDir string) (string, error) {
	if d == nil {
		return "", fmt.Errorf("delegation is nil")
	}
	if strings.TrimSpace(repoRoot) == "" {
		return "", fmt.Errorf("repoRoot is required for delegation record path")
	}
	if strings.TrimSpace(sequenceDir) == "" {
		return "", fmt.Errorf("sequence directory is required")
	}
	switch d.Tier {
	case TierB:
		if strings.TrimSpace(parentDir) == "" {
			return "", fmt.Errorf("mission id is required for Tier B delegation path")
		}
		return filepath.Join(
			repoRoot, ".ethos", "missions",
			filepath.Base(parentDir), "delegations",
			filepath.Base(sequenceDir), "record.yaml",
		), nil
	case TierA:
		if strings.TrimSpace(parentDir) == "" {
			return "", fmt.Errorf("session directory is required for Tier A delegation path")
		}
		return filepath.Join(
			repoRoot, ".ethos", "sessions",
			filepath.Base(parentDir), "adhoc",
			filepath.Base(sequenceDir), "record.yaml",
		), nil
	default:
		return "", fmt.Errorf("unknown delegation tier %q", d.Tier)
	}
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
// The two-digit sequence is the trailing numeric segment of the
// d-YYYY-MM-DD-NNN shape; that segment is what nests under the mission
// per DES-054 v5 §"Per-mission delegation tree".
func DelegationDir(repoRoot, missionID, delegationID string) string {
	return filepath.Join(
		repoRoot, ".ethos", "missions",
		filepath.Base(missionID), "delegations",
		delegationSequence(delegationID),
	)
}

// delegationSequence pulls the trailing numeric segment from a
// d-YYYY-MM-DD-NNN delegation ID. When the shape does not match (a
// bare ID, an over-trimmed handle, or anything filepath.Base would
// keep as a single segment), the full base is returned so the caller
// still gets a stable, sanitized leaf.
func delegationSequence(delegationID string) string {
	base := filepath.Base(delegationID)
	idx := strings.LastIndex(base, "-")
	if idx < 0 || idx == len(base)-1 {
		return base
	}
	return base[idx+1:]
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
	tmp, err := os.CreateTemp(dir, "record-*.yaml.tmp")
	if err != nil {
		return "", fmt.Errorf("creating temp delegation record in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("writing temp delegation record %s: %w", tmpPath, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("chmod temp delegation record %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("closing temp delegation record %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, recordPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("renaming delegation record %s -> %s: %w", tmpPath, recordPath, err)
	}
	if len(payload.Prompt) > 0 {
		promptPath := filepath.Join(dir, "prompt.md")
		if err := os.WriteFile(promptPath, payload.Prompt, 0o600); err != nil {
			return "", fmt.Errorf("writing delegation prompt %s: %w", promptPath, err)
		}
	}
	return recordPath, nil
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
// The caller must hold WithDelegationFlock for d.ID. Idempotent
// against itself: a second close on an already-closed delegation
// overwrites the verdict and ClosedAt — the caller is responsible
// for not re-closing legitimately-pass'd records (the refusal paths
// only fire on records that were just written and never advanced).
//
// Atomic via temp+rename.
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
	tmp := recordPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("close delegation: writing temp record: %w", err)
	}
	if err := os.Rename(tmp, recordPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close delegation: renaming record: %w", err)
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
// Returns an error if any ancestor cannot be loaded — the walker
// fails closed because a missing ancestor could be a deleted record
// that should still count against the depth budget, and silently
// treating it as zero would let runaway spawn patterns pass.
//
// Cycle detection: the walker bounds itself by MaxDelegationDepthDefault
// + 1; if the chain exceeds that, the function returns an error rather
// than spin. A malformed parent_delegation that points back at itself
// is a corruption, not a normal state, and surfaces as an error.
func DelegationDepth(d *Delegation, loader func(id string) (*Delegation, error)) (int, error) {
	if d == nil {
		return 0, fmt.Errorf("delegation is nil")
	}
	if loader == nil {
		return 0, fmt.Errorf("loader is required")
	}
	depth := 0
	currentID := d.ParentDelegation
	seen := make(map[string]struct{})
	for currentID != "" {
		if _, dup := seen[currentID]; dup {
			return 0, fmt.Errorf("delegation chain cycle at %q", currentID)
		}
		seen[currentID] = struct{}{}
		if depth > MaxDelegationDepthDefault+1 {
			return 0, fmt.Errorf(
				"delegation chain exceeds %d ancestors (current: %q); "+
					"a runaway recursive spawn pattern is likely",
				MaxDelegationDepthDefault+1, currentID,
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
