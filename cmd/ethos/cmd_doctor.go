package main

import "github.com/spf13/cobra"

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check installation health",
	Run: func(cmd *cobra.Command, args []string) {
		runDoctor()
	},
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}
