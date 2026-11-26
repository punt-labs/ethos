package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/spf13/cobra"
)

func init() {
	handoff := &cobra.Command{Use: "handoff", Short: "Manage handoff gate dependencies", GroupID: "admin"}
	var agent, localFile string
	fetch := &cobra.Command{
		Use:   "fetch-runtime",
		Short: "Install the current platform's pinned handoff runtime archives",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := requireCheckout(cmd.ErrOrStderr(), "handoff fetch-runtime"); err != nil {
				return err
			}
			loc := currentRoots().Located
			var installed []string
			var err error
			if cmd.Flags().Changed("file") {
				installed, err = hook.InstallHandoffRuntimeFile(cmd.Context(), loc, agent, localFile)
			} else {
				installed, err = hook.FetchHandoffRuntimes(cmd.Context(), loc, agent)
			}
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOutput {
				return json.NewEncoder(out).Encode(installed)
			}
			for _, archive := range installed {
				if _, err := fmt.Fprintln(out, archive); err != nil {
					return err
				}
			}
			return nil
		},
	}
	fetch.Flags().StringVar(&agent, "agent", "", "Select the handoff policy for this agent instead of the project default")
	fetch.Flags().StringVar(&localFile, "file", "", "Install this local archive without downloading")
	var dataAgent string
	refresh := &cobra.Command{
		Use:   "refresh-data",
		Short: "Refresh the handoff policy's read-only data snapshots",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := requireCheckout(cmd.ErrOrStderr(), "handoff refresh-data"); err != nil {
				return err
			}
			refreshed, err := hook.RefreshHandoffData(cmd.Context(), currentRoots().Located, dataAgent)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOutput {
				return json.NewEncoder(out).Encode(refreshed)
			}
			for _, name := range refreshed {
				if _, err := fmt.Fprintln(out, name); err != nil {
					return err
				}
			}
			return nil
		},
	}
	refresh.Flags().StringVar(&dataAgent, "agent", "", "Select the handoff policy for this agent instead of the project default")
	handoff.AddCommand(fetch, refresh, &cobra.Command{
		Use:   "approve-live <slug>",
		Short: "Approve the current revision of a live-verified handoff policy",
		Args:  cobra.ExactArgs(1),
		RunE:  approveLiveHandoff,
	}, &cobra.Command{
		Use:   "revoke-live <slug>",
		Short: "Revoke the live-verified handoff approval for this checkset",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			checkout, err := requireCheckout(cmd.ErrOrStderr(), "handoff revoke-live")
			if err != nil {
				return err
			}
			policy, err := hook.ResolveHandoffLiveRevocation(checkout, args[0])
			if err != nil {
				return err
			}
			return policy.Revoke()
		},
	})
	rootCmd.AddCommand(handoff)
}

func isLiveApprovalCommand(path string) bool {
	return path == "ethos handoff approve-live" || path == "ethos handoff revoke-live"
}

func approveLiveHandoff(cmd *cobra.Command, args []string) error {
	in, ok := cmd.InOrStdin().(*os.File)
	if err := checkLiveApprovalTerminal(ok && isTTY(in), os.Environ()); err != nil {
		return err
	}
	checkout, err := requireCheckout(cmd.ErrOrStderr(), "handoff approve-live")
	if err != nil {
		return err
	}
	policy, err := hook.ResolveHandoffLivePolicy(checkout, args[0])
	if err != nil {
		return err
	}
	description, err := policy.Description()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(out, "Repository: %q\nCheckset: %s\nPolicy SHA-256: %s\n%s\nApprove this policy to run without sandboxing? Type yes to approve: ", policy.Repo, policy.Slug, policy.Revision, description); err != nil {
		return err
	}
	if err := confirmLiveApproval(in); err != nil {
		return err
	}
	if err := policy.Approve(); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Approved %s at %s\n", policy.Slug, policy.Revision)
	return err
}

func checkLiveApprovalTerminal(tty bool, env []string) error {
	if !tty {
		return errors.New("approve-live requires an interactive TTY on stdin")
	}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if value != "" && (key == "CLAUDECODE" || key == "CLAUDE_PID" || strings.HasPrefix(key, "CLAUDE_CODE_") || key == "OPENCODE" || strings.HasPrefix(key, "OPENCODE_")) {
			return errors.New("approve-live refuses to run in an agent environment")
		}
	}
	return nil
}

func confirmLiveApproval(in io.Reader) error {
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "yes" {
		return errors.New("live policy not approved")
	}
	return nil
}
