package main

import (
	"fmt"
	"os"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/session"
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

// sessionStore returns the default session store rooted at the same
// location as the identity store.
func sessionStore() *session.Store {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}
	return session.NewStore(home + "/.punt-labs/ethos")
}
