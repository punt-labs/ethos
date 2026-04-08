package main

import (
	"fmt"
	"os"

	"github.com/punt-labs/ethos/internal/hook"
	"github.com/punt-labs/ethos/internal/mission"
	"github.com/spf13/cobra"
)

var hookCmd = &cobra.Command{
	Use:    "hook",
	Short:  "Internal hook dispatcher (not for direct use)",
	Hidden: true,
}

var hookSessionStartCmd = &cobra.Command{
	Use:   "session-start",
	Short: "SessionStart hook handler",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runHookSessionStart()
	},
}

var hookSessionEndCmd = &cobra.Command{
	Use:   "session-end",
	Short: "SessionEnd hook handler",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runHookSessionEnd()
	},
}

var hookSubagentStartCmd = &cobra.Command{
	Use:   "subagent-start",
	Short: "SubagentStart hook handler",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runHookSubagentStart()
	},
}

var hookSubagentStopCmd = &cobra.Command{
	Use:   "subagent-stop",
	Short: "SubagentStop hook handler",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runHookSubagentStop()
	},
}

var hookPreCompactCmd = &cobra.Command{
	Use:   "pre-compact",
	Short: "PreCompact hook handler",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runHookPreCompact()
	},
}

var hookFormatOutputCmd = &cobra.Command{
	Use:   "format-output",
	Short: "PostToolUse output formatter",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runHookFormatOutput()
	},
}

func init() {
	hookCmd.AddCommand(
		hookSessionStartCmd,
		hookSessionEndCmd,
		hookSubagentStartCmd,
		hookSubagentStopCmd,
		hookPreCompactCmd,
		hookFormatOutputCmd,
	)
	rootCmd.AddCommand(hookCmd)
}

func runHookSessionStart() {
	is := identityStore()
	deps := hook.SessionStartDeps{
		Store:    is,
		Sessions: sessionStore(),
		Teams:    layeredTeamStore(is),
		Roles:    layeredRoleStore(is),
	}
	if err := hook.HandleSessionStart(os.Stdin, deps); err != nil {
		fmt.Fprintf(os.Stderr, "ethos hook session-start: %v\n", err)
		os.Exit(1)
	}
}

func runHookSessionEnd() {
	ss := sessionStore()
	if err := hook.HandleSessionEnd(os.Stdin, ss); err != nil {
		fmt.Fprintf(os.Stderr, "ethos hook session-end: %v\n", err)
		os.Exit(1)
	}
}

func runHookSubagentStart() {
	s := identityStore()
	ss := sessionStore()
	// Phase 3.3: wire the mission store and live hash sources so the
	// verifier hash gate enforces frozen-evaluator pinning at every
	// subagent spawn. A drift between mission create and verifier
	// spawn refuses the spawn with a fatal, operator-readable error.
	ms := missionStore()
	hashSources := mission.NewLiveHashSources(s, layeredRoleStore(s), layeredTeamStore(s))
	deps := hook.SubagentStartDeps{
		Identities: s,
		Sessions:   ss,
		Missions:   ms,
		Hash:       hashSources,
	}
	if err := hook.HandleSubagentStartWithDeps(os.Stdin, deps); err != nil {
		fmt.Fprintf(os.Stderr, "ethos hook subagent-start: %v\n", err)
		os.Exit(1)
	}
}

func runHookSubagentStop() {
	ss := sessionStore()
	if err := hook.HandleSubagentStop(os.Stdin, ss); err != nil {
		fmt.Fprintf(os.Stderr, "ethos hook subagent-stop: %v\n", err)
		os.Exit(1)
	}
}

func runHookPreCompact() {
	is := identityStore()
	deps := hook.PreCompactDeps{
		Identities: is,
		Sessions:   sessionStore(),
		Teams:      layeredTeamStore(is),
		Roles:      layeredRoleStore(is),
	}
	if err := hook.HandlePreCompact(os.Stdin, deps); err != nil {
		fmt.Fprintf(os.Stderr, "ethos hook pre-compact: %v\n", err)
		os.Exit(1)
	}
}

func runHookFormatOutput() {
	if err := hook.HandleFormatOutput(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "ethos hook format-output: %v\n", err)
		os.Exit(1)
	}
}
