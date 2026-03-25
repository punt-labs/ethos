package main

import (
	"fmt"
	"os"

	"github.com/punt-labs/ethos/internal/hook"
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
		hookFormatOutputCmd,
	)
	rootCmd.AddCommand(hookCmd)
}

func runHookSessionStart() {
	s := globalStore()
	ss := sessionStore()
	if err := hook.HandleSessionStart(os.Stdin, s, ss); err != nil {
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
	s := globalStore()
	ss := sessionStore()
	if err := hook.HandleSubagentStart(os.Stdin, s, ss); err != nil {
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

func runHookFormatOutput() {
	if err := hook.HandleFormatOutput(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "ethos hook format-output: %v\n", err)
		os.Exit(1)
	}
}
