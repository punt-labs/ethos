package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var extCmd = &cobra.Command{
	Use:     "ext",
	Short:   "Manage tool-scoped extensions on identities",
	GroupID: "identity",
}

var extGetCmd = &cobra.Command{
	Use:   "get <handle> <namespace> [key]",
	Short: "Get extension values",
	Args:  cobra.RangeArgs(2, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runExtGet(cmd, args)
	},
}

var extSetCmd = &cobra.Command{
	Use:   "set <handle> <namespace> <key> <value>...",
	Short: "Set an extension value",
	Args:  cobra.MinimumNArgs(4),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runExtSet(cmd, args)
	},
}

var extDelCmd = &cobra.Command{
	Use:   "del <handle> <namespace> [key]",
	Short: "Delete extension values",
	Args:  cobra.RangeArgs(2, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runExtDel(args)
	},
}

var extListCmd = &cobra.Command{
	Use:   "list <handle>",
	Short: "List extension namespaces for an identity",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runExtList(cmd, args)
	},
}

func init() {
	extCmd.AddCommand(extGetCmd, extSetCmd, extDelCmd, extListCmd)
	rootCmd.AddCommand(extCmd)
}

func runExtGet(cmd *cobra.Command, args []string) error {
	s := globalStore()
	handle := args[0]
	namespace := args[1]
	key := ""
	if len(args) > 2 {
		key = args[2]
	}

	m, err := s.ExtGet(handle, namespace, key)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if jsonOutput {
		return writeJSON(out, m)
	}
	for k, v := range m {
		fmt.Fprintf(out, "%s: %s\n", k, v)
	}
	return nil
}

func runExtSet(cmd *cobra.Command, args []string) error {
	s := globalStore()
	value := strings.Join(args[3:], " ")
	return s.ExtSet(args[0], args[1], args[2], value)
}

func runExtDel(args []string) error {
	s := globalStore()
	key := ""
	if len(args) > 2 {
		key = args[2]
	}
	return s.ExtDel(args[0], args[1], key)
}

func runExtList(cmd *cobra.Command, args []string) error {
	s := globalStore()
	if !s.Exists(args[0]) {
		return fmt.Errorf("handle %q does not exist", args[0])
	}
	namespaces, err := s.ExtList(args[0])
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if jsonOutput {
		if namespaces == nil {
			namespaces = []string{}
		}
		return writeJSON(out, namespaces)
	}
	if len(namespaces) == 0 {
		fmt.Fprintf(out, "No extensions for %q.\n", args[0])
		return nil
	}
	for _, ns := range namespaces {
		fmt.Fprintln(out, ns)
	}
	return nil
}
