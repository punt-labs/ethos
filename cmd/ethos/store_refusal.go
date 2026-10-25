package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/spf13/cobra"
)

// storeRefusalExemptTrees are the command trees, every subcommand included,
// that run when the repository store cannot be located: they report on or
// repair the store (doctor, enable, disable, audit), do not use it (version,
// completion, help, uninstall), locate and refuse for themselves (setup, seed,
// serve, hook), or are cobra's own completion entry points.
var storeRefusalExemptTrees = []string{
	"ethos version", "ethos doctor", "ethos completion", "ethos help",
	"ethos hook", "ethos serve", "ethos enable", "ethos disable",
	"ethos uninstall", "ethos setup", "ethos seed", "ethos audit",
	"ethos __complete", "ethos __completeNoDesc",
}

// storeRefusalExemptCommands are the single commands that touch only global
// stores, so an unlocated repository store cannot misdirect them:
//
//   - session, session show, session roster, session list: read rosters in
//     ~/.punt-labs/ethos/sessions.
//   - session write-current: writes the pointer in the same directory; it is
//     doctor's own remedy for a broken session pointer.
//   - session purge: removes rosters and pointers there; the audit files it
//     checks are the checkout's (FindRepoRoot), not the store's.
//   - adr list, adr show: read ~/.punt-labs/ethos/adrs.
//
// whoami, identity whoami, iam, session iam, session start, session join and
// adr create are not here: each resolves an identity (or a persona, or an
// ADR author) through the repository identity store, so each refuses like
// identity get. The MCP session and adr tools stay unguarded: neither reads
// the identity store (adr create there takes its author from the caller).
var storeRefusalExemptCommands = []string{
	"ethos session", "ethos session show", "ethos session roster", "ethos session list",
	"ethos session write-current", "ethos session purge",
	"ethos adr list", "ethos adr show",
}

func exemptFromStoreRefusal(cmd *cobra.Command) bool {
	if cmd.Name() == "schema" {
		return true
	}
	path := cmd.CommandPath()
	for _, p := range storeRefusalExemptTrees {
		if path == p || strings.HasPrefix(path, p+" ") {
			return true
		}
	}
	return slices.Contains(storeRefusalExemptCommands, path)
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
