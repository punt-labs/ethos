package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/bundle"
	"github.com/punt-labs/ethos/internal/hook"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	setupBundle string
	setupSolo   bool
	setupFile   string
)

var setupCmd = &cobra.Command{
	Use:          "setup",
	Short:        "Set up ethos identities and team for the current repo",
	GroupID:      "admin",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	Example: `  ethos setup                           # interactive wizard
  ethos setup --solo                    # identity only, no team
  ethos setup --bundle gstack           # use gstack instead of foundation
  ethos setup --file config.yaml        # non-interactive, from file`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSetup(cmd)
	},
}

func init() {
	setupCmd.Flags().StringVar(&setupBundle, "bundle", "foundation", "Team bundle to activate")
	setupCmd.Flags().BoolVar(&setupSolo, "solo", false, "Identity only, no team bundle")
	setupCmd.Flags().StringVarP(&setupFile, "file", "f", "", "Create identities from a YAML file (non-interactive)")
	rootCmd.AddCommand(setupCmd)
}

// setupConfig holds the user's answers, whether from interactive prompts
// or from --file.
type setupConfig struct {
	Name         string `yaml:"name"`
	Handle       string `yaml:"handle"`
	WritingStyle string `yaml:"writing_style"`
	Bundle       string `yaml:"bundle"`
	Solo         bool   `yaml:"solo"`
}

// setupResult tracks what was created for --json output.
type setupResult struct {
	HumanIdentity string   `json:"human_identity"`
	AgentIdentity string   `json:"agent_identity"`
	RepoConfig    string   `json:"repo_config,omitempty"`
	Bundle        string   `json:"bundle,omitempty"`
	AgentFiles    []string `json:"agent_files,omitempty"`
	Skipped       []string `json:"skipped"`
}

func runSetup(cmd *cobra.Command) error {
	var cfg setupConfig
	errw := cmd.ErrOrStderr()

	if setupFile != "" {
		data, err := os.ReadFile(setupFile)
		if err != nil {
			return fmt.Errorf("setup: reading %s: %w", setupFile, err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("setup: parsing %s: %w", setupFile, err)
		}
		// CLI flags override file values.
		if setupSolo {
			cfg.Solo = true
		}
		if cmd.Flags().Changed("bundle") {
			cfg.Bundle = setupBundle
		}
		if cfg.Bundle == "" && !cfg.Solo {
			cfg.Bundle = "foundation"
		}
	} else {
		// Interactive mode requires a TTY.
		if !isTTY(os.Stdin) {
			fmt.Fprintln(errw, "ethos: setup requires a terminal (use --file for non-interactive mode)")
			return usageError{}
		}
		var err error
		cfg, err = setupInteractive(cmd)
		if err != nil {
			return err
		}
		cfg.Solo = setupSolo
		if !cfg.Solo {
			cfg.Bundle = setupBundle
		}
	}

	// Validate.
	if cfg.Name == "" {
		return fmt.Errorf("setup: name is required")
	}
	if cfg.Handle == "" {
		cfg.Handle = slugify(cfg.Name)
	}
	if !validSetupHandle(cfg.Handle) {
		return fmt.Errorf("setup: handle %q must be lowercase alphanumeric with hyphens", cfg.Handle)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	store := identity.NewStore(globalRoot)

	result := &setupResult{
		HumanIdentity: cfg.Handle,
		AgentIdentity: "claude",
	}

	// --- Human identity ---
	if store.Exists(cfg.Handle) {
		fmt.Fprintf(errw, "skipped: identity %q already exists\n", cfg.Handle)
		result.Skipped = append(result.Skipped, "human_identity")
	} else {
		human := &identity.Identity{
			Name:         cfg.Name,
			Handle:       cfg.Handle,
			Kind:         "human",
			WritingStyle: cfg.WritingStyle,
			Personality:  "principal-engineer",
		}
		if err := saveIdentityNoRefs(store, human); err != nil {
			return fmt.Errorf("setup: creating human identity: %w", err)
		}
		fmt.Fprintf(errw, "created: identity %q\n", cfg.Handle)
	}

	// --- Agent identity ---
	agentStyle := cfg.WritingStyle
	if agentStyle == "" {
		agentStyle = "concise-quantified"
	}
	if store.Exists("claude") {
		fmt.Fprintf(errw, "skipped: identity %q already exists\n", "claude")
		result.Skipped = append(result.Skipped, "agent_identity")
	} else {
		agent := &identity.Identity{
			Name:         "Claude",
			Handle:       "claude",
			Kind:         "agent",
			WritingStyle: agentStyle,
			Personality:  "principal-engineer",
			Talents:      []string{"engineering"},
		}
		if err := saveIdentityNoRefs(store, agent); err != nil {
			return fmt.Errorf("setup: creating agent identity: %w", err)
		}
		fmt.Fprintf(errw, "created: identity %q\n", "claude")
	}

	// --- Repo config and bundle ---
	repoRoot := resolve.FindRepoRoot()
	if repoRoot == "" {
		fmt.Fprintln(errw, "ethos: setup: not in a git repository (identities created, skipping repo config and team)")
		if result.Skipped == nil {
			result.Skipped = []string{}
		}
		if jsonOutput {
			writeJSON(cmd.OutOrStdout(), result)
		} else {
			printSetupTable(cmd.OutOrStdout(), result)
		}
		return nil
	}

	// Legacy submodule check.
	if hasLegacySubmodule(repoRoot) {
		fmt.Fprintln(errw, "ethos: setup: legacy submodule detected at .punt-labs/ethos/")
		fmt.Fprintln(errw, `Run "ethos team migrate" to convert to the bundles layout.`)
	}

	// Write repo config, merging with any existing values.
	configPath := filepath.Join(repoRoot, ".punt-labs", "ethos.yaml")
	result.RepoConfig = ".punt-labs/ethos.yaml"

	if err := mergeRepoConfig(repoRoot); err != nil {
		return fmt.Errorf("setup: writing repo config: %w", err)
	}
	fmt.Fprintf(errw, "wrote: %s\n", configPath)

	// --- Bundle activation ---
	if cfg.Solo {
		if result.Skipped == nil {
			result.Skipped = []string{}
		}
		if jsonOutput {
			writeJSON(cmd.OutOrStdout(), result)
		} else {
			printSetupTable(cmd.OutOrStdout(), result)
		}
		return nil
	}

	// Validate the bundle exists.
	bundles, err := bundle.List(repoRoot, globalRoot)
	if err != nil {
		return fmt.Errorf("setup: listing bundles: %w", err)
	}
	var match *bundle.Bundle
	for i := range bundles {
		if bundles[i].Source == bundle.SourceLegacy {
			continue
		}
		if bundles[i].Name == cfg.Bundle {
			match = &bundles[i]
			break
		}
	}
	if match == nil {
		return fmt.Errorf("setup: bundle %q not found; available bundles:\n%s", cfg.Bundle, listBundleNames(bundles))
	}

	// Check if already active.
	current, err := resolve.ResolveActiveBundle(repoRoot)
	if err != nil {
		return fmt.Errorf("setup: reading active bundle: %w", err)
	}
	if current == cfg.Bundle {
		fmt.Fprintf(errw, "skipped: bundle %q already active\n", cfg.Bundle)
		result.Skipped = append(result.Skipped, "bundle")
	} else {
		if err := setConfigKey(repoRoot, "active_bundle", cfg.Bundle); err != nil {
			return fmt.Errorf("setup: activating bundle: %w", err)
		}
		if err := setConfigKey(repoRoot, "team", cfg.Bundle); err != nil {
			return fmt.Errorf("setup: setting team: %w", err)
		}
		fmt.Fprintf(errw, "activated: bundle %q\n", cfg.Bundle)
	}
	result.Bundle = cfg.Bundle

	// --- Generate agent files ---
	is := identityStore()
	ts := layeredTeamStore(is)
	rs := layeredRoleStore(is)
	if err := hook.GenerateAgentFiles(repoRoot, is, ts, rs); err != nil {
		fmt.Fprintf(errw, "ethos: setup: generating agent files: %v\n", err)
	}

	// List generated agent files.
	agentsDir := filepath.Join(repoRoot, ".claude", "agents")
	entries, _ := os.ReadDir(agentsDir)
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			result.AgentFiles = append(result.AgentFiles, filepath.Join(".claude", "agents", e.Name()))
		}
	}

	if result.Skipped == nil {
		result.Skipped = []string{}
	}
	if jsonOutput {
		writeJSON(cmd.OutOrStdout(), result)
	} else {
		printSetupTable(cmd.OutOrStdout(), result)
	}
	return nil
}

// setupInteractive runs the 3-question wizard, reading from stdin and
// writing prompts to stderr.
func setupInteractive(cmd *cobra.Command) (setupConfig, error) {
	reader := bufio.NewReader(os.Stdin)
	errw := cmd.ErrOrStderr()
	var cfg setupConfig

	// Prompt 1: Name (required).
	fmt.Fprint(errw, "Your name: ")
	name, _ := reader.ReadString('\n')
	name = strings.TrimSpace(name)
	if name == "" {
		return cfg, fmt.Errorf("setup: name is required")
	}
	cfg.Name = name

	// Prompt 2: Handle (default: slugified name).
	defaultHandle := slugify(name)
	fmt.Fprintf(errw, "Handle [%s]: ", defaultHandle)
	handle, _ := reader.ReadString('\n')
	handle = strings.TrimSpace(handle)
	if handle == "" {
		handle = defaultHandle
	}
	cfg.Handle = handle

	// Prompt 3: Working style (from global store).
	home, err := os.UserHomeDir()
	if err == nil {
		globalRoot := filepath.Join(home, ".punt-labs", "ethos")
		ws := attribute.NewStore(globalRoot, attribute.WritingStyles)
		result, listErr := ws.List()
		if listErr == nil && result != nil && len(result.Attributes) > 0 {
			fmt.Fprintln(errw, "Working style:")
			for i, a := range result.Attributes {
				fmt.Fprintf(errw, "  %d. %s\n", i+1, a.Slug)
			}
			fmt.Fprintln(errw, "  (enter to skip)")
			fmt.Fprint(errw, "Choice: ")
			choice, _ := reader.ReadString('\n')
			choice = strings.TrimSpace(choice)
			if choice != "" {
				cfg.WritingStyle = resolveStyleChoice(choice, result.Attributes)
			}
		}
	}

	return cfg, nil
}

// resolveStyleChoice resolves a numeric index or slug to a writing style slug.
func resolveStyleChoice(choice string, attrs []*attribute.Attribute) string {
	// Try numeric.
	idx := 0
	if _, err := fmt.Sscanf(choice, "%d", &idx); err == nil && idx >= 1 && idx <= len(attrs) {
		return attrs[idx-1].Slug
	}
	// Treat as slug.
	return choice
}

// setupHandleRe matches the same pattern as identity.validHandle:
// lowercase alphanumeric, internal hyphens only, no leading or trailing hyphen.
var setupHandleRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// validSetupHandle checks the handle matches the identity package's pattern.
func validSetupHandle(h string) bool {
	return setupHandleRe.MatchString(h)
}

// mergeRepoConfig writes .punt-labs/ethos.yaml, merging with any existing
// content. Existing keys are never overwritten.
func mergeRepoConfig(repoRoot string) error {
	path := filepath.Join(repoRoot, ".punt-labs", "ethos.yaml")

	// Ensure parent dir exists.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	// Read existing config (if any).
	existing := make(map[string]string)
	data, err := os.ReadFile(path)
	if err == nil && len(data) > 0 {
		var raw map[string]interface{}
		if err := yaml.Unmarshal(data, &raw); err == nil {
			for k, v := range raw {
				if s, ok := v.(string); ok {
					existing[k] = s
				}
			}
		}
	}

	// Merge: add missing keys only. Bundle-related keys (active_bundle, team)
	// are written by runSetup's bundle activation section, not here, so the
	// idempotency check can distinguish first-run from re-run.
	if _, ok := existing["agent"]; !ok {
		if err := setConfigKey(repoRoot, "agent", "claude"); err != nil {
			return err
		}
	}
	return nil
}

// saveIdentityNoRefs writes an identity YAML file, skipping attribute
// ref validation. Setup runs in a bootstrapping context where the
// referenced personality or writing style may not exist in the store
// yet (e.g. principal-engineer is a convention, not a seeded file).
// Structural validation (name, handle, kind) is still enforced.
func saveIdentityNoRefs(store *identity.Store, id *identity.Identity) error {
	if err := id.Validate(); err != nil {
		return err
	}
	dir := store.IdentitiesDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating identity directory: %w", err)
	}
	data, err := yaml.Marshal(id)
	if err != nil {
		return fmt.Errorf("marshaling identity: %w", err)
	}
	path := store.Path(id.Handle)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("identity %q already exists", id.Handle)
		}
		return fmt.Errorf("creating identity file: %w", err)
	}
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	return os.MkdirAll(store.ExtDir(id.Handle), 0o700)
}

// isTTY reports whether f is connected to a terminal.
func isTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// hasLegacySubmodule checks if .punt-labs/ethos/ exists as a directory
// and has a .gitmodules entry pointing to it.
func hasLegacySubmodule(repoRoot string) bool {
	dir := filepath.Join(repoRoot, ".punt-labs", "ethos")
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	url, err := legacySubmoduleURL(repoRoot)
	return err == nil && url != ""
}

// usageError is returned for exit-code-2 conditions (no TTY, bad flags).
type usageError struct{}

func (usageError) Error() string { return "" }

// printSetupTable prints a summary table of created items (non-JSON mode).
func printSetupTable(w io.Writer, result *setupResult) {
	headers := []string{"ITEM", "STATUS"}
	var rows [][]string
	humanStatus := "created"
	if contains(result.Skipped, "human_identity") {
		humanStatus = "skipped (exists)"
	}
	rows = append(rows, []string{"human identity: " + result.HumanIdentity, humanStatus})

	agentStatus := "created"
	if contains(result.Skipped, "agent_identity") {
		agentStatus = "skipped (exists)"
	}
	rows = append(rows, []string{"agent identity: " + result.AgentIdentity, agentStatus})

	if result.RepoConfig != "" {
		rows = append(rows, []string{"repo config: " + result.RepoConfig, "wrote"})
	}
	if result.Bundle != "" {
		bundleStatus := "activated"
		if contains(result.Skipped, "bundle") {
			bundleStatus = "skipped (active)"
		}
		rows = append(rows, []string{"bundle: " + result.Bundle, bundleStatus})
	}
	for _, f := range result.AgentFiles {
		rows = append(rows, []string{"agent file: " + f, "generated"})
	}
	fmt.Fprintln(w, hook.FormatTable(headers, rows))
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
