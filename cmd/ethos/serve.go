package main

import (
	"fmt"
	"os"

	"path/filepath"

	"github.com/punt-labs/ethos/internal/adr"
	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/mcp"

	"github.com/mark3labs/mcp-go/server"
)

func runServeImpl() {
	s := server.NewMCPServer(
		"ethos",
		version,
		server.WithToolCapabilities(true),
	)

	is := identityStore()
	talents := layeredAttributeStore(is, attribute.Talents)
	personalities := layeredAttributeStore(is, attribute.Personalities)
	writingStyles := layeredAttributeStore(is, attribute.WritingStyles)
	roles := layeredRoleStore(is)
	teams := layeredTeamStore(is)
	// MCP shares one Store instance across every mission tool method
	// (create, list, show, close, reflect, advance, reflections). A
	// `mission create` call made via MCP must see the same role-
	// overlap gate as the CLI, so wire the lister here — the read-only
	// methods don't use it, but they still resolve against the same
	// store.
	missions := missionStoreForCreate()
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}
	adrs := adr.NewStore(filepath.Join(home, ".punt-labs", "ethos", "adrs"))
	mcp.NewHandlerWithOptions(is, talents, personalities, writingStyles,
		mcp.WithSessionStore(sessionStore()),
		mcp.WithRoleStore(roles),
		mcp.WithTeamStore(teams),
		mcp.WithMissionStore(missions),
		mcp.WithADRStore(adrs),
	).RegisterTools(s)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: MCP server error: %v\n", err)
		os.Exit(1)
	}
}
