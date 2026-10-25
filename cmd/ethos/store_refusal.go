package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/spf13/cobra"
)

// storeUse says how a command depends on the repository store, and so
// whether it runs when the store cannot be located.
type storeUse int

const (
	// storeRefuses: the command reads or writes the repository store (an
	// identity, persona or author resolves through it too), so it refuses
	// rather than fall back to the global store. The zero value: a command
	// missing from storeUses refuses.
	storeRefuses storeUse = iota
	// storeGlobalOnly: the command touches only global stores under
	// ~/.punt-labs/ethos (sessions, adrs), or no store at all, so an
	// unlocated repository store cannot misdirect it.
	storeGlobalOnly
	// storeHandlesError: the command reports on or repairs the store
	// (doctor, enable, disable, audit), or locates it and refuses for itself
	// (setup, seed, serve, every hook).
	storeHandlesError
)

// storeUses classifies every command by path. A test walks the command tree
// and requires each command here, and each entry to be a command, so a new
// command cannot run unclassified.
//
// The session commands below are global-only: they read and write rosters and
// pointers in ~/.punt-labs/ethos/sessions and resolve the session from the
// environment, never an identity (session join and session leave included —
// runSessionJoin and runSessionLeave use only sessionStore and
// resolveSessionContext). iam and session iam (runIam) likewise only join the
// persona to the roster: the persona is recorded as given, never looked up
// or validated against the identity store. session start refuses: its
// --persona and its default root resolve through the identity store. session purge checks the checkout's audit files
// (FindRepoRoot), not the store. session write-current is doctor's own
// remedy for a broken session pointer. adr list, show and settle use only
// ~/.punt-labs/ethos/adrs; adr create resolves its default author through the
// identity store, so it refuses. The MCP session and adr tools stay
// unguarded: adr create there takes its author from the caller.
var storeUses = map[string]storeUse{
	// Command groups print help.
	"ethos": storeGlobalOnly, "ethos adr": storeGlobalOnly, "ethos audit": storeGlobalOnly,
	"ethos ext": storeGlobalOnly, "ethos find": storeGlobalOnly, "ethos mission": storeGlobalOnly,
	"ethos mission pipeline": storeGlobalOnly, "ethos personality": storeGlobalOnly,
	"ethos role": storeGlobalOnly, "ethos talent": storeGlobalOnly, "ethos team": storeGlobalOnly,
	"ethos writing-style": storeGlobalOnly,

	"ethos version": storeGlobalOnly, "ethos completion": storeGlobalOnly,
	"ethos uninstall": storeGlobalOnly, "ethos identity schema": storeGlobalOnly,
	"ethos role schema": storeGlobalOnly, "ethos team schema": storeGlobalOnly,

	"ethos session": storeGlobalOnly, "ethos session show": storeGlobalOnly,
	"ethos session roster": storeGlobalOnly, "ethos session list": storeGlobalOnly,
	"ethos session write-current": storeGlobalOnly, "ethos session purge": storeGlobalOnly,
	"ethos session end": storeGlobalOnly, "ethos session join": storeGlobalOnly,
	"ethos session leave": storeGlobalOnly, "ethos session create": storeGlobalOnly,
	"ethos session delete": storeGlobalOnly, "ethos session delete-current": storeGlobalOnly,
	"ethos iam": storeGlobalOnly, "ethos session iam": storeGlobalOnly,
	"ethos adr list": storeGlobalOnly, "ethos adr show": storeGlobalOnly,
	"ethos adr settle": storeGlobalOnly,

	"ethos doctor": storeHandlesError, "ethos enable": storeHandlesError,
	"ethos disable": storeHandlesError, "ethos setup": storeHandlesError,
	"ethos seed": storeHandlesError, "ethos serve": storeHandlesError,
	"ethos audit migrate": storeHandlesError, "ethos audit quarantine": storeHandlesError,
	"ethos audit seal": storeHandlesError, "ethos audit show": storeHandlesError,
	"ethos hook": storeHandlesError, "ethos hook audit-log": storeHandlesError,
	"ethos hook commit-trailers": storeHandlesError, "ethos hook format-output": storeHandlesError,
	"ethos hook pre-compact": storeHandlesError, "ethos hook pre-tool-use": storeHandlesError,
	"ethos hook session-end": storeHandlesError, "ethos hook session-start": storeHandlesError,
	"ethos hook subagent-start": storeHandlesError, "ethos hook subagent-stop": storeHandlesError,

	"ethos adr create": storeRefuses, "ethos create": storeRefuses, "ethos export": storeRefuses,
	"ethos ext del": storeRefuses, "ethos ext delete": storeRefuses, "ethos ext get": storeRefuses,
	"ethos ext list": storeRefuses, "ethos ext set": storeRefuses, "ethos find missions": storeRefuses,
	"ethos identity": storeRefuses, "ethos identity create": storeRefuses,
	"ethos identity get": storeRefuses, "ethos identity list": storeRefuses,
	"ethos identity show": storeRefuses, "ethos identity whoami": storeRefuses,
	"ethos import": storeRefuses, "ethos list": storeRefuses,
	"ethos mission abandon": storeRefuses, "ethos mission advance": storeRefuses,
	"ethos mission claim": storeRefuses, "ethos mission close": storeRefuses,
	"ethos mission correct": storeRefuses, "ethos mission create": storeRefuses,
	"ethos mission dispatch": storeRefuses, "ethos mission export": storeRefuses,
	"ethos mission lint": storeRefuses, "ethos mission list": storeRefuses,
	"ethos mission log": storeRefuses, "ethos mission migrate": storeRefuses,
	"ethos mission pipeline instantiate": storeRefuses, "ethos mission pipeline list": storeRefuses,
	"ethos mission pipeline show": storeRefuses, "ethos mission reflect": storeRefuses,
	"ethos mission reflections": storeRefuses, "ethos mission release": storeRefuses,
	"ethos mission result": storeRefuses, "ethos mission results": storeRefuses,
	"ethos mission scaffold": storeRefuses, "ethos mission scaffold contract": storeRefuses,
	"ethos mission scaffold result": storeRefuses, "ethos mission show": storeRefuses,
	"ethos personality create": storeRefuses, "ethos personality delete": storeRefuses,
	"ethos personality list": storeRefuses, "ethos personality set": storeRefuses,
	"ethos personality show": storeRefuses, "ethos resolve-agent": storeRefuses,
	"ethos role create": storeRefuses, "ethos role delete": storeRefuses,
	"ethos role list": storeRefuses, "ethos role show": storeRefuses,
	"ethos session start": storeRefuses,
	"ethos show":          storeRefuses, "ethos talent add": storeRefuses, "ethos talent create": storeRefuses,
	"ethos talent delete": storeRefuses, "ethos talent list": storeRefuses,
	"ethos talent remove": storeRefuses, "ethos talent show": storeRefuses,
	"ethos team activate": storeRefuses, "ethos team active": storeRefuses,
	"ethos team add-bundle": storeRefuses, "ethos team add-collab": storeRefuses,
	"ethos team add-member": storeRefuses, "ethos team available": storeRefuses,
	"ethos team create": storeRefuses, "ethos team deactivate": storeRefuses,
	"ethos team delete": storeRefuses, "ethos team for-repo": storeRefuses,
	"ethos team list": storeRefuses, "ethos team remove-member": storeRefuses,
	"ethos team show": storeRefuses, "ethos ui": storeRefuses, "ethos vendor": storeRefuses,
	"ethos whoami": storeRefuses, "ethos writing-style create": storeRefuses,
	"ethos writing-style delete": storeRefuses, "ethos writing-style list": storeRefuses,
	"ethos writing-style set": storeRefuses, "ethos writing-style show": storeRefuses,
}

// cobraCommands are the commands cobra adds only when the root executes, so
// the command tree a test walks does not hold them. None touches a store.
var cobraCommands = []string{"ethos help", "ethos __complete", "ethos __completeNoDesc"}

func exemptFromStoreRefusal(cmd *cobra.Command) bool {
	path := cmd.CommandPath()
	if slices.Contains(cobraCommands, path) {
		return true
	}
	return storeUses[path] != storeRefuses
}

// refuseUnlocatableStore fails a command whose repository store cannot be
// located for any reason but "not in a repository" — an unreadable .git, a
// refused ETHOS_REPO_ROOT. Falling back to the global store there would write
// to the wrong tree and exit 0.
func refuseUnlocatableStore(cmd *cobra.Command, _ []string) error {
	if exemptFromStoreRefusal(cmd) {
		return nil
	}
	if _, err := resolve.LocateStoreRoot(); err != nil && !errors.Is(err, resolve.ErrNotInRepo) {
		name := strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
		return fmt.Errorf("%s: %w; refusing to use the global store instead", name, err)
	}
	return nil
}
