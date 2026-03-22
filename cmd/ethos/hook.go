package main

import (
	"fmt"
	"os"

	"github.com/punt-labs/ethos/internal/hook"
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

func runHookSessionStart() {
	s := store()
	ss := sessionStore()
	if err := hook.HandleSessionStart(os.Stdin, s, ss); err != nil {
		fmt.Fprintf(os.Stderr, "ethos hook session-start: %v\n", err)
	}
}

func runHookSessionEnd() {
	ss := sessionStore()
	if err := hook.HandleSessionEnd(os.Stdin, ss); err != nil {
		fmt.Fprintf(os.Stderr, "ethos hook session-end: %v\n", err)
	}
}

func runHookSubagentStart() {
	s := store()
	ss := sessionStore()
	if err := hook.HandleSubagentStart(os.Stdin, s, ss); err != nil {
		fmt.Fprintf(os.Stderr, "ethos hook subagent-start: %v\n", err)
	}
}

func runHookSubagentStop() {
	ss := sessionStore()
	if err := hook.HandleSubagentStop(os.Stdin, ss); err != nil {
		fmt.Fprintf(os.Stderr, "ethos hook subagent-stop: %v\n", err)
	}
}

func runHookFormatOutput() {
	if err := hook.HandleFormatOutput(os.Stdin); err != nil {
		fmt.Fprintf(os.Stderr, "ethos hook format-output: %v\n", err)
	}
}
