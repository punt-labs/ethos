package main

import (
	"fmt"
	"os"

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
	mcp.NewHandlerWithOptions(is, talents, personalities, writingStyles,
		mcp.WithSessionStore(sessionStore()),
		mcp.WithRoleStore(roles),
		mcp.WithTeamStore(teams),
	).RegisterTools(s)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: MCP server error: %v\n", err)
		os.Exit(1)
	}
}
