package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "ethos",
	Short: "Identity binding for humans and AI agents",
	Long: `Identity binding for humans and AI agents.

Unifies name, email, GitHub handle, writing style, personality, and
talents into a single identity that other tools read. Same schema for
humans and agents. Repo-scoped team identities are git-tracked.`,
}

func init() {
	rootCmd.SilenceUsage = true
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "JSON output")

	rootCmd.AddGroup(
		&cobra.Group{ID: "identity", Title: "Identity:"},
		&cobra.Group{ID: "attributes", Title: "Attributes:"},
		&cobra.Group{ID: "session", Title: "Session:"},
		&cobra.Group{ID: "admin", Title: "Admin:"},
	)
}

// completionCmd generates shell completion scripts.
var completionCmd = &cobra.Command{
	Use:     "completion <bash|zsh|fish>",
	Short:   "Generate shell completion script",
	GroupID: "admin",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		switch args[0] {
		case "bash":
			return rootCmd.GenBashCompletionV2(os.Stdout, true)
		case "zsh":
			return rootCmd.GenZshCompletion(os.Stdout)
		case "fish":
			return rootCmd.GenFishCompletion(os.Stdout, true)
		default:
			return fmt.Errorf("unsupported shell: %s", args[0])
		}
	},
}

func init() {
	rootCmd.AddCommand(completionCmd)
}
