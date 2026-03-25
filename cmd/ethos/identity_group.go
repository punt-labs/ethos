package main

import (
	"github.com/spf13/cobra"
)

var identityCmd = &cobra.Command{
	Use:   "identity <method>",
	Short: "Manage identities (whoami, list, get, create)",
	Run: func(cmd *cobra.Command, args []string) {
		runWhoami()
	},
}

func init() {
	var getReference bool
	var identityCreateFile string

	identityWhoamiCmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show the caller's identity",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			runWhoami()
		},
	}
	identityWhoamiCmd.Flags().BoolVar(&whoamiReference, "reference", false, "Include reference identity data")

	identityListCmd := &cobra.Command{
		Use:   "list",
		Short: "List all identities",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			runList()
		},
	}

	identityGetCmd := &cobra.Command{
		Use:   "get <handle>",
		Short: "Show identity details",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			runShow(args[0], getReference)
		},
	}
	identityGetCmd.Flags().BoolVar(&getReference, "reference", false, "Include reference identity data")

	identityCreateCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new identity",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if identityCreateFile != "" {
				createFromFile(identityCreateFile)
			} else {
				createInteractive()
			}
		},
	}
	identityCreateCmd.Flags().StringVarP(&identityCreateFile, "file", "f", "", "Create identity from YAML file")

	identityCmd.AddCommand(
		identityWhoamiCmd,
		identityListCmd,
		identityGetCmd,
		identityCreateCmd,
	)

	rootCmd.AddCommand(identityCmd)
}
