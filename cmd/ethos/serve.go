package main

import (
	"fmt"
	"os"

	"github.com/punt-labs/ethos/internal/mcp"

	"github.com/mark3labs/mcp-go/server"
)

func runServeImpl() {
	s := server.NewMCPServer(
		"ethos",
		version,
		server.WithToolCapabilities(true),
	)

	mcp.NewHandler(identityStore(), sessionStore()).RegisterTools(s)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: MCP server error: %v\n", err)
		os.Exit(1)
	}
}
