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
	Use:     "seed",
	Short:   "Deploy starter roles, talents, and skills to global directories",
	GroupID: "admin",
	Args:    cobra.NoArgs,
	RunE:    runSeed,
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

	result, err := seed.Seed(destRoot, skillsRoot, seedForce)
	if err != nil {
		if result != nil {
			for _, e := range result.Errors {
				fmt.Fprintf(os.Stderr, "  error: %s\n", e)
			}
		}
		return err
	}

	for _, d := range result.Deployed {
		fmt.Printf("  deployed: %s\n", d)
	}
	for _, s := range result.Skipped {
		fmt.Printf("  skipped (exists): %s\n", s)
	}

	fmt.Printf("\nSeeded %d files (%d skipped)\n", len(result.Deployed), len(result.Skipped))
	return nil
}
