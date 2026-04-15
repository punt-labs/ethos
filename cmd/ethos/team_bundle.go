package main

import (
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/punt-labs/ethos/internal/bundle"
	"github.com/punt-labs/ethos/internal/hook"
	"github.com/punt-labs/ethos/internal/resolve"
)

// --- flags ---

var (
	addBundleName   string
	addBundleGlobal bool
	addBundleApply  bool
)

// --- commands ---

var teamAvailableCmd = &cobra.Command{
	Use:   "available",
	Short: "List all discoverable team bundles",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTeamAvailable(cmd)
	},
}

var teamActivateCmd = &cobra.Command{
	Use:   "activate <name>",
	Short: "Set the active team bundle in repo config",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTeamActivate(cmd, args[0])
	},
}

var teamActiveCmd = &cobra.Command{
	Use:   "active",
	Short: "Show the currently active team bundle",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTeamActive(cmd)
	},
}

var teamDeactivateCmd = &cobra.Command{
	Use:   "deactivate",
	Short: "Clear the active team bundle in repo config",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTeamDeactivate(cmd)
	},
}

var teamAddBundleCmd = &cobra.Command{
	Use:   "add-bundle <git-url>",
	Short: "Scaffold a new team bundle from a git URL",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTeamAddBundle(cmd, args[0])
	},
}

func init() {
	teamAddBundleCmd.Flags().StringVar(&addBundleName, "name", "", "Bundle name (defaults to last URL path segment)")
	teamAddBundleCmd.Flags().BoolVar(&addBundleGlobal, "global", false, "Install into ~/.punt-labs/ethos/bundles/ via git clone")
	teamAddBundleCmd.Flags().BoolVar(&addBundleApply, "apply", false, "Execute the git commands (default: dry-run)")

	teamCmd.AddCommand(
		teamAvailableCmd,
		teamActivateCmd,
		teamActiveCmd,
		teamDeactivateCmd,
		teamAddBundleCmd,
	)
}

// --- available ---

// availableRow is a single row of the `team available` table / JSON.
type availableRow struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Path   string `json:"path"`
	Active bool   `json:"active"`
}

func runTeamAvailable(cmd *cobra.Command) error {
	repoRoot := resolve.FindRepoRoot()
	globalRoot := defaultGlobalRoot()

	bundles, err := bundle.List(repoRoot, globalRoot)
	if err != nil {
		return fmt.Errorf("listing bundles: %w", err)
	}

	var activeName string
	if repoRoot != "" {
		activeName, err = resolve.ResolveActiveBundle(repoRoot)
		if err != nil {
			return fmt.Errorf("reading active bundle: %w", err)
		}
	}

	rows := make([]availableRow, 0, len(bundles))
	for _, b := range bundles {
		rows = append(rows, availableRow{
			Name:   b.Name,
			Source: string(b.Source),
			Path:   b.Path,
			Active: activeName != "" && b.Name == activeName && b.Source != bundle.SourceLegacy,
		})
	}

	out := cmd.OutOrStdout()
	if jsonOutput {
		return writeJSON(out, rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "No bundles discovered.")
		return nil
	}
	printAvailableTable(out, rows)
	return nil
}

// printAvailableTable formats rows as a text table via hook.FormatTable.
func printAvailableTable(w io.Writer, rows []availableRow) {
	headers := []string{"NAME", "SOURCE", "PATH", "ACTIVE"}
	data := make([][]string, len(rows))
	for i, r := range rows {
		active := ""
		if r.Active {
			active = "*"
		}
		data[i] = []string{r.Name, r.Source, r.Path, active}
	}
	fmt.Fprintln(w, hook.FormatTable(headers, data))
}

// --- activate ---

func runTeamActivate(cmd *cobra.Command, name string) error {
	if !bundle.ValidName.MatchString(name) {
		return fmt.Errorf("invalid bundle name %q (must match %s)", name, bundle.ValidName.String())
	}
	repoRoot := resolve.FindRepoRoot()
	if repoRoot == "" {
		return fmt.Errorf("not in a git repository")
	}
	globalRoot := defaultGlobalRoot()

	bundles, err := bundle.List(repoRoot, globalRoot)
	if err != nil {
		return fmt.Errorf("listing bundles: %w", err)
	}
	var match *bundle.Bundle
	for i := range bundles {
		if bundles[i].Source == bundle.SourceLegacy {
			continue
		}
		if bundles[i].Name == name {
			match = &bundles[i]
			break
		}
	}
	if match == nil {
		return fmt.Errorf("bundle %q not found; available bundles:\n%s", name, listBundleNames(bundles))
	}

	current, err := resolve.ResolveActiveBundle(repoRoot)
	if err != nil {
		return fmt.Errorf("reading active bundle: %w", err)
	}
	out := cmd.OutOrStdout()
	if current == name {
		if jsonOutput {
			return writeJSON(out, map[string]string{"name": name, "status": "already-active"})
		}
		fmt.Fprintf(out, "bundle %q is already active\n", name)
		return nil
	}

	if err := setConfigKey(repoRoot, "active_bundle", name); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	if jsonOutput {
		return writeJSON(out, availableRow{
			Name:   match.Name,
			Source: string(match.Source),
			Path:   match.Path,
			Active: true,
		})
	}
	fmt.Fprintf(out, "activated: %s (source: %s, path: %s)\n", match.Name, match.Source, match.Path)
	return nil
}

// listBundleNames formats bundle names with their source for error output.
func listBundleNames(bundles []bundle.Bundle) string {
	if len(bundles) == 0 {
		return "  (none)"
	}
	var b strings.Builder
	for _, bn := range bundles {
		if bn.Source == bundle.SourceLegacy {
			continue
		}
		fmt.Fprintf(&b, "  - %s (%s)\n", bn.Name, bn.Source)
	}
	if b.Len() == 0 {
		return "  (none)"
	}
	return strings.TrimRight(b.String(), "\n")
}

// --- active ---

func runTeamActive(cmd *cobra.Command) error {
	repoRoot := resolve.FindRepoRoot()
	globalRoot := defaultGlobalRoot()

	b, err := bundle.ResolveActive(repoRoot, globalRoot)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if jsonOutput {
		if b == nil {
			_, err := fmt.Fprintln(out, "null")
			return err
		}
		return writeJSON(out, availableRow{
			Name:   b.Name,
			Source: string(b.Source),
			Path:   b.Path,
			Active: b.Source != bundle.SourceLegacy,
		})
	}

	if b == nil {
		fmt.Fprintln(out, "(none)")
		return nil
	}
	if b.Source == bundle.SourceLegacy {
		fmt.Fprintln(out, "(legacy)")
		return nil
	}
	fmt.Fprintf(out, "%s (source: %s, path: %s)\n", b.Name, b.Source, b.Path)
	return nil
}

// --- deactivate ---

func runTeamDeactivate(cmd *cobra.Command) error {
	repoRoot := resolve.FindRepoRoot()
	if repoRoot == "" {
		return fmt.Errorf("not in a git repository")
	}
	current, err := resolve.ResolveActiveBundle(repoRoot)
	if err != nil {
		return fmt.Errorf("reading active bundle: %w", err)
	}
	out := cmd.OutOrStdout()
	if current == "" {
		if jsonOutput {
			return writeJSON(out, map[string]string{"status": "no-active-bundle"})
		}
		fmt.Fprintln(out, "no active bundle to deactivate")
		return nil
	}
	if err := setConfigKey(repoRoot, "active_bundle", ""); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	if jsonOutput {
		return writeJSON(out, map[string]string{"deactivated": current})
	}
	fmt.Fprintf(out, "deactivated: was %s\n", current)
	return nil
}

// --- add-bundle ---

// bundleNameFromURL extracts the last path segment from a git URL and
// slug-sanitizes it so it matches bundle.ValidName.
func bundleNameFromURL(url string) string {
	s := strings.TrimSuffix(url, ".git")
	// Split on / and : to handle both HTTPS and SSH URLs.
	for _, sep := range []string{"/", ":"} {
		if i := strings.LastIndex(s, sep); i >= 0 {
			s = s[i+1:]
		}
	}
	s = strings.ToLower(s)
	// Replace disallowed characters with hyphens.
	re := regexp.MustCompile(`[^a-z0-9-]+`)
	s = re.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

func runTeamAddBundle(cmd *cobra.Command, url string) error {
	name := addBundleName
	if name == "" {
		name = bundleNameFromURL(url)
	}
	if !bundle.ValidName.MatchString(name) {
		return fmt.Errorf("invalid bundle name %q (must match %s); use --name to override", name, bundle.ValidName.String())
	}

	var gitArgs []string
	var targetDir string
	if addBundleGlobal {
		globalRoot := defaultGlobalRoot()
		if globalRoot == "" {
			return fmt.Errorf("cannot determine home directory for global install")
		}
		targetDir = filepath.Join(globalRoot, "bundles", name)
		gitArgs = []string{"git", "clone", url, targetDir}
	} else {
		repoRoot := resolve.FindRepoRoot()
		if repoRoot == "" {
			return fmt.Errorf("not in a git repository (use --global to install without a repo)")
		}
		targetDir = filepath.Join(repoRoot, ".punt-labs", "ethos-bundles", name)
		gitArgs = []string{"git", "submodule", "add", url, targetDir}
	}

	out := cmd.OutOrStdout()
	if !addBundleApply {
		if jsonOutput {
			return writeJSON(out, map[string]any{
				"name":    name,
				"target":  targetDir,
				"command": gitArgs,
				"applied": false,
			})
		}
		fmt.Fprintf(out, "dry-run: would run: %s\n", strings.Join(gitArgs, " "))
		fmt.Fprintf(out, "target: %s\n", targetDir)
		fmt.Fprintln(out, "re-run with --apply to execute")
		return nil
	}

	c := exec.Command(gitArgs[0], gitArgs[1:]...)
	c.Stdout = out
	c.Stderr = cmd.ErrOrStderr()
	if err := c.Run(); err != nil {
		return fmt.Errorf("running %s: %w", strings.Join(gitArgs, " "), err)
	}
	if jsonOutput {
		return writeJSON(out, map[string]any{
			"name":    name,
			"target":  targetDir,
			"applied": true,
		})
	}
	fmt.Fprintf(out, "added bundle %q at %s\n", name, targetDir)
	return nil
}
