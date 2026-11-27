package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/enable"
	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/spf13/cobra"
)

var enableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Enable ethos in the current repo, or refresh with --refresh or --agents-only",
	Long: `Enable ethos in the current repo.

Deposits the vendored agent guide and its manifest, writes the enabled
marker, adds the @.punt-labs/ethos/CLAUDE.md import line to the repo
CLAUDE.md, and chains the audit-seal and trailer git hooks. Use --refresh
to update vendored content and Claude agent definitions, or --agents-only
to update definitions without changing enablement. Run 'ethos setup'
separately to configure identities.`,
	GroupID:      "admin",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEnable(cmd)
	},
}

func init() {
	enableCmd.Flags().Bool("refresh", false, "Refresh vendored content and Claude agent definitions")
	enableCmd.Flags().Bool("agents-only", false, "Refresh Claude agent definitions without changing enablement or guides")
	enableCmd.MarkFlagsMutuallyExclusive("refresh", "agents-only")
	rootCmd.AddCommand(enableCmd)
}

func runEnable(cmd *cobra.Command) error {
	repoRoot := locateForCommand(cmd, "enable")
	if repoRoot == "" {
		return failClosed{}
	}
	refresh, err := cmd.Flags().GetBool("refresh")
	if err != nil {
		return err
	}
	agentsOnly, err := cmd.Flags().GetBool("agents-only")
	if err != nil {
		return err
	}
	if agentsOnly {
		rep := &enable.Report{RepoRoot: repoRoot}
		if err := refreshClaudeAgents(rep, false, true); err != nil {
			return err
		}
		return finishEnable(cmd, rep, agentRefreshAction(len(rep.Warnings) != 0))
	}
	// Deposit (guide/marker/import/hooks) is per-checkout (repoRoot); the
	// "has setup been run?" config read resolves the shared store (#370).
	// A fresh repo named by ETHOS_REPO_ROOT has no store yet, which the store
	// lookup refuses; enable is what creates it, so the checkout is the store.
	storeRoot, err := enableStoreRoot(repoRoot, func() (string, error) {
		r := currentRoots()
		if r.StoreErr != nil {
			return "", r.StoreErr
		}
		// A malformed or unreadable ethos.yaml (ConfigErr) does not stop enable:
		// the store root is known, and EnableTo warns about the config.
		return r.Store, r.EthosErr
	})
	if err != nil {
		return err
	}
	rep, err := enable.EnableTo(repoRoot, storeRoot, enable.Options{Refresh: refresh})
	action := "enabled"
	gitlinkRefresh := refresh && errors.Is(err, enable.ErrGitlinkMount)
	if gitlinkRefresh {
		rep.Warnings = append(rep.Warnings, err.Error())
	} else if err != nil {
		emitPartialReport(cmd, rep)
		return err
	}
	if refresh {
		warningsBefore := len(rep.Warnings)
		if err := refreshClaudeAgents(rep, gitlinkRefresh, false); err != nil {
			emitPartialReport(cmd, rep)
			return err
		}
		if gitlinkRefresh {
			action = agentRefreshAction(len(rep.Warnings) != warningsBefore)
		}
	}
	return finishEnable(cmd, rep, action)
}

func agentRefreshAction(skipped bool) string {
	if skipped {
		return "left Claude agent definitions unchanged"
	}
	return "refreshed Claude agent definitions"
}

func refreshClaudeAgents(rep *enable.Report, gitlink, checkGitlink bool) error {
	r := currentRoots()
	if err := r.configIssue(); err != nil {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("agent definitions left unchanged: %v", err))
		return nil
	}
	if r.Config == nil && r.ConfigErr == nil {
		if checkGitlink {
			var err error
			gitlink, err = audit.IsGitlinkMount(rep.RepoRoot)
			if err != nil {
				return fmt.Errorf("checking team gitlink: %w", err)
			}
		}
		if gitlink {
			hasStubs, err := hook.RefreshAgentStubs(r.Located)
			if err != nil {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("could not refresh agent stubs: %v", err))
				return nil
			}
			if hasStubs {
				return nil
			}
			rep.Warnings = append(rep.Warnings, "agent definitions left unchanged: no repository configuration found for the team gitlink; if the team submodule is not initialized, run git submodule update --init -- .punt-labs/ethos")
			return nil
		}
	}
	identities := identityStore()
	if err := hook.RefreshAgentDefinitions(r.Located, identities, layeredTeamStore(identities), layeredRoleStore(identities)); err != nil {
		return fmt.Errorf("refreshing Claude agent definitions: %w", err)
	}
	return nil
}

func finishEnable(cmd *cobra.Command, rep *enable.Report, action string) error {
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), rep)
	}
	printEnableReport(cmd.OutOrStdout(), cmd.ErrOrStderr(), rep, action)
	return nil
}

// enableStoreRoot is the store enable reads, found by locate. Only an
// ETHOS_REPO_ROOT naming a repository with no store yet falls back to the
// checkout: enable creates the store there. Any other failure (an unreadable
// .git, a refused override) must not send the deposit to a tree locate never
// chose.
func enableStoreRoot(repoRoot string, locate func() (string, error)) (string, error) {
	root, err := locate()
	switch {
	case err == nil:
		return root, nil
	case errors.Is(err, resolve.ErrOverrideNoStore):
		return repoRoot, nil
	}
	return "", fmt.Errorf("enable: locating the store: %w", err)
}

// locateForCommand returns the work tree root for verb, or "" after printing
// why there is none: a plain "not in a git repository", or the cause when a
// .git exists but cannot be examined or ETHOS_REPO_ROOT was refused.
func locateForCommand(cmd *cobra.Command, verb string) string {
	r := currentRoots()
	if r.Checkout != "" {
		return r.Checkout
	}
	reason := "not in a git repository"
	if r.CheckoutErr != nil {
		reason = r.CheckoutErr.Error()
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "ethos: %s: %s\n", verb, reason)
	return ""
}

// emitPartialReport surfaces the completed steps and warnings to stderr on the
// error path, so a warning already produced (e.g. deposit's grandfather
// overwrite) is not lost when a later step fails. In --json mode it writes the
// partial report as JSON to stderr so machine consumers keep it too.
func emitPartialReport(cmd *cobra.Command, rep *enable.Report) {
	if rep == nil {
		return
	}
	errOut := cmd.ErrOrStderr()
	if jsonOutput {
		_ = writeJSON(errOut, rep)
		return
	}
	for _, s := range rep.Steps {
		fmt.Fprintf(errOut, "  %-14s %-9s %s\n", s.Step, s.Status, s.Detail)
	}
	for _, w := range rep.Warnings {
		fmt.Fprintf(errOut, "ethos: %s\n", w)
	}
}

// printEnableReport writes the per-step outcome to out and any warnings to
// errOut. The verb is "enabled" or "disabled".
func printEnableReport(out, errOut io.Writer, rep *enable.Report, verb string) {
	fmt.Fprintf(out, "ethos %s in %s\n", verb, rep.RepoRoot)
	for _, s := range rep.Steps {
		if s.Detail != "" {
			fmt.Fprintf(out, "  %-14s %-9s %s\n", s.Step, s.Status, s.Detail)
		} else {
			fmt.Fprintf(out, "  %-14s %s\n", s.Step, s.Status)
		}
	}
	for _, w := range rep.Warnings {
		fmt.Fprintf(errOut, "ethos: %s\n", w)
	}
	if rep.Hint != "" {
		fmt.Fprintf(out, "\nnext: %s\n", rep.Hint)
	}
}
