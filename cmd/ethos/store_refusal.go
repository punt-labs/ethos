package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/spf13/cobra"
)

// storeRefusalExempt lists the commands that run when the repository store
// cannot be located: they report on, repair or do not use the store, or (setup,
// seed) locate and refuse for themselves. Every other command refuses (see
// refuseUnlocatableStore).
var storeRefusalExempt = []string{
	"ethos version", "ethos doctor", "ethos completion", "ethos help",
	"ethos hook", "ethos serve", "ethos enable", "ethos disable",
	"ethos uninstall", "ethos setup", "ethos seed", "ethos whoami", "ethos iam",
	"ethos identity whoami", "ethos session", "ethos audit", "ethos adr",
	"ethos __complete", "ethos __completeNoDesc",
}

func exemptFromStoreRefusal(cmd *cobra.Command) bool {
	if cmd.Name() == "schema" {
		return true
	}
	path := cmd.CommandPath()
	for _, p := range storeRefusalExempt {
		if path == p || strings.HasPrefix(path, p+" ") {
			return true
		}
	}
	return false
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
		return fmt.Errorf("%s: %w; refusing to use the global store instead", cmd.CommandPath(), err)
	}
	return nil
}
