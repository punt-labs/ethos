package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/bundle"
	"github.com/punt-labs/ethos/internal/hook"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

var (
	setupBundle string
	setupSolo   bool
	setupFile   string
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Set up ethos identities and team for the current repo",
	Long: `Set up ethos identities and team for the current repo.

For fresh installs, run 'ethos seed' first to deploy starter content
to global directories.`,
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
	HumanIdentity   string   `json:"human_identity"`
	AgentIdentity   string   `json:"agent_identity"`
	RepoConfig      string   `json:"repo_config,omitempty"`
	Bundle          string   `json:"bundle,omitempty"`
	AgentFiles      []string `json:"agent_files,omitempty"`
	AgentFilesError string   `json:"agent_files_error,omitempty"`
	Skipped         []string `json:"skipped"`
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

	// Fast-fail on the required name before any I/O. Handle format and
	// kind are validated by identity.Validate at Save time (below), the
	// single source of structural validation shared with `ethos create`.
	if cfg.Name == "" {
		return fmt.Errorf("setup: name is required")
	}
	if cfg.Handle == "" {
		cfg.Handle = slugify(cfg.Name)
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
		if err := human.Validate(); err != nil {
			return fmt.Errorf("setup: creating human identity: %w", err)
		}
		if err := store.Save(human); err != nil {
			return setupSaveError("human", human, globalRoot, err)
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
		if err := agent.Validate(); err != nil {
			return fmt.Errorf("setup: creating agent identity: %w", err)
		}
		if err := store.Save(agent); err != nil {
			return setupSaveError("agent", agent, globalRoot, err)
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
			if err := writeJSON(cmd.OutOrStdout(), result); err != nil {
				return fmt.Errorf("setup: writing JSON output: %w", err)
			}
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
			if err := writeJSON(cmd.OutOrStdout(), result); err != nil {
				return fmt.Errorf("setup: writing JSON output: %w", err)
			}
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

	// Check if already active. If --bundle wasn't explicit and a bundle
	// is already active, keep the existing one.
	current, err := resolve.ResolveActiveBundle(repoRoot)
	if err != nil {
		return fmt.Errorf("setup: reading active bundle: %w", err)
	}
	team, err := resolve.ResolveTeam(repoRoot)
	if err != nil {
		return fmt.Errorf("setup: reading team: %w", err)
	}
	switch {
	case current != "" && !cmd.Flags().Changed("bundle"):
		fmt.Fprintf(errw, "skipped: bundle %q already active (use --bundle to switch)\n", current)
		result.Skipped = append(result.Skipped, "bundle")
		cfg.Bundle = current
		if err := ensureTeamKey(errw, repoRoot, cfg.Bundle, team); err != nil {
			return err
		}
	case current == cfg.Bundle:
		fmt.Fprintf(errw, "skipped: bundle %q already active\n", cfg.Bundle)
		result.Skipped = append(result.Skipped, "bundle")
		if err := ensureTeamKey(errw, repoRoot, cfg.Bundle, team); err != nil {
			return err
		}
	default:
		// Write team FIRST so active_bundle — the idempotency sentinel the
		// skip branches above key off — lands last. If the run is
		// interrupted between the two writes, the next setup sees no
		// active_bundle, takes this branch again, and self-heals.
		if err := setConfigKey(repoRoot, "team", cfg.Bundle); err != nil {
			return fmt.Errorf("setup: setting team: %w", err)
		}
		if err := setConfigKey(repoRoot, "active_bundle", cfg.Bundle); err != nil {
			return fmt.Errorf("setup: activating bundle: %w", err)
		}
		fmt.Fprintf(errw, "activated: bundle %q\n", cfg.Bundle)
	}
	result.Bundle = cfg.Bundle

	// --- Generate agent files ---
	is := identityStore()
	ts := layeredTeamStore(is)
	rs := layeredRoleStore(is)
	if err := hook.GenerateAgentFiles(repoRoot, is, ts, rs); err != nil {
		return fmt.Errorf("setup: generating agent files: %w", err)
	}

	// List generated agent files. Generation already succeeded above; if
	// the enumeration read fails, record an explicit marker so --json does
	// not imply nothing was generated.
	agentsDir := filepath.Join(repoRoot, ".claude", "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil && !os.IsNotExist(err) {
		msg := fmt.Sprintf("reading agent files: %v", err)
		fmt.Fprintf(errw, "ethos: setup: %s\n", msg)
		result.AgentFilesError = msg
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			result.AgentFiles = append(result.AgentFiles, filepath.Join(".claude", "agents", e.Name()))
		}
	}

	if result.Skipped == nil {
		result.Skipped = []string{}
	}
	if jsonOutput {
		if err := writeJSON(cmd.OutOrStdout(), result); err != nil {
			return fmt.Errorf("setup: writing JSON output: %w", err)
		}
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
	styles := writingStyleMenu(errw)
	if len(styles) > 0 {
		fmt.Fprintln(errw, "Working style:")
		for i, a := range styles {
			fmt.Fprintf(errw, "  %d. %s\n", i+1, a.Slug)
		}
		fmt.Fprintln(errw, "  (enter to skip)")
		fmt.Fprint(errw, "Choice: ")
		choice, _ := reader.ReadString('\n')
		choice = strings.TrimSpace(choice)
		if choice != "" {
			cfg.WritingStyle = resolveStyleChoice(choice, styles)
		}
	}

	return cfg, nil
}

// writingStyleMenu loads the writing-style choices for the wizard,
// surfacing — not swallowing — any load error or per-entry warning to
// errw. A user who sees no styles must be able to tell an empty store
// from one that failed to read. Returns nil when the menu is unavailable.
func writingStyleMenu(errw io.Writer) []*attribute.Attribute {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(errw, "ethos: setup: cannot locate home directory, skipping style prompt: %v\n", err)
		return nil
	}
	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	result, err := attribute.NewStore(globalRoot, attribute.WritingStyles).List()
	if err != nil {
		fmt.Fprintf(errw, "ethos: setup: cannot list writing styles, skipping style prompt: %v\n", err)
		return nil
	}
	for _, w := range result.Warnings {
		fmt.Fprintf(errw, "ethos: warning: %s\n", w)
	}
	return result.Attributes
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
			// A present-but-non-string agent key is malformed. Surface it
			// rather than treating it as absent and clobbering it — the
			// merge contract is "existing keys are never overwritten".
			if v, present := raw["agent"]; present {
				if _, ok := v.(string); !ok {
					return fmt.Errorf("setup: repo config key \"agent\" has a non-string value (%T); fix .punt-labs/ethos.yaml by hand", v)
				}
			}
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

// ensureTeamKey repairs a missing or stale team key. active_bundle is the
// idempotency sentinel setup keys off, but an interrupted activation or an
// `ethos team bundle use` (which writes only active_bundle) can leave the
// team key absent — SessionStart then injects no team context with no other
// signal. Whenever a bundle is active, the team key must match it.
func ensureTeamKey(errw io.Writer, repoRoot, bundle, currentTeam string) error {
	if bundle == "" || currentTeam == bundle {
		return nil
	}
	if err := setConfigKey(repoRoot, "team", bundle); err != nil {
		return fmt.Errorf("setup: repairing team key: %w", err)
	}
	fmt.Fprintf(errw, "repaired: team %q (was missing or stale)\n", bundle)
	return nil
}

// setupSaveError translates a Store.Save failure into an actionable
// setup error. When the failure is a missing attribute reference — the
// case where setup ran before seed — it names the missing slug and the
// remedy. Other failures (structural, already-exists) pass through with
// context.
func setupSaveError(kind string, id *identity.Identity, globalRoot string, err error) error {
	var ve *identity.ValidationError
	if errors.As(err, &ve) {
		if slug := missingRefSlug(ve, id, globalRoot); slug != "" {
			return fmt.Errorf("setup: identity %q references %s %q, which is not installed; run \"ethos seed\" first",
				id.Handle, refNoun(ve.Field), slug)
		}
	}
	return fmt.Errorf("setup: creating %s identity: %w", kind, err)
}

// missingRefSlug returns the referenced slug that failed validation
// because it is absent from the global store. It returns "" when the
// failure is not a missing-attribute case (e.g. a malformed slug), so
// the caller falls back to the underlying error.
func missingRefSlug(ve *identity.ValidationError, id *identity.Identity, globalRoot string) string {
	switch ve.Field {
	case "personality":
		return missingSlug(globalRoot, attribute.Personalities, id.Personality)
	case "writing_style":
		return missingSlug(globalRoot, attribute.WritingStyles, id.WritingStyle)
	case "talents":
		for _, s := range id.Talents {
			if m := missingSlug(globalRoot, attribute.Talents, s); m != "" {
				return m
			}
		}
	}
	return ""
}

// missingSlug returns slug if it is a well-formed slug that does not
// exist in the given store, otherwise "".
func missingSlug(root string, kind attribute.Kind, slug string) string {
	if slug == "" || attribute.ValidateSlug(slug) != nil {
		return ""
	}
	if attribute.NewStore(root, kind).Exists(slug) {
		return ""
	}
	return slug
}

// refNoun maps an identity attribute field to a human-readable noun.
func refNoun(field string) string {
	switch field {
	case "writing_style":
		return "writing style"
	case "talents":
		return "talent"
	default:
		return field
	}
}

// isTTY reports whether f is connected to a terminal.
func isTTY(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
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
