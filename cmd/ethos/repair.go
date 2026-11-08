package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/punt-labs/ethos/v4/internal/repair"
)

func newRepairCommand() *cobra.Command {
	var yes, dryRun bool
	cmd := &cobra.Command{
		Use: "repair", Short: "Delete known obsolete repository trace files after confirmation",
		Long: `Inventory known obsolete repository prompts, reflections, log chunks and runtime residue.
WARNING: deletion permanently loses historical detail. Stop all old/new writers,
hooks and MCP servers before confirming. Old missions may need manual retirement;
repair never invents round decisions, corrections or lifecycle transitions.
Canonical local data, current publications, credentials/overlays, shared records,
global state and arbitrary user files are retained. Busy locks refuse cleanup.
No migration, Git staging/commit, history rewrite or configuration rewrite occurs.
Review tracked deletions and commit them normally. Run ethos enable afterwards to
refresh repository integration, then reconnect long-lived MCP servers.
Without --yes, confirmation requires a terminal; default, no and EOF delete nothing.
Use --dry-run for read-only inventory without confirmation.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := currentRoots().Located.Layout()
			if err != nil {
				return fmt.Errorf("repair: %w", err)
			}
			plan, err := repair.Inventory(cmd.Context(), p)
			if err != nil {
				return fmt.Errorf("repair inventory: %w", err)
			}
			out := cmd.OutOrStdout()
			if jsonOutput {
				out = cmd.ErrOrStderr()
			}
			var size int64
			for _, item := range plan.Items {
				size += item.Bytes
			}
			if _, err := fmt.Fprintf(out, "Repair inventory: %d files, %d bytes. Git index unchanged.\n", len(plan.Items), size); err != nil {
				return err
			}
			for _, item := range plan.Items {
				if _, err := fmt.Fprintf(out, "  DELETE %q (%d bytes, tracked=%t)\n", item.Path, item.Bytes, item.Tracked); err != nil {
					return err
				}
			}
			var removed []string
			var applyErr error
			if !dryRun && len(plan.Items) > 0 {
				if _, err := fmt.Fprintln(out, "WARNING: This permanently deletes the listed old prompts, reflections, events/audit history and runtime residue; no backup or migration is made. Stop ALL old/new writers, hooks and MCP servers before confirming. Historical missions may need manual retirement; repair does not change their authority."); err != nil {
					return err
				}
				confirmed := yes
				if !confirmed {
					input, ok := cmd.InOrStdin().(*os.File)
					if !ok || !term.IsTerminal(int(input.Fd())) {
						return fmt.Errorf("repair requires a terminal for confirmation; use --yes only after reviewing permanent deletion and stopping all writers (or --dry-run)")
					}
					if _, err := fmt.Fprint(out, "Are you sure? [y/N] "); err != nil {
						return err
					}
					answer, err := bufio.NewReader(input).ReadString('\n')
					if err != nil && !errors.Is(err, io.EOF) {
						return fmt.Errorf("reading repair confirmation: %w", err)
					}
					answer = strings.TrimSpace(answer)
					confirmed = err == nil && (strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes"))
				}
				if confirmed {
					removed, applyErr = plan.Apply(cmd.Context())
				}
			}
			if _, err := fmt.Fprintf(out, "Deleted %d files; remaining files and Git index unchanged. Review tracked deletions and stage/commit normally.\n", len(removed)); err != nil {
				return errors.Join(applyErr, err)
			}
			if !dryRun && applyErr == nil && (yes || len(removed) > 0) {
				if _, err := fmt.Fprintf(out, "Next: ethos enable (checkout records %q; canonical local data %q), then reconnect MCP servers.\n", p.CheckoutRecordRoot(), p.LocalRoot()); err != nil {
					return err
				}
			}
			if jsonOutput {
				return errors.Join(applyErr, json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					Items   []repair.Item `json:"items"`
					Removed []string      `json:"removed"`
				}{plan.Items, removed}))
			}
			return applyErr
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm permanent deletion and assert all old/new writers, hooks and MCP servers are stopped")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Inventory only; do not prompt, lock or delete")
	return cmd
}

func init() { rootCmd.AddCommand(newRepairCommand()) }
