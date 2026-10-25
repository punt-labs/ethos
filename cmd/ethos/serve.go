package main

import (
	"fmt"
	"os"

	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/adr"
	"github.com/punt-labs/ethos/v4/internal/attribute"
	"github.com/punt-labs/ethos/v4/internal/mcp"

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
	loc := locateServeStore()
	if loc.Err != nil {
		fmt.Fprintf(os.Stderr, "ethos serve: %v; write tools will refuse until it is repaired\n", loc.Err)
	}
	h := buildServeHandler(home, loc)
	// The store is located again on every tool call, and the stores rebuilt
	// when the answer changes: repairing .git, entering a repository, or a
	// store created by enable or setup takes effect without a restart.
	h.RelocateStore(loc, locateServeStore, func(loc mcp.StoreLocation) *mcp.Handler {
		return buildServeHandler(home, loc)
	})
	h.RegisterTools(s)

	if err := server.ServeStdio(s); err != nil {
		return fmt.Errorf("MCP server error: %w", err)
	}
	return nil
}

// locateServeStore is where the repository is for the working directory now,
// located once (locateRoots): the store root, its .punt-labs/ethos, the
// checkout, and why any of them cannot be located. Outside any repository the
// roots are "" and there is no error; any other failure — a dangling store
// symlink included — is the error.
func locateServeStore() mcp.StoreLocation {
	r := locateRoots()
	return mcp.StoreLocation{
		Root: r.Store, EthosRoot: r.Ethos, Checkout: r.Checkout,
		Err: firstError(r.StoreErr, r.EthosErr, r.CheckoutErr),
	}
}

// buildServeHandler builds the MCP handler with every store built from loc,
// the one location it was triggered with: nothing here locates again. A
// non-nil loc.Err makes the guarded tools refuse writes and note that reads
// come from the global store only; ethos serve is a command that handles the
// error itself, so its constructors take the global stores for it.
func buildServeHandler(home string, loc mcp.StoreLocation) *mcp.Handler {
	storeErr := loc.Err
	cmdRoots.Store(&repoRoots{Store: loc.Root, Ethos: loc.EthosRoot, Checkout: loc.Checkout, StoreErr: loc.Err, Global: true})
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
