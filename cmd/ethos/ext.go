package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var extCmd = &cobra.Command{
	Use:     "ext",
	Short:   "Manage tool-scoped extensions on identities",
	GroupID: "identity",
}

var extGetCmd = &cobra.Command{
	Use:   "get <persona> <namespace> [key]",
	Short: "Get extension values",
	Args:  cobra.RangeArgs(2, 3),
	Run: func(cmd *cobra.Command, args []string) {
		runExtGet(args)
	},
}

var extSetCmd = &cobra.Command{
	Use:   "set <persona> <namespace> <key> <value>...",
	Short: "Set an extension value",
	Args:  cobra.MinimumNArgs(4),
	Run: func(cmd *cobra.Command, args []string) {
		runExtSet(args)
	},
}

var extDelCmd = &cobra.Command{
	Use:   "del <persona> <namespace> [key]",
	Short: "Delete extension values",
	Args:  cobra.RangeArgs(2, 3),
	Run: func(cmd *cobra.Command, args []string) {
		runExtDel(args)
	},
}

var extListCmd = &cobra.Command{
	Use:   "list <persona>",
	Short: "List extension namespaces for a persona",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runExtList(args)
	},
}

func init() {
	extCmd.AddCommand(extGetCmd, extSetCmd, extDelCmd, extListCmd)
	rootCmd.AddCommand(extCmd)
}

func runExtGet(args []string) {
	s := globalStore()
	persona := args[0]
	namespace := args[1]
	key := ""
	if len(args) > 2 {
		key = args[2]
	}

	m, err := s.ExtGet(persona, namespace, key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		printJSON(m)
		return
	}
	for k, v := range m {
		fmt.Printf("%s: %s\n", k, v)
	}
}

func runExtSet(args []string) {
	s := globalStore()
	value := strings.Join(args[3:], " ")
	if err := s.ExtSet(args[0], args[1], args[2], value); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
}

func runExtDel(args []string) {
	s := globalStore()
	key := ""
	if len(args) > 2 {
		key = args[2]
	}
	if err := s.ExtDel(args[0], args[1], key); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
}

func runExtList(args []string) {
	s := globalStore()
	if !s.Exists(args[0]) {
		fmt.Fprintf(os.Stderr, "ethos: persona %q does not exist\n", args[0])
		os.Exit(1)
	}
	namespaces, err := s.ExtList(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		if namespaces == nil {
			namespaces = []string{}
		}
		printJSON(namespaces)
		return
	}
	if len(namespaces) == 0 {
		fmt.Printf("No extensions for %q.\n", args[0])
		return
	}
	for _, ns := range namespaces {
		fmt.Println(ns)
	}
}
