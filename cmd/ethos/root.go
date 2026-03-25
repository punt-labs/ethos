package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "ethos",
	Short: "Identity binding for humans and AI agents",
	Long: `ethos: identity binding for humans and AI agents

Product commands:
  whoami            Show the caller's identity
  identity          Manage identities (whoami, list, get, create)
  ext               Manage tool-scoped extensions

Attribute commands:
  talent            Manage talents (create, list, show, add, remove)
  personality       Manage personalities (create, list, show, set)
  writing-style     Manage writing styles (create, list, show, set)

Session commands:
  session           Show or manage session roster

Admin commands:
  version           Print version
  doctor            Check installation health
  serve             Start MCP server (stdio transport)
  uninstall         Remove plugin (--purge to remove binary + data)
  completion        Generate shell completion script`,
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "JSON output")
}

// completionCmd generates shell completion scripts.
var completionCmd = &cobra.Command{
	Use:   "completion <bash|zsh|fish>",
	Short: "Generate shell completion script",
	Args:  cobra.ExactArgs(1),
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
