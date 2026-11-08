package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/provenance"
)

func newMissionPublishCommand() *cobra.Command {
	var base string
	var adrs []string
	cmd := &cobra.Command{
		Use:   "publish <id>...",
		Short: "Publish complete selected blame records into this checkout and its actual index",
		Long: `Snapshot repository mission contracts (including inline round decisions), all results,
delegations, shared corrections, linked/explicit ADRs and selected summary rows.
Exclude forensic trace. Preserve unrelated checkout and staged files. Refuse
divergent selected targets or source/index blob differences. Failures may leave
reported checkout copies, but never partly install the actual index.

Commit the publication, then use ethos provenance message <head> to generate a
squash body. Pass it to gh pr merge --squash --match-head-commit SHA --body-file
GENERATED. Fetch the actual merge commit and run ethos provenance verify <mergeSHA>.
Ethos makes no remote calls and does not report merge success itself.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			roots := currentRoots()
			policy, err := roots.Located.Layout()
			if err != nil {
				return fmt.Errorf("mission publish: %w", err)
			}
			env, err := gitenv.ActualIndexEnv()
			if err != nil {
				return err
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("mission publish: home directory: %w", err)
			}
			store := mission.NewRepositoryStore(policy, filepath.Join(home, ".punt-labs", "ethos"))
			snapshot, err := store.SnapshotSelection(cmd.Context(), args, adrs)
			if err != nil {
				return err
			}
			publisher := provenance.Publisher{Policy: policy, Repository: provenance.Repository{Root: roots.Checkout, Env: env}}
			receipt, publishErr := publisher.Publish(cmd.Context(), snapshot, base)
			if jsonOutput {
				result := struct {
					Receipt provenance.Receipt `json:"receipt"`
					Error   string             `json:"error,omitempty"`
				}{Receipt: receipt}
				if publishErr != nil {
					result.Error = publishErr.Error()
				}
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
					return errors.Join(publishErr, err)
				}
			} else {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Manifest: %s\nActual index installed: %t\nIndex crash durability: %s\nCheckout files copied: %d\n", receipt.Manifest, receipt.IndexInstalled, receipt.Durability, len(receipt.Copied)); err != nil {
					return errors.Join(publishErr, err)
				}
				for _, path := range receipt.Copied {
					if _, err := fmt.Fprintln(cmd.OutOrStdout(), path); err != nil {
						return errors.Join(publishErr, err)
					}
				}
			}
			return publishErr
		},
	}
	cmd.Flags().StringVar(&base, "base", "HEAD^", "Ancestor base reference for branch line-association evidence")
	cmd.Flags().StringSliceVar(&adrs, "adr", nil, "Additional explicit ADR IDs (comma-separated or repeated); linked ADRs are automatic")
	return cmd
}

func init() { missionCmd.AddCommand(newMissionPublishCommand()) }
