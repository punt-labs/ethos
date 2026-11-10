package main

import (
	"context"
	"fmt"
	"slices"
	"sync/atomic"

	"github.com/spf13/cobra"

	"github.com/punt-labs/ethos/v4/internal/autorepair"
)

// ensureStorage is the repair gate. A variable so the tests can count calls.
var ensureStorage = autorepair.Ensure

// repairNotice holds the line a repair earned for "hook session-start", which
// prints JSON on stdout and whose stderr goes to an unread log: the line rides
// in the JSON instead (DES-084 Output).
var repairNotice atomic.Pointer[string]

// takeRepairNotice returns the held line once, "" when there is none.
func takeRepairNotice() string {
	if p := repairNotice.Swap(nil); p != nil {
		return *p
	}
	return ""
}

// noEnsure are the commands, by top-level name, that never read storage.
// cobra's own completion requests are completion.
var noEnsure = []string{"version", "completion", "help", "uninstall",
	cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd}

func exemptFromEnsure(cmd *cobra.Command) bool {
	top := cmd
	for top.HasParent() && top.Parent().HasParent() {
		top = top.Parent()
	}
	return top.HasParent() && slices.Contains(noEnsure, top.Name())
}

// ensureForServe is the MCP repair gate: it locates for the call's working
// directory, runs Ensure, and returns the line a repair earned.
func ensureForServe(ctx context.Context) string {
	autorepair.BinaryVersion = resolveVersion()
	res, _ := ensureStorage(ctx, locateRoots().Located, defaultGlobalRoot())
	return res.Line()
}

// ensureForCommand runs the repair gate for cmd against r and returns the
// roots the command should use. A repair can change what locating finds (a
// renamed config), so a repaired store is located again. Every command prints
// the line to stderr (pre-commit must never print to stdout: it would block
// the commit); session start holds it for its JSON.
func ensureForCommand(cmd *cobra.Command, r *repoRoots) *repoRoots {
	if exemptFromEnsure(cmd) {
		return r
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	autorepair.BinaryVersion = resolveVersion()
	res, _ := ensureStorage(ctx, r.Located, defaultGlobalRoot())
	line := res.Line()
	if line == "" {
		return r
	}
	if cmd.CommandPath() == "ethos hook session-start" {
		repairNotice.Store(&line)
	} else {
		fmt.Fprintln(cmd.ErrOrStderr(), line)
	}
	fresh := locateRoots()
	fresh.Global = r.Global
	return fresh
}
