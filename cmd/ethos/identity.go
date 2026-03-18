package main

import (
	"fmt"
	"os"

	"github.com/punt-labs/ethos/internal/identity"
)

// store returns the default identity store.
// Exits the process on failure — acceptable at startup but not inside
// request handlers. MCP handlers receive the Store via Handler injection.
func store() *identity.Store {
	s, err := identity.DefaultStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	return s
}
