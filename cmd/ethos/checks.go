package main

import (
	"github.com/punt-labs/ethos/v4/internal/schema"
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{Use: "checks", Short: "Project-defined check sets"}
	cmd.AddCommand(newSchemaCmd(schema.CheckSet))
	rootCmd.AddCommand(cmd)
}
