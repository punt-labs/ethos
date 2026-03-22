package main

import (
	"fmt"
	"os"
)

// runHook dispatches to hook subcommands. These are internal commands
// called by Claude Code hook shell scripts — not for direct user use.
func runHook(args []string) {
	if len(args) == 0 {
		printHookUsage()
		os.Exit(1)
	}

	sub := args[0]
	subcommands := map[string]func(){
		"session-start":  runHookSessionStart,
		"session-end":    runHookSessionEnd,
		"subagent-start": runHookSubagentStart,
		"subagent-stop":  runHookSubagentStop,
		"format-output":  runHookFormatOutput,
	}

	if fn, ok := subcommands[sub]; ok {
		fn()
	} else {
		fmt.Fprintf(os.Stderr, "ethos hook: unknown subcommand %q\n", sub)
		printHookUsage()
		os.Exit(1)
	}
}

func printHookUsage() {
	fmt.Fprint(os.Stderr, `Usage: ethos hook <subcommand>

Internal commands called by Claude Code hooks. Not for direct use.

Subcommands:
  session-start   SessionStart hook handler
  session-end     SessionEnd hook handler
  subagent-start  SubagentStart hook handler
  subagent-stop   SubagentStop hook handler
  format-output   PostToolUse output formatter
`)
}

// Stub handlers — will be implemented in subsequent steps.

func runHookSessionStart() {
	// TODO: Step 3
}

func runHookSessionEnd() {
	// TODO: Step 4
}

func runHookSubagentStart() {
	// TODO: Step 5
}

func runHookSubagentStop() {
	// TODO: Step 6
}

func runHookFormatOutput() {
	// TODO: Step 7
}
