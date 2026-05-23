package hook

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/internal/mission"
	"github.com/punt-labs/ethos/internal/resolve"
)

// tryTierBByInheritance attempts to inherit a parent contract for an
// Agent spawn that has PARENT_DELEGATION_ID set but no MISSION_ID.
// DES-054 v5 §"PreToolUse-on-Agent" dispatch rule (a): if any
// ancestor's contract carries a Delegations[] entry whose
// SpawnPattern matches childAgentType AND InheritsContract is true,
// the child runs as Tier B under that ancestor's missionID.
//
// Returns (missionID, true) on a hit and ("", false) otherwise. The
// resolver is NON-BLOCKING by design: every error path (parent
// record not on disk, malformed regex, walk overflow, contract
// load failure) writes a stderr warning and returns false so the
// caller falls through to Tier A. djb's "no silent admit under a
// malformed env" rule is satisfied because the warning is operator-
// visible and the spawn proceeds without an inherited contract.
//
// repoRoot is the enclosing repo (tierBRepoRoot). parentDelegation
// is the value of PARENT_DELEGATION_ID. childAgentType is the
// CLAUDE_AGENT_TYPE for the spawn being dispatched.
func tryTierBByInheritance(repoRoot, parentDelegation, childAgentType string) (string, bool) {
	if parentDelegation == "" || childAgentType == "" {
		return "", false
	}

	parent, missionID, ok := loadParentDelegation(repoRoot, parentDelegation)
	if !ok {
		return "", false
	}

	limit, err := resolve.ResolveMaxDelegationDepth(repoRoot, mission.MaxDelegationDepthDefault)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos: pre-tool-use: inheritance: resolving max_delegation_depth: %v; falling through to Tier A\n",
			err)
		return "", false
	}

	store, err := tierBMissionStore()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos: pre-tool-use: inheritance: resolving mission store: %v; falling through to Tier A\n",
			err)
		return "", false
	}

	return walkInheritanceChain(repoRoot, store, parent, missionID, childAgentType, limit)
}

// walkInheritanceChain walks the parent_delegation chain starting at
// parent (already loaded; missionID known) and returns the first
// ancestor missionID whose contract has a Delegations[] entry that
// matches childAgentType with InheritsContract=true. The walk is
// bounded by limit; an overflow surfaces as a stderr warning and a
// Tier A fall-through.
//
// Ancestor records are loaded from <repo>/.ethos/missions/<m>/
// delegations/<d>/record.yaml. The walker keeps a `seen` set as a
// cheap cycle guard so a corrupt record that points back at itself
// does not spin (the depth limit would catch it eventually, but a
// cycle is a corruption and should not be silently absorbed).
func walkInheritanceChain(
	repoRoot string,
	store *mission.Store,
	parent *mission.Delegation,
	missionID, childAgentType string,
	limit int,
) (string, bool) {
	cur := parent
	curMission := missionID
	depth := 0
	seen := map[string]struct{}{cur.ID: {}}

	for cur != nil {
		if depth >= limit {
			fmt.Fprintf(os.Stderr,
				"ethos: pre-tool-use: inheritance: chain exceeds max_delegation_depth %d at %q; falling through to Tier A\n",
				limit, cur.ID)
			return "", false
		}
		if matched, mID, ok := matchAncestorContract(store, cur, curMission, childAgentType); ok {
			return mID, matched
		}
		if cur.ParentDelegation == "" {
			return "", false
		}
		if _, dup := seen[cur.ParentDelegation]; dup {
			fmt.Fprintf(os.Stderr,
				"ethos: pre-tool-use: inheritance: cycle at %q; falling through to Tier A\n",
				cur.ParentDelegation)
			return "", false
		}
		seen[cur.ParentDelegation] = struct{}{}
		nextParent, nextMission, ok := loadParentDelegation(repoRoot, cur.ParentDelegation)
		if !ok {
			return "", false
		}
		cur = nextParent
		curMission = nextMission
		depth++
	}
	return "", false
}

// matchAncestorContract loads ancestor's contract by missionID and
// asks MatchSpawnPattern for each Delegations[] entry. The first
// entry whose pattern matches childAgentType AND has
// InheritsContract=true wins. Returns (true, missionID, true) on a
// hit, (false, "", false) on no match, and (false, "", false) on a
// non-fatal error (with a stderr warning).
func matchAncestorContract(
	store *mission.Store,
	ancestor *mission.Delegation,
	missionID, childAgentType string,
) (bool, string, bool) {
	if missionID == "" {
		return false, "", false
	}
	c, err := store.Load(missionID)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos: pre-tool-use: inheritance: loading contract %q for ancestor %q: %v; falling through to Tier A\n",
			missionID, ancestor.ID, err)
		return false, "", false
	}
	for i := range c.Delegations {
		entry := &c.Delegations[i]
		if !entry.InheritsContract {
			continue
		}
		matched, err := mission.MatchSpawnPattern(entry.SpawnPattern, childAgentType)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"ethos: pre-tool-use: inheritance: bad spawn_pattern %q in contract %q: %v; falling through to Tier A\n",
				entry.SpawnPattern, missionID, err)
			return false, "", false
		}
		if matched {
			return true, missionID, true
		}
	}
	return false, "", false
}

// loadParentDelegation finds and loads a delegation record by ID.
// PARENT_DELEGATION_ID alone does not encode which mission the
// parent belongs to, so the resolver scans
// <repo>/.ethos/missions/*/delegations/<id>/record.yaml for the
// matching record. Returns (record, missionID, true) on a hit,
// (nil, "", false) otherwise (with a stderr warning on any I/O
// error past the initial "not found").
func loadParentDelegation(repoRoot, delegationID string) (*mission.Delegation, string, bool) {
	missionsDir := filepath.Join(repoRoot, ".ethos", "missions")
	entries, err := os.ReadDir(missionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", false
		}
		fmt.Fprintf(os.Stderr,
			"ethos: pre-tool-use: inheritance: reading missions dir %q: %v; falling through to Tier A\n",
			missionsDir, err)
		return nil, "", false
	}
	target := filepath.Base(delegationID)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		recordPath := filepath.Join(
			missionsDir, e.Name(), "delegations", target, "record.yaml",
		)
		if _, statErr := os.Stat(recordPath); statErr != nil {
			// fs.ErrNotExist is the expected "this isn't the right
			// mission tree" case — silent skip. Anything else
			// (EACCES, EIO) is a real fault the operator should see;
			// surface it but still fall through so inheritance
			// remains non-blocking (Copilot on PR #328).
			if !errors.Is(statErr, fs.ErrNotExist) {
				fmt.Fprintf(os.Stderr,
					"ethos: pre-tool-use: inheritance: stat %s: %v\n",
					recordPath, statErr)
			}
			continue
		}
		d, err := mission.LoadDelegation(recordPath)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"ethos: pre-tool-use: inheritance: loading parent delegation %q: %v; falling through to Tier A\n",
				recordPath, err)
			return nil, "", false
		}
		return d, e.Name(), true
	}
	fmt.Fprintf(os.Stderr,
		"ethos: pre-tool-use: inheritance: parent delegation %q not found under %q; falling through to Tier A\n",
		delegationID, missionsDir)
	return nil, "", false
}

// pretooluseInheritReader is the package-private interface
// dispatchAgent uses to consult the inheritance resolver. Kept as a
// var so tests can stub it without rewriting environment lookups.
var pretooluseInheritReader = tryTierBByInheritance

// dispatchTierBOrTierA is the routing decision when MISSION_ID is
// unset. If PARENT_DELEGATION_ID is present the resolver tries
// inheritance; on a hit the spawn becomes Tier B with the inherited
// missionID, otherwise it falls through to Tier A.
func dispatchTierBOrTierA(w io.Writer, sessionID string) error {
	parentDelegation := os.Getenv("PARENT_DELEGATION_ID")
	if parentDelegation == "" {
		return dispatchTierA(w, sessionID)
	}
	childAgentType := os.Getenv("CLAUDE_AGENT_TYPE")
	repoRoot := tierBRepoRoot()
	if missionID, ok := pretooluseInheritReader(repoRoot, parentDelegation, childAgentType); ok {
		return dispatchTierB(w, sessionID, missionID)
	}
	return dispatchTierA(w, sessionID)
}
