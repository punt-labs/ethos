//go:build !windows

package mission

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// ErrEvaluatorNotFound wraps the underlying identity-store "not found"
// error so ApplyServerFields can return a single-line operator-facing
// message instead of the deeply-wrapped error chain the identity,
// mission, and hash-sources layers would otherwise produce.
//
// Sentinel error so callers can check via errors.Is; the concrete
// error carries the handle in its message for diagnostics.
var ErrEvaluatorNotFound = errors.New("evaluator not found")

// Store provides CRUD operations for mission contracts on the filesystem.
//
// Mirrors internal/session/store.go: contracts are stored as YAML files in
// the missions subdirectory; write operations use flock for concurrency
// safety; writes are atomic via temp file + rename.
//
// Unlike the session store, mission Store is intentionally global-only —
// contracts are not git-tracked and not layered. Phase 3.2+ may revisit if
// repo-scoped missions become necessary.
type Store struct {
	root       string          // e.g. ~/.punt-labs/ethos
	roles      RoleLister      // optional; wires the Phase 3.5 role-overlap check
	archetypes *ArchetypeStore // optional; validates Type on Create

	// repoRoot, when set, names the repository directory under which the
	// SHARED mission RECORD lives — contracts, results, reflections,
	// delegations, and the missions.jsonl trace, all under
	// <repoRoot>/.punt-labs/ethos/missions/. This is the store root
	// (StoreRepoRoot): in a linked worktree it resolves to the main work
	// tree so every checkout sees the same record. Set via WithRepoRoot
	// (legacy single-tree mode) or NewStoreWithRoots (two-tree mode).
	repoRoot string

	// checkoutRoot, when set, names the PER-CHECKOUT directory the DES-058
	// audit concern lives under — the machine-local live zone
	// (<checkoutRoot>/.punt-labs/local/ethos/) and the sealed mission chunks
	// the pre-commit seal writes and stages. It is the current work tree
	// (FindRepoRoot/EnvRepoRoot), NOT the store root: audit chunks travel in
	// the committing checkout's commit and the pre-commit seal runs there, so
	// the live append, the seal, and the reader must all agree on the
	// worktree, while the record resolves to the main store (Bugbot HIGH on
	// PR #370: a single root routed live events into the main tree that the
	// worktree's pre-commit seal never sealed). Empty falls back to repoRoot
	// via auditRoot() so single-tree callers and tests are unchanged.
	checkoutRoot string

	// twoTreeStorage, when true, activates the DES-054 phase 1
	// per-mission directory layout under <repoRoot>/.punt-labs/ethos/missions/.
	// Set only by NewStoreWithRoots; WithRepoRoot leaves it false so
	// existing callers that wired a trace destination keep the legacy
	// flat <root>/missions/<id>.yaml shape.
	twoTreeStorage bool

	// sessionID, when set alongside repoRoot, routes mission event
	// appends to the DES-058 per-(mission, session) live log under
	// <repoRoot>/.punt-labs/local/ethos/missions/<id>/<session-id>.log.jsonl
	// with a strictly-monotonic timestamp, instead of the tracked
	// log.jsonl. Empty (tests, non-session contexts) keeps the legacy
	// tracked-log append so the tree behavior is unchanged there.
	//
	// A fixed value from WithSessionID wins. Otherwise sessionResolver
	// is consulted lazily at each append and its first non-empty result
	// is cached here — so a long-lived process (the MCP server) that
	// starts before the session mapping exists still attributes later
	// events to the real session instead of the reserved no-session log.
	// sessionMu guards the lazy cache-fill against concurrent appends.
	sessionID       string
	sessionResolver func() string
	sessionMu       sync.Mutex
}

// NewStore creates a Store rooted at the given directory.
//
// Preserved as a thin wrapper over NewStoreWithRoots for backward
// compatibility — existing callers that only know about the legacy
// global tree compile unchanged. New callers that want the DES-054
// two-tree storage layout (per-repo missions under .punt-labs/ethos/, fallback
// reads from global) should use NewStoreWithRoots.
func NewStore(root string) *Store {
	return NewStoreWithRoots("", root)
}

// NewStoreWithRoots creates a Store that dispatches mission artifacts
// across two roots — per-repo and global — per DES-054 phase 1.
//
// When repoRoot is non-empty, Create writes new missions under
// <repoRoot>/.punt-labs/ethos/missions/<mission-id>/; Load, Update, Close, and
// the sibling artifact paths (results, reflections, log) read the
// repo tree first and fall back to the legacy <globalRoot>/missions/
// shape for backward compatibility. List unions both trees with
// repo-wins dedup.
//
// When repoRoot is empty, the legacy single-tree layout is the only
// path — Create, Load, Update, Close all operate against
// <globalRoot>/missions/<mission-id>.{yaml,jsonl,...}. This is the
// shape NewStore(root) preserves.
//
// Per-mission and per-create flocks always live under globalRoot —
// the DES-054 concurrency model pins them to a stable per-machine
// location so two checkouts of the same repo never lock different
// inodes for the same mission. Counter files (DES-054 sibling
// per-namespace per-date) also live under globalRoot.
func NewStoreWithRoots(repoRoot, globalRoot string) *Store {
	return &Store{
		root:           globalRoot,
		repoRoot:       repoRoot,
		twoTreeStorage: repoRoot != "",
	}
}

// WithRoleLister wires a RoleLister for the Phase 3.5 role-overlap
// check. When set, Store.Create refuses a contract whose worker and
// evaluator share a team-scoped role binding or a role slug after
// canonicalization — see checkRoleOverlap.
//
// The method is opt-in so existing unit tests that build a bare Store
// keep working; CLI and MCP construction wires the lister via the
// live identity, role, and team stores. A nil lister disables the
// check entirely (the worker != evaluator handle guard in
// checkRoleConflicts still runs).
//
// Returns the receiver so construction stays compact:
//
//	ms := mission.NewStore(root).WithRoleLister(rl)
func (s *Store) WithRoleLister(r RoleLister) *Store {
	s.roles = r
	return s
}

// WithArchetypeStore wires an ArchetypeStore for type validation on
// Create. When set, Store.Create refuses a contract whose Type does not
// match a discovered archetype. A nil store disables the check entirely
// (backward compatible).
//
// Returns the receiver so construction stays compact:
//
//	ms := mission.NewStore(root).WithArchetypeStore(as)
func (s *Store) WithArchetypeStore(as *ArchetypeStore) *Store {
	s.archetypes = as
	return s
}

// WithSessionID wires the current session id so mission event appends route
// to the DES-058 per-(mission, session) live log under
// <repoRoot>/.punt-labs/local/ethos/missions/<id>/. When unset (the default),
// appends stay on the legacy tracked log.jsonl path. Only meaningful together
// with a repoRoot (NewStoreWithRoots). Returns the receiver so construction
// stays compact.
func (s *Store) WithSessionID(sessionID string) *Store {
	s.sessionID = sessionID
	return s
}

// WithSessionResolver wires a lazy session-id resolver, consulted at each
// append rather than frozen at construction. A long-lived process (the MCP
// server) can outlive the moment the session mapping first appears: resolving
// once at startup would freeze an empty id and misroute every later event to
// the reserved no-session log. The resolver is called until it returns a
// non-empty id, which is then cached — a session does not change mid-process.
//
// A fixed WithSessionID value takes precedence; the resolver only fills an
// empty cache. The CLI single-shot path pays one resolution regardless, so it
// stays zero-cost. Returns the receiver so construction stays compact.
func (s *Store) WithSessionResolver(resolve func() string) *Store {
	s.sessionResolver = resolve
	return s
}

// resolveSessionID returns the session id for an append: a fixed WithSessionID
// value, else the first non-empty resolver result (cached), else empty. The
// caller maps empty to the reserved sessionlessID.
func (s *Store) resolveSessionID() string {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if s.sessionID != "" {
		return s.sessionID
	}
	if s.sessionResolver != nil {
		if id := s.sessionResolver(); id != "" {
			s.sessionID = id
		}
	}
	return s.sessionID
}

// WithRepoRoot sets the repository root for the post-close trace
// summary. When set, Store.Close appends a JSONL summary line to
// <repoRoot>/.punt-labs/ethos/missions.jsonl so every closed mission is
// visible in the repo's git history. An empty root disables the
// trace.
//
// Does NOT activate the DES-054 two-tree storage layout. Callers
// that want both trace and the per-repo storage tree should use
// NewStoreWithRoots, which sets both fields. WithRepoRoot stays
// trace-only so existing tests and production callers that wired a
// trace destination keep the legacy storage shape.
//
// Returns the receiver so construction stays compact:
//
//	ms := mission.NewStore(root).WithRepoRoot(repoRoot)
func (s *Store) WithRepoRoot(root string) *Store {
	s.repoRoot = root
	return s
}

// WithCheckoutRoot sets the per-checkout root for the DES-058 audit
// concern — the live zone and sealed mission chunks. When unset the audit
// path falls back to repoRoot (auditRoot), preserving single-tree and test
// behavior. Callers in a worktree pass the work-tree root (FindRepoRoot/
// EnvRepoRoot) so live appends and the pre-commit seal agree on the
// committing checkout while the record resolves to the main store (Bugbot
// HIGH on PR #370). Returns the receiver so construction stays compact:
//
//	ms := mission.NewStoreWithRoots(storeRoot, global).WithCheckoutRoot(wt)
func (s *Store) WithCheckoutRoot(root string) *Store {
	s.checkoutRoot = root
	return s
}

// auditRoot returns the root the DES-058 live zone and sealed mission
// chunks resolve under: the per-checkout root when set, else repoRoot. The
// audit concern is per-checkout (chunks commit with the worktree, the
// pre-commit seal runs there); the mission record is shared-store. Keeping
// the fallback means a Store constructed without a checkout root behaves
// exactly as before (single-tree callers, tests).
func (s *Store) auditRoot() string {
	if s.checkoutRoot != "" {
		return s.checkoutRoot
	}
	return s.repoRoot
}

// Root returns the store's root directory.
func (s *Store) Root() string { return s.root }

// validateContract resolves the archetype from the contract's Type
// (if an archetype store is wired) and runs ValidateWithArchetype.
// When no archetype store is set, falls back to Validate() (all rules).
func (s *Store) validateContract(c *Contract) error {
	if s.archetypes != nil && c.Type != "" {
		arch, err := s.archetypes.Load(c.Type)
		if err != nil {
			// Unknown type — fall back to base validation.
			return c.Validate()
		}
		return c.ValidateWithArchetype(arch)
	}
	return c.Validate()
}

// ContractPath returns the absolute path to a mission contract file
// on disk. Exposed so the Phase 3.5 verifier context-isolation path
// can read the contract byte-for-byte before injecting it into the
// verifier subagent — the invariant is "the contract the verifier
// sees is the contract pinned on disk, no re-serialization allowed".
//
// Mission ID is run through filepath.Base as defense in depth, the
// same way contractPath does internally. A caller passing a relative
// or traversal-laced ID will only ever get a path under missionsDir.
//
// Returns (string, error) so a stat error on the repo-tree layer
// (EACCES on a chmod-locked parent, for instance) surfaces as a
// wrapped error rather than collapsing to the writeLayer fallback —
// the silent-failure mode local review flagged on mission
// m-2026-05-22-027 fix 3. Callers that already accept "best-effort
// path" can wrap the error or call ContractPath under a recover; new
// callers should propagate.
func (s *Store) ContractPath(missionID string) (string, error) {
	return s.contractPath(missionID)
}

// ApplyServerFields sets all server-controlled fields on a contract at
// create time. Both the CLI (`ethos mission create --file`) and the
// MCP `mission create` handler call this before Store.Create so the
// two paths stay in lockstep — server-controlled fields are the
// server's responsibility, and any caller-supplied value is overwritten
// without warning.
//
// Fields set (every one is unconditionally overwritten):
//   - MissionID: always generated via NewID. A caller-supplied
//     mission_id would bypass the daily counter, leaving the counter
//     file stale and risking a later collision when NewID catches up.
//     The server owns this field full stop.
//   - Status: forced to StatusOpen — a newly created mission is always open
//   - CreatedAt: set to now (RFC3339, UTC)
//   - UpdatedAt: set equal to CreatedAt
//   - ClosedAt: cleared (terminal-only field; Validate's status↔closed_at
//     invariant would reject a non-empty value on an open contract anyway)
//   - WriteSetReleasedAt: cleared. A caller-supplied value here would
//     create a mission born already exempt from checkWriteSetConflicts
//     (ForceReleaseWriteSet's admission-control skip) with no
//     write_set_released event and no genuine release ever having
//     happened -- silently defeating the write-set claim it declares.
//   - Evaluator.PinnedAt: set equal to CreatedAt — the evaluator is
//     pinned AT mission launch by definition; any caller-supplied
//     timestamp would be incoherent
//   - Evaluator.Hash: computed via ComputeEvaluatorHash from the live
//     identity, attribute, and role stores (DES-033). An unresolvable
//     evaluator handle is fatal — Phase 3.1 left this field empty as
//     a placeholder; Phase 3.3 fills it. The hash is the trust anchor
//     the verifier subagent (3.5) compares against on every spawn.
//
// Returns an error if NewID fails to allocate a mission ID (daily
// counter exhausted or poisoned counter file) or if the evaluator
// handle cannot be resolved to identity content.
func (s *Store) ApplyServerFields(c *Contract, now time.Time, sources HashSources) error {
	if c == nil {
		return fmt.Errorf("contract is nil")
	}
	if err := sources.Validate(); err != nil {
		return fmt.Errorf("apply server fields: %w", err)
	}
	if strings.TrimSpace(c.Evaluator.Handle) == "" {
		return fmt.Errorf("apply server fields: evaluator.handle is required before hashing")
	}
	hash, err := ComputeEvaluatorHash(c.Evaluator.Handle, sources)
	if err != nil {
		// Detect the specific "identity YAML file does not exist" case
		// and collapse the 6-level wrapped chain into a single clean
		// operator message. All other hash errors (permission denied,
		// partial talent content, role store corruption) keep their
		// wrapped chain because the chain carries diagnostic value.
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf(
				"apply server fields: %w %q (use `ethos identity list` to see available handles)",
				ErrEvaluatorNotFound, c.Evaluator.Handle,
			)
		}
		return fmt.Errorf("apply server fields: %w", err)
	}
	// Allocate a fresh mission ID under the counters/ tree. The
	// counter is committed unconditionally on the success path
	// because ApplyServerFields itself never writes any contract — a
	// later Create failure is the caller's concern, not this method's,
	// and burning one ID per failed Create is acceptable. The Store
	// path here is the legacy single-root case; the two-root paths in
	// NewStoreWithRoots use NewIDAt with their own counter root so
	// tests stay isolated.
	id, release, err := NewIDAt(s.root, NamespaceMissions, now)
	if err != nil {
		return fmt.Errorf("generating mission ID: %w", err)
	}
	release(true)
	c.MissionID = id
	created := now.UTC().Format(time.RFC3339)
	c.Status = StatusOpen
	c.CreatedAt = created
	c.UpdatedAt = created
	c.ClosedAt = ""
	c.WriteSetReleasedAt = ""
	c.Evaluator.PinnedAt = created
	c.Evaluator.Hash = hash
	return nil
}

// missionsDir returns the global tree's missions directory —
// <root>/missions. The repo tree is reached via repoMissionsDir; the
// callers below (List, withLock, withCreateLock) explicitly know
// which tree they want, so the helper stays narrow.
func (s *Store) missionsDir() string {
	return s.globalMissionsDir()
}

// contractPath builds the YAML path for a mission. When the mission
// already exists on disk, the layer it lives in determines the path
// (DES-054 phase 1: repo-first read with global fallback). When the
// mission does not yet exist, the writeLayer wins — repo if
// repoRoot is set, global otherwise.
//
// The mission ID is run through filepath.Base as a defense-in-depth
// measure: even if a caller somehow passed an absolute or
// traversal-laced ID, only the final element survives.
//
// Distinguishes "not found" (fall back to writeLayer, nil error) from
// real stat errors (EACCES, EIO) which are wrapped and returned. The
// silent-failure mode m-2026-05-22-027 fix 3 closed: under a
// chmod-locked parent, pathSetForExisting returns the EACCES error
// and contractPath used to swallow it and fall back to writeLayer —
// hiding the permission failure behind an os.ErrNotExist surfaced
// from a stale writeLayer path.
func (s *Store) contractPath(missionID string) (string, error) {
	ps, err := s.pathSetForExisting(missionID)
	var path string
	switch {
	case err == nil:
		path = ps.contract
	case errors.Is(err, fs.ErrNotExist):
		path = s.pathSetFor(missionID, s.writeLayer()).contract
	default:
		return "", fmt.Errorf("resolving contract path for %q: %w", missionID, err)
	}
	// Uniform symlink policy at the resolver (paths.go): every
	// downstream open — read, write, or stat — sees a refusal here
	// before the syscall would follow the link. The check is cheap
	// (one Lstat) and uniform across every consumer.
	if err := rejectSymlink(path); err != nil {
		return "", err
	}
	return path, nil
}

// lockPath returns the per-mission flock path. Always under the
// global tree per DES-054 concurrency model: locks reference live
// inodes that must not move when a mission migrates between layers
// or when two checkouts of the same repo coexist.
func (s *Store) lockPath(missionID string) string {
	return filepath.Join(s.globalMissionsDir(), filepath.Base(missionID)+".lock")
}

// createLockPath returns the directory-level lock file used by
// Store.Create to serialize cross-mission write_set conflict scans.
// Stable filename, never renamed or unlinked, so the flock inode does
// not race with concurrent acquirers.
//
// Lives under the global tree alongside per-mission locks. v3.12.0
// also acquires a repo-tree create lock during the transition window
// — see Store.Create.
func (s *Store) createLockPath() string {
	return filepath.Join(s.globalMissionsDir(), ".create.lock")
}

// repoCreateLockPath is the per-repo create lock acquired alongside
// the global one during the DES-054 transition window. v3.13.0 drops
// the global one and keeps only the repo lock. Returns empty when
// repoRoot is unset (legacy single-tree mode) OR when repoMissionsDir
// itself is empty (a "trace-only" WithRepoRoot setup that never
// activated two-tree storage — see repoMissionsDir's doc comment).
// The prior check only covered the first case: a Store built via
// NewStore(x).WithRepoRoot(y) has repoRoot set but twoTreeStorage
// false, so repoMissionsDir() correctly returns "" but this method
// used to join it anyway, collapsing to a bare relative ".create.lock"
// resolved against the caller's CWD instead of a real path (ethos-qs0v).
func (s *Store) repoCreateLockPath() string {
	dir := s.repoMissionsDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, ".create.lock")
}

// logPath returns the JSONL event log path for a mission. Dispatches
// through pathSetForExisting / writeLayer the same way contractPath
// does — read paths see the existing layer, write paths see the
// writeLayer. Wraps stat errors instead of collapsing to writeLayer
// (mission m-2026-05-22-027 fix 3).
func (s *Store) logPath(missionID string) (string, error) {
	ps, err := s.pathSetForExisting(missionID)
	var path string
	switch {
	case err == nil:
		path = ps.log
	case errors.Is(err, fs.ErrNotExist):
		path = s.pathSetFor(missionID, s.writeLayer()).log
	default:
		return "", fmt.Errorf("resolving log path for %q: %w", missionID, err)
	}
	// Uniform symlink policy at the resolver (paths.go) — see
	// contractPath. Covers the writer (appendEventLocked) in log.go
	// without making it call rejectSymlink explicitly.
	if err := rejectSymlink(path); err != nil {
		return "", err
	}
	return path, nil
}

// reflectionsPath returns the sibling YAML file that holds the
// round-by-round reflections for a mission. Reflections live next to
// the contract, not inside it: the contract is the trust boundary
// pinned at launch, and a growing array of reflections would force
// every Update to rewrite an unbounded slice. The sibling file grows
// as rounds happen and is the single source of truth for the
// round-advance gate.
//
// Layer dispatch mirrors contractPath — when the mission already
// exists, the layer it lives in determines the path; otherwise the
// writeLayer wins. Wraps stat errors instead of collapsing to
// writeLayer (mission m-2026-05-22-027 fix 3).
func (s *Store) reflectionsPath(missionID string) (string, error) {
	ps, err := s.pathSetForExisting(missionID)
	var path string
	switch {
	case err == nil:
		path = ps.reflections
	case errors.Is(err, fs.ErrNotExist):
		path = s.pathSetFor(missionID, s.writeLayer()).reflections
	default:
		return "", fmt.Errorf("resolving reflections path for %q: %w", missionID, err)
	}
	// Uniform symlink policy at the resolver (paths.go) — see
	// contractPath.
	if err := rejectSymlink(path); err != nil {
		return "", err
	}
	return path, nil
}

// resultsPath returns the sibling YAML file that holds the
// round-by-round worker results for a mission. Results live next to
// the contract for the same reasons reflections do: the contract is
// the trust boundary pinned at launch, and the growing list of
// results would force every Update to rewrite an unbounded slice.
// Phase 3.6's Close gate reads this file to decide whether a
// terminal transition is allowed.
//
// The filename MUST be filtered out by isContractFile. Phase 3.4's
// round-2 BLOCKER was caused by Store.List treating a sibling file
// as a contract; adding a second sibling without teaching List about
// it would reproduce the same regression for anyone with a result
// file on disk.
//
// Wraps stat errors instead of collapsing to writeLayer (mission
// m-2026-05-22-027 fix 3).
func (s *Store) resultsPath(missionID string) (string, error) {
	ps, err := s.pathSetForExisting(missionID)
	var path string
	switch {
	case err == nil:
		path = ps.results
	case errors.Is(err, fs.ErrNotExist):
		path = s.pathSetFor(missionID, s.writeLayer()).results
	default:
		return "", fmt.Errorf("resolving results path for %q: %w", missionID, err)
	}
	// Uniform symlink policy at the resolver (paths.go) — see
	// contractPath.
	if err := rejectSymlink(path); err != nil {
		return "", err
	}
	return path, nil
}

// ensureMissionDir creates the per-mission directory in the repo
// layer when needed. Returns nil immediately for paths in the global
// (flat) layer — those land directly in <root>/missions/ and require
// no per-mission directory. Called by every writer before it opens a
// file in the per-mission directory.
func (s *Store) ensureMissionDir(missionID string) error {
	if s.repoRoot == "" {
		// Legacy layout: no per-mission dir.
		return nil
	}
	// In the two-root model, the destination depends on where the
	// mission already lives. A loaded mission in the global tree
	// stays there; a new mission lands in the repo tree.
	layer, err := s.resolveLayer(missionID)
	if err != nil {
		return fmt.Errorf("resolving mission layer: %w", err)
	}
	if layer == layerUnset {
		layer = s.writeLayer()
	}
	if layer != layerRepo {
		return nil
	}
	ps := s.pathSetFor(missionID, layerRepo)
	if err := os.MkdirAll(ps.dir, 0o700); err != nil {
		return fmt.Errorf("creating mission dir %s: %w", ps.dir, err)
	}
	return nil
}

// Create persists a new mission contract. The caller must supply a
// fully-populated Contract (the server-controlled fields — MissionID,
// Status, CreatedAt, UpdatedAt, ClosedAt, Evaluator.PinnedAt — can be
// left empty and set via ApplyServerFields before Create, which is
// what the CLI and MCP entry points do). Validate() runs before any
// disk write, so missing required fields (leader, worker, evaluator,
// write_set, success_criteria, budget) produce an error and touch no
// files. UpdatedAt defaults to CreatedAt on first write if empty —
// the one field Create may fill in.
//
// A "create" event is appended to the JSONL log. On event append
// failure the contract file is rolled back so the operation is
// atomic from the caller's point of view.
//
// Works on a shallow copy of c so a validation failure never mutates
// the caller's struct. On success, UpdatedAt is reflected back to
// the caller.
func (s *Store) Create(c *Contract) error {
	if c == nil {
		return fmt.Errorf("contract is nil")
	}
	// Work on a shallow copy so a validation failure never mutates
	// the caller's struct. The UpdatedAt default-fill and Validate
	// both touch only the copy. On success we reflect the new
	// UpdatedAt back to the caller — the one field Create is
	// contracted to set.
	staged := *c
	if staged.UpdatedAt == "" {
		staged.UpdatedAt = staged.CreatedAt
	}
	// Default Type to "implement" when the caller omits it. The field
	// is optional on input for backward compatibility; the store fills
	// the default so every persisted contract has a type.
	if staged.Type == "" {
		staged.Type = "implement"
	}
	// Validate Type against discovered archetypes and enforce constraints.
	var arch *Archetype
	if s.archetypes != nil {
		if !s.archetypes.Exists(staged.Type) {
			available, _ := s.archetypes.List()
			return fmt.Errorf(
				"unknown mission type %q; available archetypes: %s",
				staged.Type, strings.Join(available, ", "),
			)
		}
		a, err := s.archetypes.Load(staged.Type)
		if err != nil {
			return fmt.Errorf("loading archetype %q: %w", staged.Type, err)
		}
		arch = a
	}
	// 3.4: a freshly created mission begins at round 1. The caller
	// may leave CurrentRound at its zero value; Validate would
	// otherwise reject the staged contract for being out of [1, N].
	// Default-filling here keeps the caller's struct unchanged on
	// failure (the shallow copy is what Validate sees) and lets a
	// pre-3.4 client that doesn't know about the field still produce
	// a well-formed contract.
	if staged.CurrentRound == 0 {
		staged.CurrentRound = 1
	}
	if err := staged.ValidateWithArchetype(arch); err != nil {
		return fmt.Errorf("invalid contract: %w", err)
	}
	// Enforce archetype constraints beyond base validation: write_set
	// glob patterns and required fields.
	if arch != nil {
		if err := enforceArchetypeConstraints(&staged, arch); err != nil {
			return fmt.Errorf("archetype %q constraint: %w", staged.Type, err)
		}
	}

	// Phase 3.5: worker-verifier role distinction.
	//
	// The worker != evaluator handle guard is a cheap structural
	// check that runs before any lock is taken — a contract that
	// names the same handle for both slots can never be repaired,
	// and the caller deserves a fast error. The role-overlap check
	// runs inside the create lock so it sees the same store state
	// as checkWriteSetConflicts.
	if err := checkSelfVerification(&staged); err != nil {
		return err
	}

	err := s.withCreateLock(func() error {
		return s.withLock(staged.MissionID, func() error {
			dest, err := s.contractPath(staged.MissionID)
			if err != nil {
				return err
			}
			// Refuse to follow a symlink planted at the destination —
			// uniform symlink policy (see paths.go). The follow-on
			// os.Stat below would otherwise dereference the link and
			// either report "exists" (refusing the create against an
			// attacker-controlled inode) or "not exists" (and a later
			// writeContract would write through the link).
			if err := rejectSymlink(dest); err != nil {
				return err
			}
			// Refuse to overwrite an existing contract via Create — Update
			// is the explicit mutation path.
			if _, statErr := os.Stat(dest); statErr == nil {
				return fmt.Errorf("mission %q already exists", staged.MissionID)
			} else if !os.IsNotExist(statErr) {
				return fmt.Errorf("checking mission existence: %w", statErr)
			}
			// Cross-mission write_set conflict check (Phase 3.2). The
			// directory-level create lock above ensures the scan and
			// the subsequent writeContract are atomic with respect to
			// other concurrent Creates: no other Create can pass its
			// own scan after this Create writes its file but before
			// the create lock is released.
			if err := s.checkWriteSetConflicts(&staged); err != nil {
				return err
			}
			// Phase 3.5: role-overlap check. Runs only when a RoleLister
			// is wired via WithRoleLister — tests that build a bare
			// Store skip the check. CLI and MCP construction always
			// wires the lister so production callers cannot opt out.
			if s.roles != nil {
				if err := checkRoleOverlap(s.roles, &staged); err != nil {
					return err
				}
			}
			if err := s.writeContract(&staged); err != nil {
				return err
			}
			if err := s.appendEventLocked(staged.MissionID, Event{
				TS:    time.Now().UTC().Format(time.RFC3339),
				Event: "create",
				Actor: staged.Leader,
				Details: map[string]any{
					"worker":    staged.Worker,
					"evaluator": staged.Evaluator.Handle,
					"ticket":    staged.Inputs.Ticket,
				},
			}); err != nil {
				// Rollback: remove the just-written contract so the
				// operation is atomic from the caller's point of view.
				// Without rollback, a retry after a log-append failure
				// would hit "already exists" and the caller would have
				// no clean recovery path.
				if rbErr := os.Remove(dest); rbErr != nil && !os.IsNotExist(rbErr) {
					return fmt.Errorf("create: event append failed: %w; rollback failed: %v", err, rbErr)
				}
				return fmt.Errorf("create: event append failed, contract rolled back: %w", err)
			}
			return nil
		})
	})
	if err != nil {
		return err
	}
	// Success: reflect server-defaulted fields back to the caller.
	// A failed Create leaves the caller's struct unchanged.
	c.UpdatedAt = staged.UpdatedAt
	c.CurrentRound = staged.CurrentRound
	c.Type = staged.Type
	return nil
}

// Load reads a mission contract by ID.
//
// Decodes with KnownFields(true) so an attacker who has local write
// access cannot drop extra fields into the on-disk YAML and have them
// silently ignored. Symmetric with the strict create paths.
//
// Distinguishes "not found" from other read errors (permission denied,
// I/O failure) so operators get an accurate diagnostic instead of a
// misleading "not found" for a file that exists but can't be read.
func (s *Store) Load(missionID string) (*Contract, error) {
	if strings.TrimSpace(missionID) == "" {
		return nil, fmt.Errorf("missionID is required")
	}
	path, err := s.contractPath(missionID)
	if err != nil {
		return nil, err
	}
	if err := rejectSymlink(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("mission %q not found", missionID)
		}
		return nil, fmt.Errorf("reading mission %q: %w", missionID, err)
	}
	c, err := s.decodeAndValidate(data, missionID)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// decodeAndValidate is the shared strict-decode + validate + filename-
// match check used by Load and loadLocked. Factored out so the two
// read paths stay in lockstep. The filename check prevents a
// hand-edited foo.yaml file containing mission_id: m-... from
// being silently accepted — a later Close would write the mutated
// contract to m-....yaml (because writeContract uses c.MissionID),
// producing a second file and leaving foo.yaml stale.
//
// Validation is archetype-aware: the contract's Type field is resolved
// against the Store's ArchetypeStore (when wired) so that archetypes
// permitting empty write_set are honored on the load path, not only
// on the create path.
func (s *Store) decodeAndValidate(data []byte, missionID string) (*Contract, error) {
	c, err := DecodeContractStrict(data, missionID)
	if err != nil {
		return nil, err
	}
	// Pre-type contracts on disk have no type line and decode to "".
	// Default-fill on read keeps the upgrade path clean.
	if c.Type == "" {
		c.Type = "implement"
	}
	// 3.4: pre-3.4 contracts on disk have no current_round line and
	// decode to CurrentRound == 0. Default-fill on read keeps the
	// upgrade path clean — a mission created by 3.3 still loads in
	// 3.4 — and the in-memory invariant (1 ≤ CurrentRound ≤
	// Budget.Rounds) is enforced by the Validate call below for
	// every other failure mode.
	if c.CurrentRound == 0 {
		c.CurrentRound = 1
	}
	// Defense in depth: even on read, run Validate. A corrupt or
	// hand-edited contract should be flagged before callers act on it.
	// Uses archetype-aware validation so archetypes permitting empty
	// write_set (report, inbox) are honored on load, not only on create.
	if err := s.validateContract(c); err != nil {
		return nil, fmt.Errorf("contract %q failed validation on load: %w", missionID, err)
	}
	// The on-disk filename must match the contract's own mission_id.
	// Rejects the scenario where a caller passes a filename that
	// doesn't match the contract content (typically a hand-edited
	// file, or a rename that forgot to update the payload).
	if c.MissionID != missionID {
		return nil, fmt.Errorf(
			"contract filename %q does not match mission_id %q in the file",
			missionID, c.MissionID,
		)
	}
	return c, nil
}

// Update writes a mutated contract back to disk under flock. The caller
// is responsible for any field mutation; Update bumps UpdatedAt and
// validates before writing.
//
// Update works on a shallow copy of the caller's contract inside the
// lock so that a mid-method failure (stat, validate, write) leaves the
// caller's struct unchanged. On success, UpdatedAt is reflected back
// to the caller — that is the one field Update is contracted to
// mutate. The shallow copy is safe because Validate and writeContract
// never modify any slice or nested struct; value-type sub-structs
// (Evaluator, Inputs, Budget) are deep-copied by the shallow copy
// itself.
//
// Atomicity: the new contract is written, then the update event is
// appended. If the event append fails, the original contract is
// restored and the caller's struct is NOT mutated — the method's
// failure semantics match "operation did not happen."
func (s *Store) Update(c *Contract) error {
	if c == nil {
		return fmt.Errorf("contract is nil")
	}
	return s.withLock(c.MissionID, func() error {
		dest, err := s.contractPath(c.MissionID)
		if err != nil {
			return err
		}
		// Read the current bytes for rollback before touching the file.
		oldData, err := os.ReadFile(dest)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("mission %q not found", c.MissionID)
			}
			return fmt.Errorf("reading mission %q: %w", c.MissionID, err)
		}
		// WriteSetReleasedAt is preserved from disk, never taken from
		// the caller -- the same reasoning as ApplyServerFields'
		// create-time strip, applied here to the update path (found
		// by Copilot review). Update has no dedicated audit event or
		// required reason the way ForceReleaseWriteSet does; letting
		// a caller set or clear the field through Update would let
		// admission control be bypassed silently under a generic
		// "update" event, undermining the recovery-only semantics
		// ForceReleaseWriteSet exists to enforce.
		onDisk, err := DecodeContractStrict(oldData, c.MissionID)
		if err != nil {
			return fmt.Errorf("reading mission %q: %w", c.MissionID, err)
		}
		updated := *c
		updated.WriteSetReleasedAt = onDisk.WriteSetReleasedAt
		updated.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := s.validateContract(&updated); err != nil {
			return fmt.Errorf("invalid contract: %w", err)
		}
		if err := s.writeContract(&updated); err != nil {
			return err
		}
		if err := s.appendEventLocked(c.MissionID, Event{
			TS:    updated.UpdatedAt,
			Event: "update",
			Actor: updated.Leader,
		}); err != nil {
			if rbErr := s.restoreContract(dest, oldData); rbErr != nil {
				return fmt.Errorf("update: event append failed: %w; rollback failed: %v", err, rbErr)
			}
			return fmt.Errorf("update: event append failed, contract rolled back: %w", err)
		}
		// Success: reflect server-owned fields back to the caller --
		// this mutation happens only after the event log commits, so
		// a failed Update leaves the caller's struct unchanged.
		// WriteSetReleasedAt is included alongside UpdatedAt so a
		// caller that tried to set or clear it sees the disk-preserved
		// value it actually got, not its own discarded input.
		c.UpdatedAt = updated.UpdatedAt
		c.WriteSetReleasedAt = updated.WriteSetReleasedAt
		return nil
	})
}

// Close transitions a mission to the given terminal status (closed,
// failed, or escalated), sets ClosedAt, and appends a "close" event.
// Returns the satisfying result that authorized the transition so
// the caller can echo round and verdict without re-reading disk.
//
// Phase 3.6: Close is gated on a result artifact for the mission's
// current round. The gate fires unconditionally at the store
// boundary — neither the CLI nor MCP can bypass it, because the
// refusal lives here, not in the entry-point code. The refusal
// message names the mission, the missing round, and the submission
// command the operator should run. There is no override flag: the
// point of the gate is the invariant.
//
// The returned *Result is non-nil exactly when the error is nil:
// the gate already verified the result exists during the locked
// section, so returning it to the caller closes the TOCTOU window
// a post-Close LoadResult would otherwise open. On failure the
// method returns (nil, err).
//
// Atomicity: the new closed state is written, then the close event is
// appended. If the event append fails, the original contract bytes
// are restored — a failed Close leaves the on-disk state unchanged.
func (s *Store) Close(missionID, status string) (*Result, error) {
	// StatusAbandoned is excluded here on purpose: it is not a status a
	// caller can request through Close. Abandon is a separate, more
	// narrowly gated operation (see Store.Abandon below) — routing
	// "abandoned" through Close would let a caller retire a mission
	// with a real result artifact on record, which is exactly the
	// case Abandon's gate exists to refuse.
	if !validStatuses[status] || status == StatusOpen || status == StatusAbandoned {
		return nil, fmt.Errorf("invalid close status %q: must be closed, failed, or escalated", status)
	}
	var satisfying *Result
	var closed *Contract
	err := s.withLock(missionID, func() error {
		dest, err := s.contractPath(missionID)
		if err != nil {
			return err
		}
		// loadLocked returns both the parsed contract and the raw
		// bytes, so Close reads the file only once and keeps the
		// original bytes for rollback if the event append fails.
		c, oldData, err := s.loadLocked(missionID)
		if err != nil {
			return err
		}
		// Refuse to re-close a mission that's already in a terminal
		// state. Re-closing would silently overwrite the original
		// closed_at timestamp and append a duplicate "close" event
		// to the JSONL log, which breaks the log's one-transition-
		// per-event invariant.
		if c.Status != StatusOpen {
			return fmt.Errorf("mission %q is already in terminal state %q", missionID, c.Status)
		}
		// Phase 3.6: result gate. A mission cannot transition to a
		// terminal status unless the current round has a valid result
		// artifact on disk. The gate is the whole point of the phase:
		// the leader's final verdict must be backed by the worker's
		// structured output, not prose left in chat.
		//
		// The gate returns the satisfying result so the close event
		// can record the round number and verdict — round 2 of Phase
		// 3.6 added this so an auditor reading the JSONL does not
		// have to scan back across round_advanced events to
		// reconstruct which result authorized the terminal transition.
		gated, err := s.checkResultGateLocked(c)
		if err != nil {
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339)
		c.Status = status
		c.ClosedAt = now
		c.UpdatedAt = now
		if err := s.validateContract(c); err != nil {
			return fmt.Errorf("invalid contract after close: %w", err)
		}
		if err := s.writeContract(c); err != nil {
			return err
		}
		closeDetails := map[string]any{
			"status":  status,
			"round":   gated.Round,
			"verdict": gated.Verdict,
		}
		if err := s.appendEventLocked(missionID, Event{
			TS:      now,
			Event:   "close",
			Actor:   c.Leader,
			Details: closeDetails,
		}); err != nil {
			if rbErr := s.restoreContract(dest, oldData); rbErr != nil {
				return fmt.Errorf("close: event append failed: %w; rollback failed: %v", err, rbErr)
			}
			return fmt.Errorf("close: event append failed, contract rolled back: %w", err)
		}
		// Publish the satisfying result only after the close event
		// commits. A mid-method failure leaves satisfying nil, matching
		// the "operation did not happen" contract the Update and Close
		// rollback paths already guarantee for on-disk state.
		satisfying = gated
		closed = c
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Close open delegation skeletons. Non-fatal — the mission is
	// already closed; a delegation-close failure writes stderr but
	// does not roll back the mission close. The verdict maps from the
	// mission's result verdict (pass/fail) to the delegation-level
	// vocabulary (pass/fail/error/aborted).
	//
	// The sweep runs under AcquireMissionLockExclusive so it waits for
	// every in-flight AcquireMissionLock (shared) holder — a
	// concurrent dispatchTierB — to release before it enumerates
	// delegations/. Without this, a dispatch that read status: open a
	// moment before this Close committed could still be mid-write when
	// the sweep walked the directory (docs/design-delegation-lifecycle.md
	// facet 1). A lock-acquisition failure falls back to an unlocked
	// sweep (below) rather than skipping it outright — a skipped sweep
	// is the exact bug this fix exists to close, just via a new
	// mechanism.
	//
	// The lock is taken only when the repo-tree per-mission directory
	// already exists — determined by os.IsNotExist on the Stat error,
	// never a bare "err != nil" check, which would also treat EACCES,
	// EIO, or ELOOP as "does not exist" and skip locking for a
	// directory that may well be present. AcquireMissionLockExclusive's
	// own MkdirAll would otherwise create that directory as a side
	// effect of closing a mission whose data lives entirely in the
	// legacy global tree — there is nothing to make quiescent (no
	// dispatchTierB call under this repoRoot could be in flight for
	// this mission, since its first act is the same MkdirAll) and no
	// repo-tree footprint should appear where none existed
	// (TestStore_TwoRoot_CloseStaysInItsLayer).
	if s.repoRoot != "" {
		delegationVerdict := DelegationVerdictPass
		if satisfying != nil && satisfying.Verdict == VerdictFail {
			delegationVerdict = DelegationVerdictFail
		}
		closedAt := time.Now().UTC().Format(time.RFC3339)
		missionDir := RepoStatePath(s.repoRoot, "missions", filepath.Base(missionID))
		_, statErr := os.Stat(missionDir)
		switch {
		case missingRepoTreeDir(statErr):
			closeDelegationSkeletons(s.repoRoot, missionID, delegationVerdict, closedAt)
		default:
			if statErr != nil {
				fmt.Fprintf(os.Stderr,
					"ethos: mission %s: stat %s failed, treating directory as present: %v\n",
					missionID, missionDir, statErr)
			}
			if releaseExcl, lockErr := AcquireMissionLockExclusive(s.repoRoot, missionID); lockErr != nil {
				fmt.Fprintf(os.Stderr,
					"ethos: mission %s: acquiring exclusive lock for delegation sweep: %v — "+
						"falling back to unlocked sweep — a concurrent dispatchTierB may still "+
						"race in a late delegation write; open delegations WILL be closed, but "+
						"a narrow TOCTOU window remains\n",
					missionID, lockErr)
				closeDelegationSkeletons(s.repoRoot, missionID, delegationVerdict, closedAt)
			} else {
				closeDelegationSkeletons(s.repoRoot, missionID, delegationVerdict, closedAt)
				releaseExcl()
			}
		}
	}

	// Trace: append a summary line to the repo-local JSONL log.
	// Non-fatal — the mission is already closed; a trace failure
	// must not roll back the close.
	if err := s.appendTraceSummary(closed, satisfying); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission %s: trace write failed: %v\n", missionID, err)
	}
	return satisfying, nil
}

// missingRepoTreeDir reports whether statErr indicates the repo-tree
// per-mission directory legitimately does not exist — the only case
// where Close's delegation sweep may skip AcquireMissionLockExclusive
// and go straight to an unlocked sweep. Any other stat failure
// (EACCES, EIO, ELOOP, ...) means the directory may well be present,
// so a nil statErr and a non-ENOENT statErr are treated alike: both
// fall through to the locked branch in Close.
func missingRepoTreeDir(statErr error) bool {
	return statErr != nil && os.IsNotExist(statErr)
}

// closeDelegationSkeletons walks delegations/ under the per-mission
// directory and closes any skeleton whose verdict is still "open".
func closeDelegationSkeletons(repoRoot, missionID, verdict, closedAt string) {
	delegationsDir := filepath.Join(
		RepoStatePath(repoRoot, "missions"),
		filepath.Base(missionID), "delegations",
	)
	entries, err := os.ReadDir(delegationsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "ethos: mission %s: reading delegations dir: %v\n", missionID, err)
		}
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		recordPath := filepath.Join(delegationsDir, e.Name(), "record.yaml")
		d, loadErr := LoadDelegation(recordPath)
		if loadErr != nil {
			fmt.Fprintf(os.Stderr, "ethos: mission %s: loading delegation %s for close: %v\n", missionID, e.Name(), loadErr)
			continue
		}
		if d.Verdict != "open" {
			continue
		}
		if closeErr := CloseDelegationSkeleton(repoRoot, missionID, e.Name(), verdict, closedAt); closeErr != nil {
			fmt.Fprintf(os.Stderr, "ethos: mission %s: closing delegation %s: %v\n", missionID, e.Name(), closeErr)
		}
	}
}

// Abandon retires a mission that was created but never actually
// dispatched to a worker — zero delegation records under
// delegations/, zero result artifacts for any round — into the
// StatusAbandoned terminal state. It is a distinct, more narrowly
// gated operation from Close, not a bypass of it.
//
// Close's result gate (checkResultGateLocked, see the comment above
// Close) is intentionally unconditional: a mission cannot close
// without a result artifact for the current round, because a
// terminal verdict must be backed by structured worker output. That
// invariant is correct and stays correct — Abandon does not weaken
// it or add an override flag to Close.
//
// Abandon answers a different question: was there ever any work to
// lose? A mission whose delegations/ directory is empty and whose
// results file is empty never had a worker spawned against it — the
// "create" event is the only entry in its event log. Retiring such a
// mission cannot discard anything, so it does not need Close's
// verdict gate. Any sign that work started — a delegation record
// (even a still-open skeleton), or a result for any round, not only
// the current one — refuses the transition and points the caller at
// Close instead. There is no override flag here either, for the same
// reason Close has none: the gate is the whole point.
//
// The distinct StatusAbandoned value (rather than reusing
// StatusClosed) matters for the same reason Close's terminal states
// are distinct from each other: an auditor reading `mission list` or
// the trace log needs to tell "this mission produced a verdict" from
// "this mission never started" without cross-referencing the event
// log for every row.
//
// Excluding abandoned missions from checkWriteSetConflicts is
// automatic and requires no change to that function: it only
// considers missions with Status == StatusOpen, and Abandon moves the
// mission's status to StatusAbandoned in the same locked section that
// commits the abandon event. This is the actual fix for the blocking
// bug the operator reported: a mission that was created via
// dispatch/create but never had a worker spawned can now be retired,
// which frees its write_set for a new Create the moment Abandon
// commits.
//
// reason is required (non-empty after trimming) and is recorded on
// the abandon event's Details map so the audit trail records why the
// mission was retired, not just that it was.
func (s *Store) Abandon(missionID, reason string) (*Contract, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("abandon: reason is required")
	}
	if containsControlChar(reason) {
		return nil, fmt.Errorf("abandon: reason contains control character")
	}
	// The mission tree is git-tracked and routinely pushed to a
	// public repo (see the storage layout table in CLAUDE.md), and
	// reason is operator-supplied free text — the same class of
	// content WriteDelegationSkeleton redacts before it touches disk
	// (see the rationale on NewPathRedactor). A reason like
	// "superseded by the contract in /Users/alice/work/x.yaml" would
	// otherwise leak the operator's home path and username into
	// shared history. Redaction failure is fail-closed, not
	// best-effort: an unresolvable $HOME means the write cannot be
	// redacted, so Abandon refuses rather than write unredacted
	// content — mirroring the empty-repoRoot refusal below.
	//
	// Built here, before the lock and before any mutation, not next
	// to the appendEventLocked call it feeds: NewPathRedactor depends
	// only on s.repoRoot and the environment, nothing the locked
	// section produces, so there is no reason to defer it. Deferring
	// it used to mean writeContract could already have stamped
	// status: abandoned on disk before this could fail — the only
	// error path in Abandon that did not call restoreContract, since
	// by the time it fired dest had already been overwritten with no
	// prior bytes captured for rollback. A retry then hit "already in
	// terminal state" with no abandon event ever recorded, and no way
	// back to open. Validating every precondition before the lock
	// means every failure in this function happens before any
	// mutation, so there is nothing to roll back and nothing that can
	// wedge.
	redact, err := NewPathRedactor(s.repoRoot)
	if err != nil {
		return nil, fmt.Errorf("abandon: building path redactor: %w", err)
	}
	var abandoned *Contract
	err = s.withLock(missionID, func() error {
		dest, err := s.contractPath(missionID)
		if err != nil {
			return err
		}
		c, oldData, err := s.loadLocked(missionID)
		if err != nil {
			return err
		}
		// Refuse an already-terminal mission for the same reason Close
		// does: re-transitioning would silently overwrite the original
		// closed_at timestamp and append a second terminal event to the
		// JSONL log.
		if c.Status != StatusOpen {
			return fmt.Errorf(
				"mission %q is already in terminal state %q; abandon only applies to open missions",
				missionID, c.Status,
			)
		}
		// Gate 1: zero delegation records. Any entry under
		// delegations/ — open, closed, any verdict — means a worker
		// was actually spawned against this contract. That is real
		// work; Close's result gate, not Abandon, is the correct
		// arbiter of whether it may retire.
		//
		// A missing repoRoot must REFUSE, not skip: an empty
		// s.repoRoot means countDelegations has no directory to look
		// under, so "no delegations found" would be indistinguishable
		// from "delegations exist but we didn't check." djb's probe
		// proved the earlier `if s.repoRoot != ""` guard let a mission
		// with a real spawned worker abandon cleanly with no error
		// when repoRoot was empty — silently trusting the absence of
		// evidence as evidence of absence. Fail closed instead: the
		// operator gets an actionable error naming the fix (run from
		// inside the repo checkout), not a silently unsafe abandon.
		if s.repoRoot == "" {
			return fmt.Errorf(
				"mission %q cannot be abandoned: no repo root in scope, so the "+
					"delegations/ gate cannot be evaluated; run abandon from inside the repo checkout",
				missionID,
			)
		}
		n, dErr := countDelegations(s.repoRoot, missionID)
		if dErr != nil {
			return fmt.Errorf("abandon: checking delegations for %q: %w", missionID, dErr)
		}
		if n > 0 {
			return fmt.Errorf(
				"mission %q cannot be abandoned: %d delegation record(s) exist under delegations/; "+
					"a worker was spawned, so this mission may have recoverable work — "+
					"submit a result and run `ethos mission close %s` instead",
				missionID, n, missionID,
			)
		}
		// Gate 2: zero result artifacts, for any round — not only the
		// mission's current round. A result recorded for an earlier
		// round (e.g. the mission advanced past a round that still
		// produced output) is exactly the recoverable-work case this
		// gate exists to catch.
		results, rErr := s.loadResultsLocked(missionID)
		if rErr != nil {
			return fmt.Errorf("abandon: loading results for %q: %w", missionID, rErr)
		}
		if len(results) > 0 {
			rounds := make([]string, len(results))
			for i, r := range results {
				rounds[i] = fmt.Sprintf("%d", r.Round)
			}
			return fmt.Errorf(
				"mission %q cannot be abandoned: result artifact(s) exist for round(s) %s; "+
					"a result means the worker produced output — run `ethos mission close %s` instead",
				missionID, strings.Join(rounds, ", "), missionID,
			)
		}

		now := time.Now().UTC().Format(time.RFC3339)
		c.Status = StatusAbandoned
		c.ClosedAt = now
		c.UpdatedAt = now
		if err := s.validateContract(c); err != nil {
			return fmt.Errorf("invalid contract after abandon: %w", err)
		}
		if err := s.writeContract(c); err != nil {
			return err
		}
		// redact was built before the lock (see the comment at the top
		// of Abandon) so its construction cannot fail here, after
		// writeContract has already stamped the terminal state.
		if err := s.appendEventLocked(missionID, Event{
			TS:    now,
			Event: "abandon",
			Actor: c.Leader,
			Details: redact.Map(map[string]any{
				"reason": reason,
			}),
		}); err != nil {
			if rbErr := s.restoreContract(dest, oldData); rbErr != nil {
				return fmt.Errorf("abandon: event append failed: %w; rollback failed: %v", err, rbErr)
			}
			return fmt.Errorf("abandon: event append failed, contract rolled back: %w", err)
		}
		abandoned = c
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Trace: append a summary line, mirroring Close. Non-fatal — the
	// mission is already abandoned on disk; a trace failure must not
	// roll back the transition. There is no satisfying result to pass
	// through (that's the entire premise of Abandon), so the synthetic
	// Result carries only the terminal-state verdict string for the
	// trace log's benefit; buildTraceSummary otherwise reads the
	// abandoned contract's own fields.
	if err := s.appendTraceSummary(abandoned, &Result{Verdict: StatusAbandoned}); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission %s: trace write failed: %v\n", missionID, err)
	}
	return abandoned, nil
}

// ForceReleaseWriteSet marks an open mission's write_set/extract_into
// as released from admission control (WriteSetReleasedAt), so
// checkWriteSetConflicts stops treating it as claiming those paths.
// It does NOT clear WriteSet or ExtractInto themselves: most
// archetypes (implement, test, investigate, design, ...) require a
// non-empty write_set (validate.go rule 11), so emptying it would
// pass this write but fail every future Load -- mission show, mission
// list, even a later Close or Abandon on this same mission would then
// error forever. The declared write_set stays on the contract for
// validation and audit history; only its effect on admission control
// changes. Also does not touch Status, SuccessCriteria, Budget,
// Worker, Evaluator, or any other planning field. UpdatedAt does
// change -- releasing a claim is itself a modification, the same as
// any other Store write.
// It is a leader-invoked, explicit, audited release of an
// admission-control claim — not a terminal transition, and not a
// substitute for Abandon or Close. A leader who can already prove
// zero delegations and zero results should use Abandon instead: that
// is a real terminal state, not a half-measure. See
// docs/mission-force-release-write-set.md.
//
// Modeled directly on Store.Abandon: same locking discipline
// (withLock), same NewPathRedactor-before-lock construction for
// reason — the rationale at Abandon's definition applies unchanged
// here, since reason is leader-supplied free text landing in a
// git-tracked, publicly-pushed event log — and the same
// every-failure-before-any-mutation discipline, so a refusal never
// leaves a half-mutated contract on disk.
//
// Deliberately has no automatic age or delegation-count gate.
// Staleness is a heuristic judgment, not a correctness invariant —
// the leader consults the Staleness signal (staleness.go; surfaced
// via mission show / mission list --stale-days once that CLI surface
// lands -- see docs/mission-force-release-write-set.md) before
// deciding, and this method does not
// re-derive or enforce that judgment. Its own gate covers only
// correctness: the mission must be open, and there must be something
// to release.
func (s *Store) ForceReleaseWriteSet(missionID, reason string) (*Contract, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("force-release-write-set: reason is required")
	}
	if containsControlChar(reason) {
		return nil, fmt.Errorf("force-release-write-set: reason contains control character")
	}
	// Built before the lock and before any mutation, for the same
	// reason Abandon's redactor is: a construction failure here must
	// never follow a writeContract call that already stamped
	// WriteSetReleasedAt on disk with no release event to explain it.
	redact, err := NewPathRedactor(s.repoRoot)
	if err != nil {
		return nil, fmt.Errorf("force-release-write-set: building path redactor: %w", err)
	}
	// Wrapped in the create lock, same nesting order Create uses
	// (create lock -> per-mission lock), so a concurrent Create's
	// cross-mission conflict scan can never observe the brief window
	// between writeContract stamping WriteSetReleasedAt and
	// appendEventLocked confirming it. Without this, Create could
	// admit an overlapping mission against a release that later rolls
	// back on event-append failure, leaving two open missions with
	// intersecting write_sets on disk -- exactly what admission
	// control exists to prevent.
	var released *Contract
	err = s.withCreateLock(func() error {
		return s.withLock(missionID, func() error {
			dest, err := s.contractPath(missionID)
			if err != nil {
				return err
			}
			c, oldData, err := s.loadLocked(missionID)
			if err != nil {
				return err
			}
			// Gate 1: mission must be open. A terminal mission's write_set
			// is already excluded from checkWriteSetConflicts by virtue of
			// Status != StatusOpen, so there is nothing to release.
			if c.Status != StatusOpen {
				return fmt.Errorf(
					"mission %q is already in terminal state %q; force-release-write-set only applies to open missions",
					missionID, c.Status,
				)
			}
			// Gate 2: refuse a no-op release rather than silently succeed
			// and write a misleading event -- either the mission was
			// already released, or it never claimed anything to begin
			// with.
			if c.WriteSetReleasedAt != "" {
				return fmt.Errorf(
					"mission %q already has its write_set released (at %s); nothing to release",
					missionID, c.WriteSetReleasedAt,
				)
			}
			if len(c.WriteSet) == 0 && len(c.ExtractInto) == 0 {
				return fmt.Errorf(
					"mission %q already has an empty write_set and extract_into; nothing to release",
					missionID,
				)
			}

			// The staleness snapshot recorded on the event is informational,
			// not gating (see the doc comment above): every input below
			// degrades to "unknown" on failure rather than refusing the
			// whole operation, unlike Abandon's gate 1. This matters most
			// exactly when it is hardest to satisfy — a corrupt audit
			// chunk or an oversized log is the kind of thing that leaves a
			// mission stuck in the first place, and this method exists to
			// unstick it; a hard error here would make ForceReleaseWriteSet
			// unusable in precisely the scenario it is the recovery path
			// for.
			delegationsKnown := s.repoRoot != ""
			delegationCount := 0
			if delegationsKnown {
				n, dErr := countDelegations(s.repoRoot, missionID)
				if dErr != nil {
					delegationsKnown = false
					fmt.Fprintf(os.Stderr, "ethos: mission %s: counting delegations for staleness snapshot: %v\n", missionID, dErr)
				} else {
					delegationCount = n
				}
			}
			var events []Event
			eventsKnown := true
			loadedEvents, warnings, evErr := s.LoadEvents(missionID)
			if evErr != nil {
				eventsKnown = false
				fmt.Fprintf(os.Stderr, "ethos: mission %s: loading events for staleness snapshot: %v\n", missionID, evErr)
			} else {
				events = loadedEvents
				for _, w := range warnings {
					fmt.Fprintf(os.Stderr, "ethos: mission %s: %s\n", missionID, w)
				}
			}
			var results []Result
			resultsKnown := true
			loadedResults, rErr := s.loadResultsLocked(missionID)
			if rErr != nil {
				resultsKnown = false
				fmt.Fprintf(os.Stderr, "ethos: mission %s: loading results for staleness snapshot: %v\n", missionID, rErr)
			} else {
				results = loadedResults
			}
			now := time.Now().UTC()
			st := Staleness(c, events, results, delegationCount, delegationsKnown, now)

			writeSetAtRelease := append([]string(nil), c.WriteSet...)
			extractIntoAtRelease := append([]string(nil), c.ExtractInto...)

			nowStr := now.Format(time.RFC3339)
			c.WriteSetReleasedAt = nowStr
			c.UpdatedAt = nowStr
			// WriteSet and ExtractInto are untouched, so the normal
			// archetype-resolving validation path applies unchanged --
			// releasing a claim never violates rule 11, since the
			// write_set this rule checks was never cleared.
			if err := s.validateContract(c); err != nil {
				return fmt.Errorf("invalid contract after force-release-write-set: %w", err)
			}
			if err := s.writeContract(c); err != nil {
				return err
			}
			delegationDetail := any(delegationCount)
			if !delegationsKnown {
				delegationDetail = "unknown"
			}
			lastActivityDetail := any(st.LastActivityAt)
			ageDaysDetail := any(st.AgeDays)
			if !eventsKnown || !st.AgeDaysKnown {
				lastActivityDetail = "unknown"
				ageDaysDetail = "unknown"
			}
			hasResultsDetail := any(st.HasResults)
			if !resultsKnown {
				hasResultsDetail = "unknown"
			}
			if err := s.appendEventLocked(missionID, Event{
				TS:    nowStr,
				Event: EventWriteSetReleased,
				Actor: c.Leader,
				Details: redact.Map(map[string]any{
					"reason":                  reason,
					"write_set_at_release":    writeSetAtRelease,
					"extract_into_at_release": extractIntoAtRelease,
					"staleness_snapshot": map[string]any{
						"last_activity_at": lastActivityDetail,
						"age_days":         ageDaysDetail,
						"has_results":      hasResultsDetail,
						"delegation_count": delegationDetail,
					},
				}),
			}); err != nil {
				if rbErr := s.restoreContract(dest, oldData); rbErr != nil {
					return fmt.Errorf("force-release-write-set: event append failed: %w; rollback failed: %v", err, rbErr)
				}
				return fmt.Errorf("force-release-write-set: event append failed, contract rolled back: %w", err)
			}
			released = c
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return released, nil
}

// countDelegations returns the number of delegation record
// subdirectories under the mission's delegations/ directory. Mirrors
// the walk closeDelegationSkeletons uses, but only counts — Abandon's
// gate does not care about verdict, only whether a worker was ever
// spawned. A missing delegations/ directory (never created because no
// worker was ever spawned — the common case) reports zero, not an
// error.
func countDelegations(repoRoot, missionID string) (int, error) {
	delegationsDir := filepath.Join(
		RepoStatePath(repoRoot, "missions"),
		filepath.Base(missionID), "delegations",
	)
	entries, err := os.ReadDir(delegationsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	return n, nil
}

// loadLocked reads a contract without acquiring the flock. Callers must
// already hold the lock for the given missionID. Returns both the parsed
// contract and the raw bytes so callers that need the bytes for rollback
// (Close, Update) don't have to read the file twice.
//
// Decodes with KnownFields(true) and runs Validate() for symmetry with
// the public Load() — a corrupt or hand-edited contract must be
// rejected before Close (or any future locked caller) mutates it.
// Otherwise an invalid on-disk state could slip through Close's
// post-mutation Validate because the mutation fixed the field under
// inspection.
func (s *Store) loadLocked(missionID string) (*Contract, []byte, error) {
	path, err := s.contractPath(missionID)
	if err != nil {
		return nil, nil, err
	}
	if err := rejectSymlink(path); err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("mission %q not found", missionID)
		}
		return nil, nil, fmt.Errorf("reading mission %q: %w", missionID, err)
	}
	c, err := s.decodeAndValidate(data, missionID)
	if err != nil {
		return nil, nil, err
	}
	return c, data, nil
}

// DecodeContractStrict parses a YAML contract with strict rules: every
// field must be known to the Contract struct, and exactly one YAML
// document must be present. Multi-document YAML or trailing content
// after the first document is rejected — otherwise a caller could
// smuggle extra content past the trust boundary by appending it to
// a legitimate contract.
//
// This helper is the single entry point for YAML → Contract decoding
// in the mission package. Both the CLI (`ethos mission create --file`)
// and the MCP `mission create` handler use it, as do the Store's
// Load/loadLocked paths, so the on-disk trust boundary matches the
// input trust boundary exactly.
//
// The label argument is a human-readable identifier (mission ID or
// file path) used in error messages to help operators locate the
// source of a parse failure.
func DecodeContractStrict(data []byte, label string) (*Contract, error) {
	var c Contract
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("invalid contract %s: %w", label, err)
	}
	// Enforce single-document input: a second Decode must return
	// io.EOF. Anything else means there was more content — either a
	// second YAML document (separated by `---`) or trailing scalars.
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("invalid contract %s: multiple YAML documents are not allowed", label)
		}
		return nil, fmt.Errorf("invalid contract %s: trailing content after first document: %w", label, err)
	}
	return &c, nil
}

// List returns all mission IDs known to the store. In two-tree
// mode (NewStoreWithRoots), the union of the repo and global trees
// is returned with repo-wins dedup — a mission ID present in both
// trees appears once, sourced from the repo. In legacy single-tree
// mode (NewStore), only the flat global directory is walked.
//
// The two trees have different file shapes: the repo tree carries
// per-mission directories (<repoRoot>/.punt-labs/ethos/missions/<id>/contract.yaml),
// the global tree carries flat files (<globalRoot>/missions/<id>.yaml).
// Both shapes are normalized to a bare mission ID before merging.
func (s *Store) List() ([]string, error) {
	seen := make(map[string]struct{})
	var ids []string

	// Repo tree (when active). A per-mission subdirectory holding
	// contract.yaml counts as one mission. Empty subdirectories or
	// directories without a contract.yaml are skipped — they may be
	// in-flight Creates or stale state, not first-class entries.
	if s.twoTreeStorage && s.repoRoot != "" {
		repoEntries, err := os.ReadDir(s.repoMissionsDir())
		switch {
		case err == nil:
			for _, entry := range repoEntries {
				if !entry.IsDir() {
					continue
				}
				name := entry.Name()
				if strings.HasPrefix(name, ".") {
					continue
				}
				contractFile := filepath.Join(s.repoMissionsDir(), name, "contract.yaml")
				if _, statErr := os.Stat(contractFile); statErr != nil {
					continue
				}
				if _, dup := seen[name]; dup {
					continue
				}
				seen[name] = struct{}{}
				ids = append(ids, name)
			}
		case os.IsNotExist(err):
			// First-run repo with no missions yet — fall through.
		default:
			return nil, fmt.Errorf("reading repo missions directory: %w", err)
		}
	}

	// Global tree. Flat-shape files; sibling artifacts are filtered
	// by isContractFile.
	globalEntries, err := os.ReadDir(s.missionsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return ids, nil
		}
		return nil, fmt.Errorf("reading missions directory: %w", err)
	}
	for _, entry := range globalEntries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isContractFile(name) {
			continue
		}
		id := strings.TrimSuffix(name, ".yaml")
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

// isContractFile reports whether a missions-directory entry name is a
// mission contract YAML file. A contract file ends in ".yaml" but is
// not a sibling file (".reflections.yaml", ".results.yaml", and any
// future ".annotations.yaml" / ".notes.yaml" the package grows) and
// is not a dotfile such as ".counter-YYYY-MM-DD" or ".create.lock".
//
// Centralizes the decision so future sibling file layouts add one
// case here rather than re-finding the same filtering bug in every
// reader. The catastrophic Phase 3.4 regression was a List() that
// did not exclude ".reflections.yaml", which made every mission with
// a reflection look like two missions — breaking Create's cross-
// mission conflict scan, Show's prefix match, and List's decode.
// Phase 3.6 adds ".results.yaml" to the same filter so a result file
// on disk cannot reproduce that failure mode.
func isContractFile(name string) bool {
	if strings.HasPrefix(name, ".") {
		return false
	}
	if !strings.HasSuffix(name, ".yaml") {
		return false
	}
	if strings.HasSuffix(name, ".reflections.yaml") {
		return false
	}
	if strings.HasSuffix(name, ".results.yaml") {
		return false
	}
	return true
}

// MatchByPrefix finds a mission ID from a prefix string. Mirrors
// session.Store.MatchByPrefix: an exact match wins; otherwise the prefix
// must match exactly one ID. Zero or multiple matches are an error.
func (s *Store) MatchByPrefix(prefix string) (string, error) {
	ids, err := s.List()
	if err != nil {
		return "", fmt.Errorf("listing missions: %w", err)
	}
	var matches []string
	for _, id := range ids {
		if id == prefix {
			return id, nil // exact match wins
		}
		if len(id) >= len(prefix) && id[:len(prefix)] == prefix {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no mission matching prefix %q", prefix)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous prefix %q: matches %d missions", prefix, len(matches))
	}
}

// writeContract marshals and writes a contract atomically via temp
// file plus rename. The caller must hold the contract's flock.
//
// In the two-root layout, ensureMissionDir creates the per-mission
// directory under <repoRoot>/.punt-labs/ethos/missions/<id>/ before the temp
// file is opened; the legacy single-root layout has no per-mission
// directory and skips the mkdir.
func (s *Store) writeContract(c *Contract) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshaling contract: %w", err)
	}
	if err := s.ensureMissionDir(c.MissionID); err != nil {
		return err
	}
	dest, err := s.contractPath(c.MissionID)
	if err != nil {
		return err
	}
	tmp := dest + ".tmp"
	// Uniform symlink policy (paths.go): refuse a symlink at dest OR
	// at the temp path. os.WriteFile would follow a symlink at tmp,
	// writing through to the link target (write-set bypass); a symlink
	// at dest would be replaced by Rename via inode, but reject it too
	// so an attacker cannot redirect any read between this write and
	// the next loadLocked under the same flock.
	if err := rejectSymlink(dest); err != nil {
		return err
	}
	if err := rejectSymlink(tmp); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing temp contract: %w", err)
	}
	return os.Rename(tmp, dest)
}

// restoreContract writes oldData back to dest atomically via temp+rename.
// Used by Update and Close to roll back a contract write when the
// follow-on event-log append fails, keeping the caller's view of
// on-disk state consistent with the operation's success/failure.
func (s *Store) restoreContract(dest string, oldData []byte) error {
	tmp := dest + ".tmp"
	// Uniform symlink policy (paths.go): the rollback path runs after
	// a failed event-log append, when an attacker may have raced to
	// plant a symlink at the temp path. Refuse before WriteFile.
	if err := rejectSymlink(dest); err != nil {
		return fmt.Errorf("writing rollback temp: %w", err)
	}
	if err := rejectSymlink(tmp); err != nil {
		return fmt.Errorf("writing rollback temp: %w", err)
	}
	if err := os.WriteFile(tmp, oldData, 0o600); err != nil {
		return fmt.Errorf("writing rollback temp: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("renaming rollback temp: %w", err)
	}
	return nil
}

// withLock executes fn while holding an exclusive flock on the mission's
// lock file. Mirrors session.Store.withLock.
func (s *Store) withLock(missionID string, fn func() error) error {
	if strings.TrimSpace(missionID) == "" {
		return fmt.Errorf("missionID is required")
	}
	if err := os.MkdirAll(s.missionsDir(), 0o700); err != nil {
		return fmt.Errorf("creating missions directory: %w", err)
	}
	lockFile := s.lockPath(missionID)
	// Uniform symlink policy (paths.go): a symlink at the lock path
	// would redirect the flock onto an unrelated inode, defeating the
	// per-mission serialization invariant. Reject before OpenFile,
	// which would otherwise create-and-follow the link.
	if err := rejectSymlink(lockFile); err != nil {
		return err
	}
	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("opening lock file: %w", err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquiring lock: %w", err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	return fn()
}

// withCreateLock executes fn while holding an exclusive flock on the
// missions directory's create lock file. Used by Store.Create to
// serialize Create attempts across cooperating processes so that the
// cross-mission write_set conflict scan and the new mission's write
// happen atomically with respect to other concurrent Creates.
//
// Update and Close do NOT acquire this lock — they mutate an existing
// mission's status, which is unrelated to Create-vs-Create
// serialization. The lock file is a stable filename that is never
// renamed or unlinked, so concurrent acquirers always lock the same
// inode.
func (s *Store) withCreateLock(fn func() error) error {
	if err := os.MkdirAll(s.missionsDir(), 0o700); err != nil {
		return fmt.Errorf("creating missions directory: %w", err)
	}
	createLock := s.createLockPath()
	// Uniform symlink policy (paths.go): a symlink at the directory-
	// level create lock would let an attacker redirect every Create's
	// flock onto an attacker-chosen inode, collapsing the cross-
	// mission write_set conflict scan's atomicity.
	if err := rejectSymlink(createLock); err != nil {
		return err
	}
	f, err := os.OpenFile(createLock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("opening create lock file: %w", err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquiring create lock: %w", err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	// DES-054 v5 rolling-upgrade fence — when a repoRoot is in scope,
	// also acquire the per-repo create lock as a nested flock so two
	// processes that share the same repo but different global roots
	// (e.g., separate ~/.punt-labs/ trees in containers) still
	// serialize on Create. Acquisition order is global → repo;
	// release is reverse via defer LIFO. v3.13.0 drops the global
	// acquisition once the field is fully on v3.12+.
	//
	// When repoRoot resolves to the same tree as globalRoot (test
	// fixtures and single-tree deployments after the .ethos →
	// .punt-labs/ethos relocation), the two lock paths point at the
	// same inode and a second Flock(LOCK_EX) on the same FD-table
	// entry blocks forever. Compare absolute paths first and skip
	// the second acquire when they collide.
	if repoLockPath := s.repoCreateLockPath(); repoLockPath != "" {
		absRepo, absRepoErr := filepath.Abs(repoLockPath)
		absGlobal, absGlobalErr := filepath.Abs(s.createLockPath())
		if absRepoErr != nil || absGlobalErr != nil {
			// Abs failed (deleted cwd or unresolvable path). Log and
			// proceed to acquire the second lock unconditionally —
			// double-locking the same inode with LOCK_EX from the
			// same goroutine is a no-op on macOS/Linux, so this is
			// safe even if the paths actually collide.
			fmt.Fprintf(os.Stderr,
				"ethos: withCreateLock: filepath.Abs failed (repo=%v global=%v); acquiring second lock unconditionally\n",
				absRepoErr, absGlobalErr)
		} else if absRepo == absGlobal {
			return fn()
		}
		if err := os.MkdirAll(filepath.Dir(repoLockPath), 0o700); err != nil {
			return fmt.Errorf("creating repo missions directory: %w", err)
		}
		// Uniform symlink policy (paths.go): the per-repo create lock
		// is the same trust boundary as the global one — refuse a
		// symlink here too.
		if err := rejectSymlink(repoLockPath); err != nil {
			return err
		}
		rf, rerr := os.OpenFile(repoLockPath, os.O_CREATE|os.O_RDWR, 0o600)
		if rerr != nil {
			return fmt.Errorf("opening repo create lock file: %w", rerr)
		}
		defer rf.Close()

		if rerr := syscall.Flock(int(rf.Fd()), syscall.LOCK_EX); rerr != nil {
			return fmt.Errorf("acquiring repo create lock: %w", rerr)
		}
		defer func() { _ = syscall.Flock(int(rf.Fd()), syscall.LOCK_UN) }()
	}

	return fn()
}

// --- 3.4: reflections and round advance ---

// reflectionsFile is the on-disk schema for the sibling
// .reflections.yaml file. The single Round-keyed sequence is the
// shape callers see; the wrapper struct exists so the file format
// can grow new top-level metadata without breaking decode.
//
// Two ordering invariants hold for the on-disk slice (and the helper
// methods enforce them on every write):
//
//  1. Reflections are sorted by Round ascending. The store rewrites
//     the file on every Append, and the rewrite preserves order.
//  2. Each Round value appears at most once. AppendReflection
//     refuses to add a duplicate, so the slice is dense.
type reflectionsFile struct {
	Reflections []Reflection `yaml:"reflections"`
}

// LoadReflections returns the reflections recorded for a mission, in
// round order. Missing file → empty slice; the absence of any
// reflection is the normal state for a brand-new round 1 mission.
//
// Decodes with KnownFields(true) so a hand-edited reflections file
// cannot smuggle extra keys past the trust boundary, symmetric with
// the contract decode path.
//
// A reflection with no mission: field is legacy data; decode fills it
// from missionID. See decodeReflectionsFile.
func (s *Store) LoadReflections(missionID string) ([]Reflection, error) {
	if strings.TrimSpace(missionID) == "" {
		return nil, fmt.Errorf("missionID is required")
	}
	path, err := s.reflectionsPath(missionID)
	if err != nil {
		return nil, err
	}
	if err := rejectSymlink(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading reflections for %q: %w", missionID, err)
	}
	parsed, err := decodeReflectionsFile(data, missionID)
	if err != nil {
		return nil, err
	}
	return parsed, nil
}

// decodeReflectionsFile parses a reflections.yaml body, runs Validate
// on every entry, and asserts the round-monotone invariant. Returns
// the decoded slice (nil if the file is empty/blank).
//
// A blank mission field is back-filled from missionID; a non-blank one
// that disagrees with missionID is refused.
func decodeReflectionsFile(data []byte, missionID string) ([]Reflection, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var wrapper reflectionsFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&wrapper); err != nil {
		return nil, fmt.Errorf("invalid reflections file %q: %w", missionID, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("invalid reflections file %q: multiple YAML documents are not allowed", missionID)
		}
		return nil, fmt.Errorf("invalid reflections file %q: trailing content: %w", missionID, err)
	}
	for i := range wrapper.Reflections {
		r := &wrapper.Reflections[i]
		// Legacy back-fill. Reflections written before the mission field
		// existed carry no mission: key, and every such file already sits
		// in missions/<id>/, so the containing directory names the mission
		// unambiguously. Fill a blank field from missionID before Validate
		// rather than refusing to read data that predates the field —
		// otherwise AdvanceRound cannot load prior rounds for any in-flight
		// mission. The write path is untouched: Validate still requires the
		// field of every newly submitted reflection.
		if strings.TrimSpace(r.Mission) == "" {
			r.Mission = missionID
		}
		if err := r.Validate(); err != nil {
			return nil, fmt.Errorf("reflections[%d] for %q: %w", i, missionID, err)
		}
		// On-disk trust symmetry with AppendReflection and the results
		// read path: the write path refuses a reflection whose Mission
		// does not match the target, so the read path must too — a
		// hand-edited file cannot claim a different mission. Only a
		// non-blank mismatch reaches here; a blank field was back-filled.
		if r.Mission != missionID {
			return nil, fmt.Errorf(
				"reflections[%d].mission: expected %q, got %q",
				i, missionID, r.Mission,
			)
		}
		if i > 0 && wrapper.Reflections[i-1].Round >= r.Round {
			return nil, fmt.Errorf(
				"reflections file %q is out of order or has duplicate round: %d after %d",
				missionID, r.Round, wrapper.Reflections[i-1].Round,
			)
		}
	}
	return wrapper.Reflections, nil
}

// AppendReflection records a reflection for a mission's current round.
// The append is append-only by construction: a duplicate Round is
// refused, and the file is rewritten via temp+rename so a partial
// write cannot leave a half-encoded YAML doc on disk.
//
// The caller-provided Reflection.Round must equal Contract.CurrentRound
// (the round the worker is currently in). Submitting a reflection for
// any other round is a programming error and is refused — the gate
// would otherwise have to chase out-of-order reflections at advance
// time, where the operator-facing error is much further from the bug.
//
// Validate runs before any disk I/O. CreatedAt is set to now if the
// caller left it blank. The reflect event is appended to the JSONL
// log inside the per-mission flock so concurrent advance/reflect
// attempts on the same mission serialize cleanly.
//
// Atomic from the caller's view: a write failure leaves the on-disk
// reflections file unchanged.
func (s *Store) AppendReflection(missionID string, r *Reflection) error {
	if r == nil {
		return fmt.Errorf("reflection is nil")
	}
	staged := *r
	// Normalize Author before persisting so whitespace around the
	// handle does not pollute the audit trail or the event log.
	// Parity with AppendResult — Phase 3.6 round 2 widened the class
	// fix to both sibling stores so the two surfaces stay symmetric.
	staged.Author = strings.TrimSpace(staged.Author)
	if strings.TrimSpace(staged.CreatedAt) == "" {
		staged.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if err := staged.Validate(); err != nil {
		return fmt.Errorf("invalid reflection: %w", err)
	}
	return s.withLock(missionID, func() error {
		c, _, err := s.loadLocked(missionID)
		if err != nil {
			return err
		}
		// Refuse to record a reflection on a closed mission. The
		// round-advance gate is also closed-status-aware, but rejecting
		// here gives a clearer diagnostic than "advance refused" later.
		if c.Status != StatusOpen {
			return fmt.Errorf("mission %q is in terminal state %q; reflections are accepted only on open missions", missionID, c.Status)
		}
		// Mission ID cross-check, symmetric with AppendResult: the
		// reflection's self-declared Mission must match the caller's
		// target so a file renamed between missions cannot slip past.
		if staged.Mission != missionID {
			return fmt.Errorf(
				"reflection mission %q does not match target mission %q",
				staged.Mission, missionID,
			)
		}
		if staged.Round != c.CurrentRound {
			return fmt.Errorf(
				"reflection round %d does not match mission %q current round %d",
				staged.Round, missionID, c.CurrentRound,
			)
		}
		existing, err := s.loadReflectionsLocked(missionID)
		if err != nil {
			return err
		}
		for _, e := range existing {
			if e.Round == staged.Round {
				return fmt.Errorf(
					"reflection for round %d of mission %q already exists; reflections are append-only",
					staged.Round, missionID,
				)
			}
		}
		updated := append(existing, staged)
		if err := s.writeReflectionsLocked(missionID, updated); err != nil {
			return err
		}
		if err := s.appendEventLocked(missionID, Event{
			TS:    staged.CreatedAt,
			Event: "reflect",
			Actor: staged.Author,
			Details: map[string]any{
				"round":          staged.Round,
				"recommendation": staged.Recommendation,
				"converging":     staged.Converging,
				"signal_count":   len(staged.Signals),
			},
		}); err != nil {
			// Roll back the reflections file so the mission's on-disk
			// state matches the operation's failure: if the event log
			// rejects the reflect record, the reflection itself must
			// not be observable to a later read.
			//
			// If the file did not exist before the append (existing
			// was nil), remove it entirely rather than writing an
			// empty reflections: [] stub that would confuse readers.
			if existing == nil {
				rbPath, pErr := s.reflectionsPath(missionID)
				if pErr != nil {
					return fmt.Errorf("reflect: event append failed: %w; rollback path failed: %v", err, pErr)
				}
				if rbErr := os.Remove(rbPath); rbErr != nil && !os.IsNotExist(rbErr) {
					return fmt.Errorf("reflect: event append failed: %w; rollback remove failed: %v", err, rbErr)
				}
			} else if rbErr := s.writeReflectionsLocked(missionID, existing); rbErr != nil {
				return fmt.Errorf("reflect: event append failed: %w; rollback failed: %v", err, rbErr)
			}
			return fmt.Errorf("reflect: event append failed, reflection rolled back: %w", err)
		}
		// Reflect CreatedAt back to the caller — this is the one field
		// AppendReflection is contracted to set. Author is always
		// caller-supplied; the default-fill is restricted to CreatedAt
		// so the caller's Reflection.Author never changes behind its
		// back.
		r.CreatedAt = staged.CreatedAt
		return nil
	})
}

// loadReflectionsLocked is the lock-respecting twin of LoadReflections.
// The caller must already hold the per-mission flock; AppendReflection
// and AdvanceRound use it to read the existing slice without
// re-acquiring the lock and deadlocking.
func (s *Store) loadReflectionsLocked(missionID string) ([]Reflection, error) {
	path, err := s.reflectionsPath(missionID)
	if err != nil {
		return nil, err
	}
	if err := rejectSymlink(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading reflections for %q: %w", missionID, err)
	}
	return decodeReflectionsFile(data, missionID)
}

// writeReflectionsLocked rewrites the reflections file via temp+rename.
// Caller must hold the per-mission flock. The file is wrapped in a
// reflectionsFile struct so future top-level metadata (e.g. a
// schema_version key) can be added without breaking decode of older
// files.
func (s *Store) writeReflectionsLocked(missionID string, rs []Reflection) error {
	wrapper := reflectionsFile{Reflections: rs}
	data, err := yaml.Marshal(&wrapper)
	if err != nil {
		return fmt.Errorf("marshaling reflections: %w", err)
	}
	if err := s.ensureMissionDir(missionID); err != nil {
		return err
	}
	dest, err := s.reflectionsPath(missionID)
	if err != nil {
		return err
	}
	tmp := dest + ".tmp"
	// Uniform symlink policy (paths.go): refuse symlinks at dest and
	// tmp before WriteFile would follow them.
	if err := rejectSymlink(dest); err != nil {
		return err
	}
	if err := rejectSymlink(tmp); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing temp reflections: %w", err)
	}
	return os.Rename(tmp, dest)
}

// AdvanceRound is the round-advance gate. It moves a mission from
// round N to round N+1, refusing the move if any of the 3.4
// invariants is violated:
//
//  1. The mission is not open. Closed/failed/escalated missions are
//     out of the gate's purview.
//  2. The current round (N) has no reflection on disk. Reflection is
//     mandatory between rounds — that is the whole point of 3.4.
//  3. The current round's reflection recommended `stop` or
//     `escalate`. The gate surfaces the leader's reason verbatim so
//     the operator sees the leader's own words.
//  4. Advancing would exceed Budget.Rounds. The contract is the
//     trust boundary; the budget is load-bearing.
//
// On success, Contract.CurrentRound is bumped, the contract is
// rewritten, and a `round_advanced` event is appended to the log.
// The transition is atomic with respect to other operations on the
// same mission via the per-mission flock.
//
// Returns the new round number on success.
func (s *Store) AdvanceRound(missionID, actor string) (int, error) {
	if strings.TrimSpace(missionID) == "" {
		return 0, fmt.Errorf("missionID is required")
	}
	if strings.TrimSpace(actor) == "" {
		return 0, fmt.Errorf("actor is required")
	}
	var newRound int
	err := s.withLock(missionID, func() error {
		c, oldData, err := s.loadLocked(missionID)
		if err != nil {
			return err
		}
		if c.Status != StatusOpen {
			return fmt.Errorf("mission %q is in terminal state %q; cannot advance round", missionID, c.Status)
		}
		// Budget exhaustion check happens before the reflection check
		// so the operator sees the right diagnostic when they have
		// reflected on the final round and then tried to push past
		// the budget anyway. The right next step there is to close
		// the mission, not to record one more reflection.
		if c.CurrentRound >= c.Budget.Rounds {
			return fmt.Errorf(
				"mission %q has exhausted its round budget (%d/%d); close or re-scope",
				missionID, c.CurrentRound, c.Budget.Rounds,
			)
		}
		reflections, err := s.loadReflectionsLocked(missionID)
		if err != nil {
			return err
		}
		var current *Reflection
		for i := range reflections {
			if reflections[i].Round == c.CurrentRound {
				current = &reflections[i]
				break
			}
		}
		if current == nil {
			return fmt.Errorf(
				"mission %q has no reflection for round %d; submit one before advancing",
				missionID, c.CurrentRound,
			)
		}
		if IsTerminalRecommendation(current.Recommendation) {
			return fmt.Errorf(
				"mission %q round %d reflection recommends %q: %s",
				missionID, c.CurrentRound, current.Recommendation, current.Reason,
			)
		}
		// All gates passed; commit the bump.
		dest, err := s.contractPath(missionID)
		if err != nil {
			return err
		}
		c.CurrentRound++
		c.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := s.validateContract(c); err != nil {
			return fmt.Errorf("invalid contract after advance: %w", err)
		}
		if err := s.writeContract(c); err != nil {
			return err
		}
		if err := s.appendEventLocked(missionID, Event{
			TS:    c.UpdatedAt,
			Event: "round_advanced",
			Actor: actor,
			Details: map[string]any{
				"from_round":     c.CurrentRound - 1,
				"to_round":       c.CurrentRound,
				"recommendation": current.Recommendation,
			},
		}); err != nil {
			if rbErr := s.restoreContract(dest, oldData); rbErr != nil {
				return fmt.Errorf("advance: event append failed: %w; rollback failed: %v", err, rbErr)
			}
			return fmt.Errorf("advance: event append failed, contract rolled back: %w", err)
		}
		newRound = c.CurrentRound
		return nil
	})
	if err != nil {
		return 0, err
	}
	return newRound, nil
}

// checkSelfVerification refuses a contract that names the same handle
// as both worker and evaluator. This is Phase 3.5's weakest role
// invariant — a pure field comparison, no lookups — and runs before
// any lock is taken so the caller sees a fast failure.
//
// The caller can always rename one side; the error names both slots
// so the fix is obvious.
func checkSelfVerification(c *Contract) error {
	worker := strings.TrimSpace(c.Worker)
	evaluator := strings.TrimSpace(c.Evaluator.Handle)
	if worker == "" || evaluator == "" {
		// Empty fields are a Validate concern, not a role concern.
		return nil
	}
	if worker == evaluator {
		return fmt.Errorf(
			"mission %q: worker %q cannot also be evaluator; assign a distinct reviewer (the verifier must not review its own work)",
			c.MissionID, worker,
		)
	}
	return nil
}

// checkRoleOverlap refuses a contract whose worker and evaluator share
// a team-scoped role binding OR a role slug after canonicalization.
//
// The invariant is Phase 3.5's load-bearing distinction: roles are
// interfaces, and two identities bound to the same role on the same
// team have identical responsibilities — they cannot verify each
// other's work any more meaningfully than one identity can verify
// itself.
//
// Canonicalization rules (see canonicalRoleSlug):
//   - `engineering/go-specialist` and `engineering/go-specialist`
//     overlap (same team-scoped binding).
//   - `engineering/go-specialist` and `security/go-specialist`
//     overlap (same role slug regardless of team).
//   - `engineering/go-specialist` and `engineering/security-reviewer`
//     do NOT overlap (same team, different role).
//   - Identity on no teams → no overlap. An empty binding set is a
//     pass; the check is for ACTIVE role coincidence, not for
//     missing metadata.
//
// Errors name both handles and every overlapping binding so the
// operator can edit the team membership or rename one side.
func checkRoleOverlap(roles RoleLister, c *Contract) error {
	if roles == nil {
		return nil
	}
	worker := strings.TrimSpace(c.Worker)
	evaluator := strings.TrimSpace(c.Evaluator.Handle)
	if worker == "" || evaluator == "" {
		return nil
	}
	// checkSelfVerification already caught worker == evaluator; this
	// helper is only ever called after that gate.

	workerRoles, err := roles.ListRoles(worker)
	if err != nil {
		return fmt.Errorf("role overlap check: listing roles for worker %q: %w", worker, err)
	}
	evaluatorRoles, err := roles.ListRoles(evaluator)
	if err != nil {
		return fmt.Errorf("role overlap check: listing roles for evaluator %q: %w", evaluator, err)
	}
	if len(workerRoles) == 0 || len(evaluatorRoles) == 0 {
		return nil
	}

	// Build the worker's full binding set and its canonical role-slug
	// set. Two passes over the evaluator's roles check both overlap
	// flavors and collect every offending binding so the error lists
	// them all, not just the first one found.
	workerBindings := make(map[string]struct{}, len(workerRoles))
	workerSlugs := make(map[string]string, len(workerRoles))
	for _, r := range workerRoles {
		workerBindings[r.Name] = struct{}{}
		slug := canonicalRoleSlug(r.Name)
		if slug != "" {
			workerSlugs[slug] = r.Name
		}
	}

	type overlap struct {
		workerBinding    string
		evaluatorBinding string
	}
	var overlaps []overlap
	for _, r := range evaluatorRoles {
		if _, ok := workerBindings[r.Name]; ok {
			// Same team/role exactly: the stronger of the two
			// collision flavors. Record with the worker and
			// evaluator both naming the same binding.
			overlaps = append(overlaps, overlap{workerBinding: r.Name, evaluatorBinding: r.Name})
			continue
		}
		slug := canonicalRoleSlug(r.Name)
		if slug == "" {
			continue
		}
		if workerBinding, ok := workerSlugs[slug]; ok {
			overlaps = append(overlaps, overlap{workerBinding: workerBinding, evaluatorBinding: r.Name})
		}
	}
	if len(overlaps) == 0 {
		return nil
	}
	sort.Slice(overlaps, func(i, j int) bool {
		if overlaps[i].workerBinding != overlaps[j].workerBinding {
			return overlaps[i].workerBinding < overlaps[j].workerBinding
		}
		return overlaps[i].evaluatorBinding < overlaps[j].evaluatorBinding
	})
	var lines []string
	// Singular/plural split: "1 overlapping role assignment" vs
	// "N overlapping role assignments". The bare "(s)" reads awkwardly
	// in operator output — render the correct word for the count.
	noun := "assignments"
	if len(overlaps) == 1 {
		noun = "assignment"
	}
	lines = append(lines, fmt.Sprintf(
		"mission %q: worker %q and evaluator %q share %d overlapping role %s; the verifier must not share a role with the worker",
		c.MissionID, worker, evaluator, len(overlaps), noun,
	))
	for _, o := range overlaps {
		if o.workerBinding == o.evaluatorBinding {
			lines = append(lines, fmt.Sprintf(
				"  both bound to %q (same team, same role)",
				o.workerBinding,
			))
		} else {
			lines = append(lines, fmt.Sprintf(
				"  worker bound to %q, evaluator bound to %q (same role slug after canonicalization)",
				o.workerBinding, o.evaluatorBinding,
			))
		}
	}
	lines = append(lines, "  recovery: assign the evaluator to a distinct role, or name a different evaluator")
	return errors.New(strings.Join(lines, "\n"))
}

// canonicalRoleSlug extracts the role slug from a RoleLister binding
// name of the form "team/role". The team prefix is stripped so two
// identities bound to the same role on different teams still compare
// equal. A name with no slash (legacy or hand-built) is returned
// as-is. An empty name returns "".
//
// Stripping uses the LAST slash so future multi-level team paths
// (e.g. `engineering/subgroup/go-specialist`) still yield the right
// slug. The existing liveRoleLister emits `team/role`, single-slash;
// this helper accepts both shapes.
func canonicalRoleSlug(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// checkWriteSetConflicts loads every existing mission, filters to
// open ones, and asks findWriteSetConflicts whether the new contract's
// write_set overlaps any of them. Returns a non-nil error iff there
// is at least one conflict.
//
// The caller must hold the directory-level create lock so that the
// scan-then-write transition is atomic with respect to other Creates.
//
// A mission that fails to load is skipped with a stderr warning.
// Unloadable missions cannot conflict — the safe default is skip,
// not block all future creates.
func (s *Store) checkWriteSetConflicts(c *Contract) error {
	ids, err := s.List()
	if err != nil {
		return fmt.Errorf("create: listing existing missions: %w", err)
	}
	var openContracts []*Contract
	for _, id := range ids {
		// Skip self defensively. The Create caller has already
		// verified the destination file does not exist, so this
		// should never trigger pre-create — but if Create is ever
		// reused for a re-validation path the self-skip prevents a
		// false positive.
		if id == c.MissionID {
			continue
		}
		existing, err := s.Load(id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ethos: warning: skipping mission %q during conflict check: %v\n", id, err)
			continue
		}
		if existing.Status == StatusOpen {
			// Skip a mission whose write_set claim was explicitly
			// released (ForceReleaseWriteSet, ethos-9x07) -- the fields
			// are still on disk for validation and audit history, but
			// they no longer represent an active admission-control
			// claim.
			if existing.WriteSetReleasedAt != "" {
				continue
			}
			// Skip missions in the same pipeline. Pipeline stages are
			// expected to execute sequentially under the pipeline runner
			// or leader's orchestration; write_set overlap within a pipeline
			// is expected (one stage's writes are the next stage's inputs).
			// depends_on is advisory — it documents intent but does not
			// block Create. Cross-pipeline overlaps are still rejected.
			if c.Pipeline != "" && existing.Pipeline == c.Pipeline {
				continue
			}
			openContracts = append(openContracts, existing)
		}
	}
	conflicts := findWriteSetConflicts(c.WriteSet, c.ExtractInto, openContracts)
	if len(conflicts) == 0 {
		return nil
	}
	return formatConflictError(conflicts)
}

// --- 3.6: result artifacts and close gate ---

// resultsFile is the on-disk schema for the sibling .results.yaml
// file. The single Round-keyed sequence mirrors reflectionsFile so
// the two sibling layouts stay symmetric: one file per mission,
// round-sorted slice, append-only discipline enforced at write time.
//
// Two ordering invariants hold for the on-disk slice (and the helper
// methods enforce them on every write):
//
//  1. Results are sorted by Round ascending.
//  2. Each Round value appears at most once — AppendResult refuses
//     to add a duplicate, so the slice is dense.
type resultsFile struct {
	Results []Result `yaml:"results"`
}

// LoadResults returns every result recorded for a mission, in round
// order. Missing file → empty slice; the absence of any result is
// the normal state for a freshly created mission.
//
// Decodes with KnownFields(true) so a hand-edited results file
// cannot smuggle extra keys past the trust boundary, symmetric with
// the contract and reflection decode paths.
func (s *Store) LoadResults(missionID string) ([]Result, error) {
	if strings.TrimSpace(missionID) == "" {
		return nil, fmt.Errorf("missionID is required")
	}
	path, err := s.resultsPath(missionID)
	if err != nil {
		return nil, err
	}
	if err := rejectSymlink(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading results for %q: %w", missionID, err)
	}
	return decodeResultsFile(data, missionID)
}

// LoadResult returns the result for a specific round of a mission,
// or (nil, nil) if no result has been submitted for that round. The
// Phase 3.6 close gate uses this method to decide whether a terminal
// transition is permitted.
//
// A nil return with nil error means "no result on file for this
// round"; the caller interprets that as "gate refuses". Any other
// error — decode failure, I/O failure — is propagated so a corrupt
// results file produces a loud diagnostic rather than a silent gate
// bypass.
func (s *Store) LoadResult(missionID string, round int) (*Result, error) {
	results, err := s.LoadResults(missionID)
	if err != nil {
		return nil, err
	}
	for i := range results {
		if results[i].Round == round {
			return &results[i], nil
		}
	}
	return nil, nil
}

// decodeResultsFile parses a results.yaml body, runs Validate on
// every entry, and asserts the round-monotone invariant. Returns
// the decoded slice (nil if the file is empty/blank).
func decodeResultsFile(data []byte, missionID string) ([]Result, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var wrapper resultsFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&wrapper); err != nil {
		return nil, fmt.Errorf("invalid results file %q: %w", missionID, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("invalid results file %q: multiple YAML documents are not allowed", missionID)
		}
		return nil, fmt.Errorf("invalid results file %q: trailing content: %w", missionID, err)
	}
	for i := range wrapper.Results {
		r := &wrapper.Results[i]
		if err := r.Validate(); err != nil {
			return nil, fmt.Errorf("results[%d] for %q: %w", i, missionID, err)
		}
		// On-disk trust symmetry with AppendResult: the write path
		// refuses a result whose self-declared Mission does not match
		// the target mission, but until Phase 3.6 round 5 the read
		// path did not. An attacker with local write access could
		// hand-edit <mission-A>.results.yaml to contain a result
		// claiming mission-B, and the close gate would accept it as
		// long as the round number matched. Reject the mismatch here
		// so the two surfaces enforce the same invariant.
		if r.Mission != missionID {
			return nil, fmt.Errorf(
				"results[%d].mission: expected %q, got %q",
				i, missionID, r.Mission,
			)
		}
		if i > 0 && wrapper.Results[i-1].Round >= r.Round {
			return nil, fmt.Errorf(
				"results file %q is out of order or has duplicate round: %d after %d",
				missionID, r.Round, wrapper.Results[i-1].Round,
			)
		}
	}
	return wrapper.Results, nil
}

// AppendResult records a worker result for a mission's current round.
// The append is append-only by construction: a duplicate Round is
// refused, and the file is rewritten via temp+rename so a partial
// write cannot leave a half-encoded YAML doc on disk.
//
// The caller-provided Result.Round must equal Contract.CurrentRound
// (the round the worker is currently in). Submitting a result for
// any other round is a programming error and is refused — the close
// gate would otherwise have to chase out-of-order results at close
// time, where the operator-facing error is much further from the bug.
//
// Result.Mission must match the caller-supplied missionID; the
// cross-check exists so a file cut loose from its mission cannot
// slip past the gate by claiming the wrong parent.
//
// files_changed paths are cross-checked against the contract's
// write_set using pathContainedBy (asymmetric segment-prefix). A
// path outside the allowlist is a fatal error; the error names every
// offending path so the operator sees the full picture in one pass.
// Phase 3.2's pathsOverlap is deliberately NOT used here — the two
// primitives answer different questions, and symmetric overlap would
// accept a parent-prefix of a file entry.
//
// Validate runs before any disk I/O. CreatedAt is set to now if the
// caller left it blank. The result event is appended to the JSONL
// log inside the per-mission flock so concurrent submit/close
// attempts serialize cleanly.
//
// Atomic from the caller's view: a write failure leaves the on-disk
// results file unchanged.
func (s *Store) AppendResult(missionID string, r *Result) error {
	if r == nil {
		return fmt.Errorf("result is nil")
	}
	staged := *r
	// Normalize Author before persisting so whitespace around the
	// handle does not pollute the audit trail or the event log. The
	// Validate call only checks that the trimmed form is non-empty;
	// it does not reject surrounding whitespace, which would break
	// backwards compatibility with round 1 files that stored an
	// untrimmed author. Normalizing in Append is purely additive.
	staged.Author = strings.TrimSpace(staged.Author)
	if strings.TrimSpace(staged.CreatedAt) == "" {
		staged.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if err := staged.Validate(); err != nil {
		return fmt.Errorf("invalid result: %w", err)
	}
	return s.withLock(missionID, func() error {
		c, _, err := s.loadLocked(missionID)
		if err != nil {
			return err
		}
		// Refuse to record a result on a closed mission. A result is
		// the worker's handoff for a live round; a closed mission has
		// no live round to hand off from.
		if c.Status != StatusOpen {
			return fmt.Errorf("mission %q is in terminal state %q; results are accepted only on open missions", missionID, c.Status)
		}
		// Mission ID cross-check: the result's self-declared Mission
		// must match the caller's target. A file renamed between
		// missions is the kind of silent data corruption that would
		// otherwise slip past every other check.
		if staged.Mission != missionID {
			return fmt.Errorf(
				"result mission %q does not match target mission %q",
				staged.Mission, missionID,
			)
		}
		if staged.Round != c.CurrentRound {
			return fmt.Errorf(
				"result round %d does not match mission %q current round %d",
				staged.Round, missionID, c.CurrentRound,
			)
		}
		// files_changed containment: every declared path must live
		// inside at least one entry of the contract's write_set.
		// This is the third use of the write_set cross-check pattern
		// (Phase 3.2 admission, Phase 3.5 verifier allowlist, now
		// Phase 3.6 result containment). Uses pathContainedBy, the
		// asymmetric segment-prefix helper — a parent-prefix of a
		// write_set file entry must NOT be admitted.
		if err := checkFilesChangedContainment(c, &staged); err != nil {
			return err
		}
		existing, err := s.loadResultsLocked(missionID)
		if err != nil {
			return err
		}
		for _, e := range existing {
			if e.Round == staged.Round {
				return fmt.Errorf(
					"result for round %d of mission %q already exists; results are append-only",
					staged.Round, missionID,
				)
			}
		}
		updated := append(existing, staged)
		if err := s.writeResultsLocked(missionID, updated); err != nil {
			return err
		}
		if err := s.appendEventLocked(missionID, Event{
			TS:    staged.CreatedAt,
			Event: "result",
			Actor: staged.Author,
			Details: map[string]any{
				"round":            staged.Round,
				"verdict":          staged.Verdict,
				"confidence":       staged.Confidence,
				"files_changed":    len(staged.FilesChanged),
				"evidence_entries": len(staged.Evidence),
			},
		}); err != nil {
			// Roll back the results file so the mission's on-disk
			// state matches the operation's failure: if the event log
			// rejects the result record, the result itself must not
			// be observable to a later read or to the close gate.
			//
			// If the file did not exist before the append (existing
			// was nil), remove it entirely rather than writing an
			// empty results: [] stub.
			if existing == nil {
				rbPath, pErr := s.resultsPath(missionID)
				if pErr != nil {
					return fmt.Errorf("result: event append failed: %w; rollback path failed: %v", err, pErr)
				}
				if rbErr := os.Remove(rbPath); rbErr != nil && !os.IsNotExist(rbErr) {
					return fmt.Errorf("result: event append failed: %w; rollback remove failed: %v", err, rbErr)
				}
			} else if rbErr := s.writeResultsLocked(missionID, existing); rbErr != nil {
				return fmt.Errorf("result: event append failed: %w; rollback failed: %v", err, rbErr)
			}
			return fmt.Errorf("result: event append failed, result rolled back: %w", err)
		}
		// Reflect CreatedAt back to the caller — the one field
		// AppendResult is contracted to set. Every other field came
		// from the caller and is preserved as-is.
		r.CreatedAt = staged.CreatedAt
		return nil
	})
}

// loadResultsLocked is the lock-respecting twin of LoadResults. The
// caller must already hold the per-mission flock; AppendResult and
// checkResultGateLocked use it to read the existing slice without
// re-acquiring the lock and deadlocking.
func (s *Store) loadResultsLocked(missionID string) ([]Result, error) {
	path, err := s.resultsPath(missionID)
	if err != nil {
		return nil, err
	}
	if err := rejectSymlink(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading results for %q: %w", missionID, err)
	}
	return decodeResultsFile(data, missionID)
}

// writeResultsLocked rewrites the results file via temp+rename.
// Caller must hold the per-mission flock. The file is wrapped in a
// resultsFile struct so future top-level metadata (e.g. a
// schema_version key) can be added without breaking decode of older
// files.
func (s *Store) writeResultsLocked(missionID string, rs []Result) error {
	wrapper := resultsFile{Results: rs}
	data, err := yaml.Marshal(&wrapper)
	if err != nil {
		return fmt.Errorf("marshaling results: %w", err)
	}
	if err := s.ensureMissionDir(missionID); err != nil {
		return err
	}
	dest, err := s.resultsPath(missionID)
	if err != nil {
		return err
	}
	tmp := dest + ".tmp"
	// Uniform symlink policy (paths.go): refuse symlinks at dest and
	// tmp before WriteFile would follow them.
	if err := rejectSymlink(dest); err != nil {
		return err
	}
	if err := rejectSymlink(tmp); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing temp results: %w", err)
	}
	return os.Rename(tmp, dest)
}

// checkResultGateLocked is the Phase 3.6 close gate. It runs inside
// Close's per-mission flock and refuses the terminal transition
// unless a valid result artifact exists for the mission's current
// round.
//
// On success it returns the satisfying result so the caller can
// record the round number and verdict on the close event's
// Details map. This was added in round 2 so the audit trail
// directly links the close transition to the result that authorized
// it, instead of forcing an auditor to scan back across
// round_advanced events.
//
// The refusal message names the mission, the missing round, and
// the submission command so the operator sees the recovery path in
// the error itself, not in separate documentation.
func (s *Store) checkResultGateLocked(c *Contract) (*Result, error) {
	results, err := s.loadResultsLocked(c.MissionID)
	if err != nil {
		return nil, fmt.Errorf("close: loading results for gate: %w", err)
	}
	for i := range results {
		if results[i].Round == c.CurrentRound {
			// Return a pointer into a local copy so the caller
			// cannot mutate the on-disk cache by accident.
			r := results[i]
			return &r, nil
		}
	}
	return nil, fmt.Errorf(
		"mission %q cannot close: no result artifact for round %d; run `ethos mission result %s --file <path>` to submit one",
		c.MissionID, c.CurrentRound, c.MissionID,
	)
}

// checkFilesChangedContainment verifies every FilesChanged entry in
// r lives under at least one entry of c.WriteSet. Uses
// pathContainedBy (asymmetric) so a result cannot quietly claim
// authority over a parent directory of a write_set entry.
//
// Phase 3.6 round 1 used the symmetric pathsOverlap helper; all four
// reviewers flagged the bug independently. A contract declaring
// `cmd/ethos/serve.go` with a result claiming `cmd` overlaps in one
// direction only — the file `cmd` has fewer segments than the entry
// `cmd/ethos/serve.go` — and the symmetric check accepted it. The
// asymmetric check correctly refuses: the entry's segment list must
// be a prefix of the file's segment list.
//
// The helper collects every out-of-bounds path before returning, so
// the operator sees the complete fix list in a single error rather
// than one path per retry. Empty FilesChanged is allowed — a round
// that only inspected code without writing is a legitimate outcome.
func checkFilesChangedContainment(c *Contract, r *Result) error {
	if len(r.FilesChanged) == 0 {
		return nil
	}
	var outOfBounds []string
	for _, fc := range r.FilesChanged {
		contained := false
		for _, entry := range c.WriteSet {
			if pathContainedBy(fc.Path, entry) {
				contained = true
				break
			}
		}
		if !contained {
			outOfBounds = append(outOfBounds, fc.Path)
		}
	}
	if len(outOfBounds) == 0 {
		return nil
	}
	return fmt.Errorf(
		"result files_changed contains %d path(s) outside mission %q write_set: %s",
		len(outOfBounds), c.MissionID, strings.Join(outOfBounds, ", "),
	)
}
