package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"

	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/ui"
	"github.com/spf13/cobra"
)

var uiPort int

var uiCmd = &cobra.Command{
	Use:   "ui",
	Short: "Open the traceability dashboard in a browser",
	Long: `Start a localhost HTTP server that renders mission contracts,
delegation records, and audit trails as an interactive web UI.
Opens the default browser automatically. Ctrl-C stops the server.

The UI is read-only — it never modifies on-disk state.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runUI()
	},
}

func init() {
	uiCmd.Flags().IntVar(&uiPort, "port", 0, "Port to listen on (default: random free port)")
	rootCmd.AddCommand(uiCmd)
}

func runUI() error {
	repoRoot := resolve.EnvRepoRoot()
	if repoRoot == "" {
		return fmt.Errorf("ethos ui must run inside a repo (no .git found)")
	}
	// storeRoot resolves the mission store through the git common dir, so the
	// dashboard shows the main work tree's missions when run from a linked
	// worktree (CR#2). Take it VERBATIM — no fallback to repoRoot. repoRoot
	// (EnvRepoRoot, requireStore=false) would re-accept an ETHOS_REPO_ROOT
	// that StoreRepoRoot deliberately refused (F1 fail-closed), resurrecting
	// the very override the store resolver rejected (Bugbot MED on PR #370,
	// same class as tierBStoreRoot's dropped Getwd fallback). When the store
	// root cannot be resolved — a refused override — fail loud rather than
	// serve a repo-scoped dashboard off a relative or wrong tree.
	storeRoot := resolve.StoreRepoRoot()
	if storeRoot == "" {
		return fmt.Errorf("ethos ui: cannot resolve the mission store root " +
			"(ETHOS_REPO_ROOT was set but refused); unset it or point it at a " +
			"repo with a .punt-labs/ethos store")
	}

	srv, err := ui.NewServer(repoRoot, storeRoot)
	if err != nil {
		return fmt.Errorf("creating UI server: %w", err)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", uiPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	url := fmt.Sprintf("http://%s", ln.Addr())
	fmt.Fprintf(os.Stdout, "ethos ui: %s\n", url)

	// Open browser.
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("open", url).Start()
	case "linux":
		_ = exec.Command("xdg-open", url).Start()
	}

	// Graceful shutdown on Ctrl-C.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		<-sig
		fmt.Fprintln(os.Stderr, "\nethos ui: shutting down")
		_ = ln.Close()
	}()

	if err := http.Serve(ln, srv); err != nil {
		if opErr, ok := err.(*net.OpError); ok && opErr.Op == "accept" {
			return nil // listener closed by signal handler
		}
		return err
	}
	return nil
}
