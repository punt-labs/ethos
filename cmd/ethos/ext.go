package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/identity"
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

// extLocal is set by --local on set and delete. It selects the
// <ns>.local.yaml companion — the untracked half, for secret and
// machine-specific values (DES-057 Part C).
var extLocal bool

var extSetCmd = &cobra.Command{
	Use:   "set <handle> <namespace> <key> <value>...",
	Short: "Set an extension value",
	Args:  cobra.MinimumNArgs(4),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runExtSet(cmd, args)
	},
}

var extDeleteCmd = &cobra.Command{
	Use:   "delete <handle> <namespace> [key]",
	Short: "Delete extension values",
	Args:  cobra.RangeArgs(2, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runExtDel(cmd, args)
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
	const localUsage = "Target the untracked <namespace>.local.yaml companion instead of the base file"
	extSetCmd.Flags().BoolVar(&extLocal, "local", false, localUsage)
	extDeleteCmd.Flags().BoolVar(&extLocal, "local", false, localUsage)
	extCmd.AddCommand(extGetCmd, extSetCmd, extDeleteCmd, extListCmd)
	rootCmd.AddCommand(extCmd)
}

func runExtGet(cmd *cobra.Command, args []string) error {
	s := identityStore()
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
	s := identityStore()
	value := strings.Join(args[3:], " ")
	return s.ExtSet(args[0], args[1], args[2], value, identity.Local(extLocal))
}

func runExtDel(_ *cobra.Command, args []string) error {
	s := identityStore()
	key := ""
	if len(args) > 2 {
		key = args[2]
	}
	return s.ExtDel(args[0], args[1], key, identity.Local(extLocal))
}

func runExtList(cmd *cobra.Command, args []string) error {
	s := identityStore()
	// A boolean existence probe cannot distinguish absence from a rejected
	// owner. Check without resolving unrelated attributes or migrating voice;
	// listing extensions must remain read-only.
	if _, err := s.Load(args[0], identity.Reference(true), identity.WithoutMigration()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("handle %q does not exist", args[0])
		}
		return err
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
