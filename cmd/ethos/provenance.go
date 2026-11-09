package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/provenance"
)

func newProvenanceCommand() *cobra.Command {
	command := &cobra.Command{Use: "provenance", Short: "Generate squash references and verify committed blame snapshots", GroupID: "mission"}
	var staged bool
	verifyIndex := &cobra.Command{
		Use: "verify-index [manifest]...", Short: "Verify selected snapshots in the actual committing index",
		Args: func(cmd *cobra.Command, args []string) error {
			if staged {
				return cobra.NoArgs(cmd, args)
			}
			return cobra.MinimumNArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			if staged {
				args, err = verifyStagedIndex(cmd.Context())
				if err != nil || len(args) == 0 {
					return err // Ordinary commits make no provenance claim.
				}
			} else if err = verifyIndexManifests(cmd.Context(), args); err != nil {
				return err
			}
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					Manifests []string `json:"manifests"`
				}{args})
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Verified selected snapshots in the actual index")
			return err
		},
	}
	verifyIndex.Flags().BoolVar(&staged, "staged", false, "Verify only added/modified manifests in the actual committing index versus HEAD")
	command.AddCommand(verifyIndex, &cobra.Command{
		Use: "message <ref>", Short: "Emit the squash body for the final publication commit", Args: cobra.ExactArgs(1),
		Long: `Verify the final publication commit and write a squash body to stdout.
Save it under the canonical store's .punt-labs/local/ethos/ or approved scratch.
Use gh pr merge --squash --match-head-commit SHA --body-file GENERATED, where SHA
is the exact reviewed ref (available with --json). Then fetch and verify the
actual merge commit with ethos provenance verify <mergeSHA>. This command does
not call GitHub. It refuses a ref that does not add a publication manifest.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := provenanceRepository()
			if err != nil {
				return err
			}
			body, head, err := r.SquashMessage(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					Head string `json:"head"`
					Body string `json:"body"`
				}{head, body})
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), body)
			return err
		},
	}, &cobra.Command{
		Use: "verify <ref>", Short: "Verify provenance against an actual commit (including a squash merge)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := provenanceRepository()
			if err != nil {
				return err
			}
			result, err := r.VerifyCommit(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Verified %s (%d provenance manifest(s))\n", result.Commit, len(result.Manifests))
			return err
		},
	})
	return command
}

// verifyIndexManifests verifies the named manifests in the actual committing
// index.
func verifyIndexManifests(ctx context.Context, manifests []string) error {
	r, err := indexRepository()
	if err != nil {
		return err
	}
	return r.VerifyIndex(ctx, manifests)
}

// verifyStagedIndex verifies the manifests added or modified in the actual
// committing index against HEAD, and returns them. It returns none when the
// commit stages no provenance snapshot.
func verifyStagedIndex(ctx context.Context) ([]string, error) {
	r, err := indexRepository()
	if err != nil {
		return nil, err
	}
	manifests, err := r.StagedManifests(ctx)
	if err != nil || len(manifests) == 0 {
		return nil, err
	}
	if err := r.VerifyIndex(ctx, manifests); err != nil {
		return nil, err
	}
	return manifests, nil
}

// indexRepository is provenanceRepository bound to the committing index, which
// differs from the default one during a partial commit (GIT_INDEX_FILE).
func indexRepository() (provenance.Repository, error) {
	r, err := provenanceRepository()
	if err != nil {
		return r, err
	}
	r.Env, err = gitenv.ActualIndexEnv()
	return r, err
}

func provenanceRepository() (provenance.Repository, error) {
	roots := currentRoots()
	if err := roots.Err(); err != nil {
		return provenance.Repository{}, err
	}
	if roots.Checkout == "" {
		return provenance.Repository{}, fmt.Errorf("provenance requires a Git checkout")
	}
	return provenance.Repository{Root: roots.Checkout, Env: gitenv.Parsed()}, nil
}

func init() { rootCmd.AddCommand(newProvenanceCommand()) }
