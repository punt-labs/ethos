package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/internal/seed"
	"github.com/spf13/cobra"
)

var seedForce bool

var seedCmd = &cobra.Command{
	Use:          "seed",
	Short:        "Deploy starter roles, talents, and skills to global directories",
	GroupID:      "admin",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runSeed,
}

func init() {
	seedCmd.Flags().BoolVar(&seedForce, "force", false, "Overwrite existing files")
	rootCmd.AddCommand(seedCmd)
}

func runSeed(cmd *cobra.Command, args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("finding home directory: %w", err)
	}

	destRoot := filepath.Join(home, ".punt-labs", "ethos")
	skillsRoot := filepath.Join(home, ".claude", "skills")

	result, err := seed.SeedVersion(destRoot, skillsRoot, version, seedForce)
	if err != nil {
		if result != nil {
			for _, e := range result.Errors {
				fmt.Fprintf(os.Stderr, "  error: %s\n", e)
			}
		}
		return err
	}

	for _, d := range result.Deployed {
		fmt.Printf("  deployed:  %s\n", d)
	}
	for _, u := range result.Updated {
		fmt.Printf("  updated:   %s\n", u)
	}
	for _, u := range result.Unchanged {
		fmt.Printf("  unchanged: %s\n", u)
	}
	for _, s := range result.Skipped {
		fmt.Printf("  skipped (exists): %s\n", s)
	}
	for _, e := range result.Edited {
		fmt.Printf("  skipped (local edit): %s\n", e)
	}
	for _, rp := range result.Repaired {
		fmt.Printf("  repaired (was empty): %s\n", rp)
	}

	// The "wrote" count is every file seed put on disk this run: new, updated,
	// and repaired. Unchanged/skipped/edited files were not written.
	fmt.Printf("\nSeeded %d files: %d new, %d updated, %d repaired, %d unchanged, %d skipped, %d local edit(s)\n",
		len(result.Deployed)+len(result.Updated)+len(result.Repaired),
		len(result.Deployed), len(result.Updated), len(result.Repaired),
		len(result.Unchanged), len(result.Skipped), len(result.Edited))
	if len(result.Edited) > 0 {
		fmt.Printf("%d file(s) look locally edited; re-run 'ethos seed --force' to overwrite them.\n",
			len(result.Edited))
	}
	return nil
}
