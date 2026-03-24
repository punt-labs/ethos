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
	registerLegacyCommands()
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// registerLegacyCommands wires existing run functions into cobra commands.
// This is temporary — each will be properly migrated in later phases.
func registerLegacyCommands() {
	cmds := []struct {
		use    string
		short  string
		fn     func([]string)
		hidden bool
	}{
		{"whoami", "Show the caller's identity", runWhoami, false},
		{"show", "Show identity details", runShow, false},
		{"list", "List all identities", func(_ []string) { runList() }, false},
		{"ext", "Manage tool-scoped extensions", runExt, false},
		{"iam", "Declare persona in current session", runIam, false},
		{"session", "Manage session roster", runSession, false},
		{"talent", "Manage talents", runTalent, false},
		{"personality", "Manage personalities", runPersonality, false},
		{"writing-style", "Manage writing styles", runWritingStyle, false},
		{"hook", "Internal hook dispatcher", runHook, true},
		{"resolve-agent", "Show default agent", func(_ []string) { runResolveAgent() }, false},
		{"create", "Create a new identity", runCreate, false},
		{"serve", "Start MCP server (stdio transport)", func(_ []string) { runServe() }, false},
		{"uninstall", "Remove plugin", runUninstall, false},
	}

	for _, c := range cmds {
		fn := c.fn // capture
		cmd := &cobra.Command{
			Use:                c.use,
			Short:              c.short,
			DisableFlagParsing: true,
			Run: func(cmd *cobra.Command, args []string) {
				var filtered []string
				for _, a := range args {
					if a == "--json" {
						jsonOutput = true
					} else {
						filtered = append(filtered, a)
					}
				}
				fn(filtered)
			},
		}
		cmd.Hidden = c.hidden
		rootCmd.AddCommand(cmd)
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

// printSubcommandHelp prints usage for legacy commands that handle their own
// flag parsing. Will be removed as commands migrate to native cobra.
func printSubcommandHelp(cmd string) {
	switch cmd {
	case "ext":
		fmt.Print("Usage: ethos ext <subcommand> [args]\n\n  Manage tool-scoped extensions on identities.\n\n  ethos ext get <persona> <namespace> [key]\n  ethos ext set <persona> <namespace> <key> <value>\n  ethos ext del <persona> <namespace> [key]\n  ethos ext list <persona>\n")
	case "session":
		fmt.Print("Usage: ethos session [subcommand]\n\n  Manage session roster.\n\n  ethos session                                  Show current session participants\n  ethos session create --session ID --root-id X   Create a new session roster\n  ethos session join --agent-id X [...]            Add a participant\n  ethos session leave --agent-id X                 Remove a participant\n  ethos session purge                              Clean up stale sessions\n")
	}
}

func runVersion() {
	if jsonOutput {
		printJSON(map[string]string{"version": version})
	} else {
		fmt.Printf("ethos %s\n", version)
	}
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

func runWhoami(_ []string) {
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

func runServe() {
	runServeImpl()
}

func runCreate(args []string) {
	runCreateImpl(args)
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

func runShow(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "ethos: show requires a handle argument")
		os.Exit(1)
	}

	// Check for --reference flag.
	handle := args[0]
	var opts []identity.LoadOption
	for _, a := range args[1:] {
		if a == "--reference" {
			opts = append(opts, identity.Reference(true))
		}
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
