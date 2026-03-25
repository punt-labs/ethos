package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/internal/hook"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/process"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/punt-labs/ethos/internal/session"
	"github.com/spf13/cobra"
)

var version = "dev"

// jsonOutput is set by the --json persistent flag on rootCmd.
var jsonOutput bool

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// printJSON marshals v to stdout. Exits on error.
func printJSON(v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(data))
}

// --- version ---

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		if jsonOutput {
			printJSON(map[string]string{"version": version})
		} else {
			fmt.Printf("ethos %s\n", version)
		}
	},
}

// --- doctor ---

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check installation health",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runDoctor()
	},
}

// --- whoami ---

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Show the caller's identity",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runWhoami()
	},
}

// --- show ---

var showReference bool

var showCmd = &cobra.Command{
	Use:   "show <handle>",
	Short: "Show identity details",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runShow(args[0], showReference)
	},
}

// --- list ---

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all identities",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runList()
	},
}

// --- resolve-agent ---

var resolveAgentCmd = &cobra.Command{
	Use:    "resolve-agent",
	Short:  "Show default agent from repo config",
	Args:   cobra.NoArgs,
	Hidden: true,
	Run: func(cmd *cobra.Command, args []string) {
		runResolveAgent()
	},
}

// --- iam ---

var iamCmd = &cobra.Command{
	Use:   "iam <persona>",
	Short: "Declare persona in current session",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runIam(args[0])
	},
}

// --- serve ---

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start MCP server (stdio transport)",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runServeImpl()
	},
}

func init() {
	showCmd.Flags().BoolVar(&showReference, "reference", false, "Include reference identity data")

	rootCmd.AddCommand(
		versionCmd,
		doctorCmd,
		whoamiCmd,
		showCmd,
		listCmd,
		resolveAgentCmd,
		iamCmd,
		serveCmd,
	)
}

func runDoctor() {
	is := identityStore()
	ss := sessionStore()

	type checkResult struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Detail string `json:"detail"`
	}

	checks := []struct {
		name string
		fn   func(identity.IdentityStore, *session.Store) (string, bool)
	}{
		{"Identity directory", checkIdentityDir},
		{"Human identity", checkHumanIdentity},
		{"Default agent", checkDefaultAgent},
		{"Duplicate fields", checkDuplicateFields},
	}

	allPassed := true
	var results []checkResult
	for _, c := range checks {
		detail, ok := c.fn(is, ss)
		status := "PASS"
		if !ok {
			status = "FAIL"
			allPassed = false
		}
		results = append(results, checkResult{Name: c.name, Status: status, Detail: detail})
	}

	if jsonOutput {
		printJSON(results)
	} else {
		for _, r := range results {
			fmt.Printf("  %-24s %s  %s\n", r.Name, r.Status, r.Detail)
		}
	}

	if !allPassed {
		os.Exit(1)
	}
}

func checkIdentityDir(s identity.IdentityStore, _ *session.Store) (string, bool) {
	dir := s.IdentitiesDir()
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("not found: %s", dir), false
		}
		return fmt.Sprintf("error: %v", err), false
	}
	return dir, true
}

func checkHumanIdentity(s identity.IdentityStore, ss *session.Store) (string, bool) {
	handle, err := resolve.Resolve(s, ss)
	if err != nil {
		return fmt.Sprintf("no match — %v", err), false
	}
	id, err := s.Load(handle, identity.Reference(true))
	if err != nil {
		return fmt.Sprintf("handle %q not loadable: %v", handle, err), false
	}
	return fmt.Sprintf("%s (%s)", id.Name, id.Handle), true
}

func checkDefaultAgent(s identity.IdentityStore, _ *session.Store) (string, bool) {
	repoRoot := resolve.FindRepoRoot()
	if repoRoot == "" {
		return "not in a git repo", true
	}
	handle := resolve.ResolveAgent(repoRoot)
	if handle == "" {
		return "not configured", true
	}
	return handle, true
}

func checkDuplicateFields(s identity.IdentityStore, _ *session.Store) (string, bool) {
	result, err := s.List()
	if err != nil {
		return fmt.Sprintf("error: %v", err), false
	}
	var dupes []string
	seen := map[string]map[string]string{
		"github": {},
		"email":  {},
	}
	for _, id := range result.Identities {
		for field, values := range seen {
			var val string
			switch field {
			case "github":
				val = id.GitHub
			case "email":
				val = id.Email
			}
			if val == "" {
				continue
			}
			if prev, ok := values[val]; ok {
				dupes = append(dupes, fmt.Sprintf("%s %q: %s and %s", field, val, prev, id.Handle))
			} else {
				values[val] = id.Handle
			}
		}
	}
	if len(dupes) > 0 {
		return "duplicates found: " + strings.Join(dupes, "; "), false
	}
	return "no duplicates", true
}

func runWhoami() {
	is := identityStore()
	ss := sessionStore()

	handle, err := resolve.Resolve(is, ss)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}

	id, err := is.Load(handle)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: identity %q not found: %v\n", handle, err)
		os.Exit(1)
	}

	if jsonOutput {
		printJSON(id)
	} else {
		fmt.Printf("%s (%s)\n", id.Name, id.Handle)
	}
}

func runResolveAgent() {
	repoRoot := resolve.FindRepoRoot()
	handle := resolve.ResolveAgent(repoRoot)
	if handle != "" {
		fmt.Println(handle)
	}
}

func runList() {
	is := identityStore()
	result, err := is.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "ethos: %s\n", w)
	}
	if jsonOutput {
		ids := result.Identities
		if ids == nil {
			ids = []*identity.Identity{}
		}
		printJSON(ids)
		return
	}
	if len(result.Identities) == 0 {
		fmt.Println("No identities found. Run 'ethos create' to create one.")
		return
	}

	// Build columnar table: HANDLE, NAME, KIND, PERSONALITY, ACTIVE.
	activeHandles := sessionParticipantHandles()

	headers := []string{"HANDLE", "NAME", "KIND", "PERSONALITY", "ACTIVE"}
	rows := make([][]string, len(result.Identities))
	for i, id := range result.Identities {
		personality := id.Personality
		if personality == "" {
			personality = "-"
		}
		marker := "-"
		if activeHandles[id.Handle] {
			marker = "*"
		}
		rows[i] = []string{id.Handle, id.Name, id.Kind, personality, marker}
	}

	fmt.Println(hook.FormatTable(headers, rows))
}

// sessionParticipantHandles returns the set of persona handles that are
// active in the current session. Returns an empty map if no session.
func sessionParticipantHandles() map[string]bool {
	handles := make(map[string]bool)
	ss := sessionStore()
	pid := process.FindClaudePID()
	sessionID, err := ss.ReadCurrentSession(pid)
	if err != nil {
		return handles
	}
	roster, err := ss.Load(sessionID)
	if err != nil {
		return handles
	}
	for _, p := range roster.Participants {
		if p.Persona != "" {
			handles[p.Persona] = true
		}
	}
	return handles
}

func runShow(handle string, reference bool) {
	var opts []identity.LoadOption
	if reference {
		opts = append(opts, identity.Reference(true))
	}

	id, err := identityStore().Load(handle, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}

	// Print warnings to stderr.
	for _, w := range id.Warnings {
		fmt.Fprintf(os.Stderr, "ethos: warning: %s\n", w)
	}

	if jsonOutput {
		printJSON(id)
		return
	}

	// Build summary table of identity fields.
	type field struct{ label, value string }
	fields := []field{
		{"Name", id.Name},
		{"Handle", id.Handle},
		{"Kind", id.Kind},
		{"Email", id.Email},
		{"GitHub", id.GitHub},
		{"Agent", id.Agent},
		{"Personality", id.Personality},
		{"Writing", id.WritingStyle},
		{"Talents", joinTalents(id.Talents)},
	}
	// Collect extension fields.
	nsNames := make([]string, 0, len(id.Ext))
	for ns := range id.Ext {
		nsNames = append(nsNames, ns)
	}
	sort.Strings(nsNames)
	for _, ns := range nsNames {
		keys := id.Ext[ns]
		keyNames := make([]string, 0, len(keys))
		for k := range keys {
			keyNames = append(keyNames, k)
		}
		sort.Strings(keyNames)
		for _, k := range keyNames {
			fields = append(fields, field{"ext:" + ns + "." + k, keys[k]})
		}
	}

	// Filter to non-empty values and build table rows.
	headers := []string{"FIELD", "VALUE"}
	var rows [][]string
	for _, f := range fields {
		if f.value != "" {
			rows = append(rows, []string{f.label, f.value})
		}
	}
	fmt.Println(hook.FormatTable(headers, rows))

	// Show resolved attribute content below the table.
	if id.WritingStyle != "" && id.WritingStyleContent != "" {
		fmt.Println()
		fmt.Print(id.WritingStyleContent)
	}
	if id.Personality != "" && id.PersonalityContent != "" {
		fmt.Println()
		fmt.Print(id.PersonalityContent)
	}
	if len(id.Talents) > 0 {
		for i, slug := range id.Talents {
			if i < len(id.TalentContents) && id.TalentContents[i] != "" {
				fmt.Println()
				fmt.Printf("--- %s ---\n", slug)
				fmt.Print(id.TalentContents[i])
			}
		}
	}
}

// joinTalents formats a talents slice for display.
func joinTalents(talents []string) string {
	var filtered []string
	for _, sk := range talents {
		if s := strings.TrimSpace(sk); s != "" {
			filtered = append(filtered, s)
		}
	}
	return strings.Join(filtered, ", ")
}

// oneLine collapses a multi-line string to a single line by joining
// whitespace-separated fields with a single space.
func oneLine(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}
