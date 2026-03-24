package main

import "github.com/spf13/cobra"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version",
	Run: func(cmd *cobra.Command, args []string) {
		runVersion()
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
