package main

import (
	"fmt"
	"os"

	"github.com/punt-labs/ethos/internal/hook"
	"github.com/punt-labs/ethos/internal/mission"
	"github.com/punt-labs/ethos/internal/resolve"
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
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookSessionStart()
	},
}

var hookSessionEndCmd = &cobra.Command{
	Use:   "session-end",
	Short: "SessionEnd hook handler",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookSessionEnd()
	},
}

var hookSubagentStartCmd = &cobra.Command{
	Use:   "subagent-start",
	Short: "SubagentStart hook handler",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookSubagentStart()
	},
}

var hookSubagentStopCmd = &cobra.Command{
	Use:   "subagent-stop",
	Short: "SubagentStop hook handler",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookSubagentStop()
	},
}

var hookPreCompactCmd = &cobra.Command{
	Use:   "pre-compact",
	Short: "PreCompact hook handler",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookPreCompact()
	},
}

var hookFormatOutputCmd = &cobra.Command{
	Use:   "format-output",
	Short: "PostToolUse output formatter",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookFormatOutput()
	},
}

var hookPreToolUseCmd = &cobra.Command{
	Use:   "pre-tool-use",
	Short: "PreToolUse hook handler (verifier file allowlist)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookPreToolUse()
	},
}

var hookAuditLogCmd = &cobra.Command{
	Use:   "audit-log",
	Short: "PostToolUse audit logger",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookAuditLog()
	},
}

func init() {
	hookCmd.AddCommand(
		hookSessionStartCmd,
		hookSessionEndCmd,
		hookSubagentStartCmd,
		hookSubagentStopCmd,
		hookPreCompactCmd,
		hookPreToolUseCmd,
		hookFormatOutputCmd,
		hookAuditLogCmd,
	)
	rootCmd.AddCommand(hookCmd)
}

func runHookSessionStart() error {
	is := identityStore()
	deps := hook.SessionStartDeps{
		Store:    is,
		Sessions: sessionStore(),
		Teams:    layeredTeamStore(is),
		Roles:    layeredRoleStore(is),
	}
	if err := hook.HandleSessionStart(os.Stdin, deps); err != nil {
		return fmt.Errorf("hook session-start: %w", err)
	}
	return nil
}

func runHookSessionEnd() error {
	ss := sessionStore()
	if err := hook.HandleSessionEnd(os.Stdin, ss); err != nil {
		return fmt.Errorf("hook session-end: %w", err)
	}
	return nil
}

func runHookSubagentStart() error {
	s := identityStore()
	ss := sessionStore()
	// Phase 3.3: wire the mission store and live hash sources so the
	// verifier hash gate enforces frozen-evaluator pinning at every
	// subagent spawn. A drift between mission create and verifier
	// spawn refuses the spawn with a fatal, operator-readable error.
	ms := missionStore()
	hashSources, err := mission.NewLiveHashSources(s, layeredRoleStore(s), layeredTeamStore(s))
	if err != nil {
		return fmt.Errorf("hook subagent-start: %w", err)
	}
	deps := hook.SubagentStartDeps{
		Identities: s,
		Sessions:   ss,
		Missions:   ms,
		Hash:       hashSources,
		RepoRoot:   resolve.FindRepoRoot(),
	}
	if err := hook.HandleSubagentStartWithDeps(os.Stdin, deps); err != nil {
		return fmt.Errorf("hook subagent-start: %w", err)
	}
	return nil
}

func runHookSubagentStop() error {
	ss := sessionStore()
	if err := hook.HandleSubagentStop(os.Stdin, ss); err != nil {
		return fmt.Errorf("hook subagent-stop: %w", err)
	}
	return nil
}

func runHookPreCompact() error {
	is := identityStore()
	deps := hook.PreCompactDeps{
		Identities: is,
		Sessions:   sessionStore(),
		Teams:      layeredTeamStore(is),
		Roles:      layeredRoleStore(is),
	}
	if err := hook.HandlePreCompact(os.Stdin, deps); err != nil {
		return fmt.Errorf("hook pre-compact: %w", err)
	}
	return nil
}

func runHookPreToolUse() error {
	if err := hook.HandlePreToolUse(os.Stdin, os.Stdout); err != nil {
		return fmt.Errorf("hook pre-tool-use: %w", err)
	}
	return nil
}

func runHookFormatOutput() error {
	if err := hook.HandleFormatOutput(os.Stdin, os.Stdout); err != nil {
		return fmt.Errorf("hook format-output: %w", err)
	}
	return nil
}

func runHookAuditLog() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("hook audit-log: %w", err)
	}
	sessionsDir := home + "/.punt-labs/ethos/sessions"
	_ = hook.HandleAuditLog(os.Stdin, sessionsDir)
	return nil
}
