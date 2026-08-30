package main

import (
	"fmt"

	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/schema"

	"github.com/spf13/cobra"
)

// newSchemaCmd returns the `schema` subcommand for one typed entity. It
// takes no arguments and never touches the store: it renders the field
// shape from the registry. Default output is a human table; --json emits a
// JSON Schema (draft 2020-12).
func newSchemaCmd(e schema.Entity) *cobra.Command {
	return &cobra.Command{
		Use:   "schema",
		Short: fmt.Sprintf("Show the %s field reference", e.Wire),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if jsonOutput {
				return writeJSON(out, e.JSONSchema())
			}
			headers, rows := e.Table()
			fmt.Fprintln(out, hook.FormatTable(headers, rows))
			return nil
		},
	}
}
