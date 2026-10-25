package main

import (
	"errors"
	"fmt"
	"os"

	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/adr"
	"github.com/punt-labs/ethos/v4/internal/attribute"
	"github.com/punt-labs/ethos/v4/internal/mcp"
	"github.com/punt-labs/ethos/v4/internal/resolve"

	"github.com/mark3labs/mcp-go/server"
)

func runServeImpl() error {
	s := server.NewMCPServer(
		"ethos",
		// resolveVersion, not the raw `version` var: `version` is only
		// non-empty when the Makefile's ldflags set it (main.go,
		// ethos-vqbk), and this server is exactly as reachable from a
		// `go install`-built binary as `ethos version` is.
		resolveVersion(),
		server.WithToolCapabilities(true),
	)

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}
	storeErr := locateServeStore()
	if storeErr != nil {
		fmt.Fprintf(os.Stderr, "ethos serve: %v; write tools will refuse until it is repaired\n", storeErr)
	}
	h := buildServeHandler(home, storeErr)
	// The store is located again on every tool call, and the stores rebuilt
	// when the answer changes: repairing .git takes effect without a restart.
	h.RelocateStore(locateServeStore, func(storeErr error) *mcp.Handler {
		return buildServeHandler(home, storeErr)
	})
	h.RegisterTools(s)

	if err := server.ServeStdio(s); err != nil {
		return fmt.Errorf("MCP server error: %w", err)
	}
	return nil
}

// locateServeStore is why the repository store cannot be located, or nil when
// it is located or the server runs outside any repository.
func locateServeStore() error {
	if _, err := resolve.LocateStoreRoot(); err != nil && !errors.Is(err, resolve.ErrNotInRepo) {
		return err
	}
	return nil
}

// buildServeHandler builds the MCP handler with every store resolved for the
// current working directory. A non-nil storeErr makes the guarded tools
// refuse writes and note that reads come from the global store only.
func buildServeHandler(home string, storeErr error) *mcp.Handler {
	is := identityStore()
	talents := layeredAttributeStore(is, attribute.Talents)
	personalities := layeredAttributeStore(is, attribute.Personalities)
	writingStyles := layeredAttributeStore(is, attribute.WritingStyles)
	// MCP shares one Store instance across every mission tool method
	// (create, list, show, close, reflect, advance, reflections). A
	// `mission create` call made via MCP must see the same role-
	// overlap gate as the CLI, so wire the lister here — the read-only
	// methods don't use it, but they still resolve against the same
	// store.
	opts := []mcp.Option{
		mcp.WithSessionStore(sessionStore()),
		mcp.WithRoleStore(layeredRoleStore(is)),
		mcp.WithTeamStore(layeredTeamStore(is)),
		mcp.WithMissionStore(missionStoreForCreate()),
		mcp.WithADRStore(adr.NewStore(filepath.Join(home, ".punt-labs", "ethos", "adrs"))),
		// Vendor gets its own full-chain stores for the same reason the
		// CLI does: it copies from global into the repo layer.
		mcp.WithVendorRunner(vendorRunner()),
	}
	if storeErr != nil {
		opts = append(opts, mcp.WithStoreError(storeErr))
	}
	return mcp.NewHandlerWithOptions(is, talents, personalities, writingStyles, opts...)
}
