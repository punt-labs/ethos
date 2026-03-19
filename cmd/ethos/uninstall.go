package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// pluginID matches the install.sh PLUGIN_NAME@MARKETPLACE_NAME and --scope user.
const pluginID = "ethos@punt-labs"

func runUninstall(args []string) {
	purge := false
	for _, a := range args {
		if a == "--purge" {
			purge = true
		}
	}

	// Step 1: Remove Claude Code plugin.
	pluginFailed := false
	claude, err := exec.LookPath("claude")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ethos: claude CLI not found — skipping plugin removal")
	} else {
		cmd := exec.Command(claude, "plugin", "uninstall", pluginID, "--scope", "user")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "ethos: plugin uninstall failed: %v\n", err)
			pluginFailed = true
		} else {
			fmt.Println("Removed Claude Code plugin.")
		}
	}

	if !purge {
		if pluginFailed {
			os.Exit(1)
		}
		fmt.Println("\nPlugin removed. Binary and identity data are still present.")
		fmt.Println("Run 'ethos uninstall --purge' to remove everything.")
		return
	}

	// Step 2 (purge): Confirm before deleting data.
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}
	dataDir := filepath.Join(home, ".punt-labs", "ethos")

	bin, binErr := resolvedBinaryPath()
	fmt.Printf("\nThis will permanently delete:\n")
	fmt.Printf("  %s   (all identities, sessions, config)\n", dataDir)
	if binErr == nil {
		fmt.Printf("  %s\n", bin)
	}
	fmt.Print("\nType 'yes' to confirm: ")

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "yes" {
		fmt.Println("Aborted.")
		os.Exit(1)
	}

	// Step 3: Remove data directory.
	if err := os.RemoveAll(dataDir); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: failed to remove %s: %v\n", dataDir, err)
	} else {
		fmt.Printf("Removed %s\n", dataDir)
	}

	// Step 4: Remove binary.
	if binErr != nil {
		fmt.Fprintln(os.Stderr, "ethos: cannot determine binary path — remove manually")
	} else if err := os.Remove(bin); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: failed to remove %s: %v\n", bin, err)
	} else {
		fmt.Printf("Removed %s\n", bin)
	}

	fmt.Println("\nethos is uninstalled.")
}

// resolvedBinaryPath returns the absolute, symlink-resolved path of the running executable.
func resolvedBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot determine executable path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return exe, nil
	}
	return resolved, nil
}
