package main

import (
	"fmt"
	"os"

	"github.com/punt-labs/ethos/internal/identity"
)

// store returns the default identity store.
// Exits with an error if the home directory cannot be determined.
// Use only from CLI commands, not MCP handlers.
func store() *identity.Store {
	s, err := identity.DefaultStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	return s
}
