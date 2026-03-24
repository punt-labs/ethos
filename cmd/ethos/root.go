package main

import "github.com/spf13/cobra"

var rootCmd = &cobra.Command{
	Use:   "ethos",
	Short: "Identity binding for humans and AI agents",
	Long: `ethos: identity binding for humans and AI agents

Product commands:
  whoami            Show the caller's identity
  create            Create a new identity
  list              List all identities
  show <handle>     Show identity details
  ext               Manage tool-scoped extensions

Attribute commands:
  talent            Manage talents (create, list, show, add, remove)
  personality       Manage personalities (create, list, show, set)
  writing-style     Manage writing styles (create, list, show, set)

Session commands:
  iam <persona>     Declare persona in current session
  session           Show or manage session roster

Admin commands:
  version           Print version
  doctor            Check installation health
  serve             Start MCP server (stdio transport)
  uninstall         Remove plugin (--purge to remove binary + data)`,
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "JSON output")
}
