package main

import (
	"fmt"
	"os"
)

func runExt(args []string) {
	if len(args) == 0 {
		printSubcommandHelp("ext")
		os.Exit(1)
	}

	sub := args[0]
	subArgs := args[1:]

	switch sub {
	case "get":
		runExtGet(subArgs)
	case "set":
		runExtSet(subArgs)
	case "del":
		runExtDel(subArgs)
	case "list":
		runExtList(subArgs)
	default:
		fmt.Fprintf(os.Stderr, "ethos ext: unknown subcommand %q\n", sub)
		printSubcommandHelp("ext")
		os.Exit(1)
	}
}

func runExtGet(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: ethos ext get <persona> <namespace> [key]")
		os.Exit(1)
	}
	s := store()
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
	if len(args) < 4 {
		fmt.Fprintln(os.Stderr, "Usage: ethos ext set <persona> <namespace> <key> <value>")
		os.Exit(1)
	}
	s := store()
	if err := s.ExtSet(args[0], args[1], args[2], args[3]); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
}

func runExtDel(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: ethos ext del <persona> <namespace> [key]")
		os.Exit(1)
	}
	s := store()
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
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: ethos ext list <persona>")
		os.Exit(1)
	}
	s := store()
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
