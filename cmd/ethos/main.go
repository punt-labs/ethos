package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/internal/doctor"
	"github.com/punt-labs/ethos/internal/hook"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/spf13/cobra"
)

var version = "dev"

// jsonOutput is set by the --json persistent flag on rootCmd.
var jsonOutput bool

func main() {
	if err := rootCmd.Execute(); err != nil {
		// Cobra already printed the error to stderr (SilenceErrors is
		// off). We only set the exit code here.
		if isUsageError(err) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

// isUsageError reports whether err is a cobra usage error (bad flag,
// unknown command, wrong arg count). Cobra does not export a typed
// error for these; we match on the message prefixes cobra itself
// generates.
func isUsageError(err error) bool {
	msg := err.Error()
	prefixes := []string{
		"unknown command",
		"unknown flag",
		"unknown shorthand flag",
		"required flag",
		"accepts ",
		"requires at least",
		"invalid argument",
		"flag needs an argument",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(msg, p) {
			return true
		}
	}
	return false
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
	Use:     "version",
	Short:   "Print version",
	GroupID: "admin",
	Args:    cobra.NoArgs,
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
	Use:     "doctor",
	Short:   "Check installation health",
	GroupID: "admin",
	Args:    cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runDoctor()
	},
}

// --- whoami ---

var whoamiReference bool

var whoamiCmd = &cobra.Command{
	Use:     "whoami",
	Short:   "Show the caller's identity",
	GroupID: "identity",
	Args:    cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runWhoami()
	},
}

// --- show ---

var showReference bool

var showCmd = &cobra.Command{
	Use:    "show <handle>",
	Short:  "Show identity details",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runShow(args[0], showReference)
	},
}

// --- list ---

var listCmd = &cobra.Command{
	Use:    "list",
	Short:  "List all identities",
	Hidden: true,
	Args:   cobra.NoArgs,
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
	Use:    "iam <persona>",
	Short:  "Declare persona in current session",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runIam(args[0])
	},
}

// --- serve ---

var serveCmd = &cobra.Command{
	Use:     "serve",
	Short:   "Start MCP server (stdio transport)",
	GroupID: "admin",
	Args:    cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runServeImpl()
	},
}

func init() {
	showCmd.Flags().BoolVar(&showReference, "reference", false, "Include reference identity data")
	whoamiCmd.Flags().BoolVar(&whoamiReference, "reference", false, "Include reference identity data")
	iamCmd.Flags().StringVar(&sessionIamSession, "session", "", "Session ID (full or prefix)")


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
	results := doctor.RunAll(is, ss)

	if jsonOutput {
		printJSON(results)
	} else {
		for _, r := range results {
			fmt.Printf("  %-24s %s  %s\n", r.Name, r.Status, r.Detail)
		}
	}

	if !doctor.AllPassed(results) {
		os.Exit(1)
	}
}

func runWhoami() {
	is := identityStore()
	ss := sessionStore()

	handle, err := resolve.Resolve(is, ss)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}

	var opts []identity.LoadOption
	if whoamiReference {
		opts = append(opts, identity.Reference(true))
	}

	id, err := is.Load(handle, opts...)
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
	handle, err := resolve.ResolveAgent(repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
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

	// Build columnar table.
	headers := []string{"HANDLE", "NAME", "KIND", "PERSONALITY", "WRITING"}
	rows := make([][]string, len(result.Identities))
	for i, id := range result.Identities {
		personality := id.Personality
		if personality == "" {
			personality = "-"
		}
		writing := id.WritingStyle
		if writing == "" {
			writing = "-"
		}
		rows[i] = []string{id.Handle, id.Name, id.Kind, personality, writing}
	}

	fmt.Println(hook.FormatTable(headers, rows))
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
