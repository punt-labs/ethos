package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/internal/identity"
)

var version = "dev"

// jsonOutput is set by the --json global flag.
var jsonOutput bool

func main() {
	// Extract global flags. --json is recognized anywhere except after "--".
	var args []string
	pastSeparator := false
	for _, a := range os.Args[1:] {
		if a == "--" {
			pastSeparator = true
			args = append(args, a)
		} else if !pastSeparator && a == "--json" {
			jsonOutput = true
		} else {
			args = append(args, a)
		}
	}

	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}

	cmd := args[0]
	cmdArgs := args[1:]

	// Check for subcommand-level --help (skip if cmd is itself a help alias).
	if cmd != "help" && cmd != "-h" && cmd != "--help" && hasHelpFlag(cmdArgs) {
		printSubcommandHelp(cmd)
		return
	}

	commands := map[string]func([]string){
		"version":       func([]string) { runVersion() },
		"doctor":        func([]string) { runDoctor() },
		"whoami":        runWhoami,
		"serve":         func([]string) { runServe() },
		"create":        runCreate,
		"list":          func([]string) { runList() },
		"show":          runShow,
		"ext":           runExt,
		"iam":           runIam,
		"session":       runSession,
		"skill":         runSkill,
		"personality":   runPersonality,
		"writing-style": runWritingStyle,
		"uninstall":     runUninstall,
		"help":          func([]string) { printUsage() },
		"-h":            func([]string) { printUsage() },
		"--help":        func([]string) { printUsage() },
	}

	if fn, ok := commands[cmd]; ok {
		fn(cmdArgs)
	} else {
		fmt.Fprintf(os.Stderr, "ethos: unknown command %q\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

// hasHelpFlag returns true if args contains --help or -h.
func hasHelpFlag(args []string) bool {
	for _, a := range args {
		if a == "--help" || a == "-h" {
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

func printSubcommandHelp(cmd string) {
	switch cmd {
	case "whoami":
		fmt.Print("Usage: ethos whoami [handle] [--json]\n\n  Show the active identity, or set it to <handle>.\n")
	case "create":
		fmt.Print("Usage: ethos create [-f|--file <path>]\n\n  Create a new identity interactively, or from a YAML file.\n")
	case "list":
		fmt.Print("Usage: ethos list [--json]\n\n  List all identities. Active identity is marked with *.\n")
	case "show":
		fmt.Print("Usage: ethos show <handle> [--json]\n\n  Show full details for an identity.\n")
	case "doctor":
		fmt.Print("Usage: ethos doctor [--json]\n\n  Check installation health.\n")
	case "serve":
		fmt.Print("Usage: ethos serve\n\n  Start MCP server (stdio transport).\n")
	case "version":
		fmt.Print("Usage: ethos version\n\n  Print version.\n")
	case "ext":
		fmt.Print("Usage: ethos ext <subcommand> [args]\n\n  Manage tool-scoped extensions on identities.\n\n  ethos ext get <persona> <namespace> [key]\n  ethos ext set <persona> <namespace> <key> <value>\n  ethos ext del <persona> <namespace> [key]\n  ethos ext list <persona>\n")
	case "iam":
		fmt.Print("Usage: ethos iam <persona>\n\n  Declare your persona in the current session.\n")
	case "uninstall":
		fmt.Print("Usage: ethos uninstall [--purge]\n\n  Remove the Claude Code plugin.\n  With --purge: also remove the binary and all identity data.\n")
	case "session":
		fmt.Print("Usage: ethos session [subcommand]\n\n  Manage session roster.\n\n  ethos session                                  Show current session participants\n  ethos session create --session ID --root-id X   Create a new session roster\n  ethos session join --agent-id X [...]            Add a participant\n  ethos session leave --agent-id X                 Remove a participant\n  ethos session purge                              Clean up stale sessions\n")
	case "skill":
		fmt.Print("Usage: ethos skill <subcommand>\n\n  Manage skills.\n\n  ethos skill create <slug>           Create a new skill\n  ethos skill list                    List all skills\n  ethos skill show <slug>             Show skill content\n  ethos skill add <handle> <slug>     Add skill to an identity\n  ethos skill remove <handle> <slug>  Remove skill from an identity\n")
	case "personality":
		fmt.Print("Usage: ethos personality <subcommand>\n\n  Manage personalities.\n\n  ethos personality create <slug>           Create a new personality\n  ethos personality list                    List all personalities\n  ethos personality show <slug>             Show personality content\n  ethos personality set <handle> <slug>     Set personality on an identity\n")
	case "writing-style":
		fmt.Print("Usage: ethos writing-style <subcommand>\n\n  Manage writing styles.\n\n  ethos writing-style create <slug>           Create a new writing style\n  ethos writing-style list                    List all writing styles\n  ethos writing-style show <slug>             Show writing style content\n  ethos writing-style set <handle> <slug>     Set writing style on an identity\n")
	default:
		fmt.Fprintf(os.Stderr, "ethos: unknown command %q\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`ethos: identity binding for humans and AI agents

Product commands:
  whoami [name]     Show or set the active identity
  create            Create a new identity
  list              List all identities
  show <handle>     Show identity details
  ext               Manage tool-scoped extensions

Attribute commands:
  skill             Manage skills (create, list, show, add, remove)
  personality       Manage personalities (create, list, show, set)
  writing-style     Manage writing styles (create, list, show, set)

Session commands:
  iam <persona>     Declare persona in current session
  session           Show or manage session roster

Admin commands:
  version           Print version
  doctor            Check installation health
  serve             Start MCP server (stdio transport)
  uninstall         Remove plugin (--purge to remove binary + data)

Flags:
  --json            JSON output
  --help, -h        Show this help
`)
}

func runVersion() {
	if jsonOutput {
		printJSON(map[string]string{"version": version})
	} else {
		fmt.Printf("ethos %s\n", version)
	}
}

func runDoctor() {
	s := store()

	type checkResult struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Detail string `json:"detail"`
	}

	checks := []struct {
		name string
		fn   func(*identity.Store) (string, bool)
	}{
		{"Identity directory", checkIdentityDir},
		{"Active identity", checkActiveIdentity},
	}

	allPassed := true
	var results []checkResult
	for _, c := range checks {
		detail, ok := c.fn(s)
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

func checkIdentityDir(s *identity.Store) (string, bool) {
	dir := s.IdentitiesDir()
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("not found: %s", dir), false
		}
		return fmt.Sprintf("error: %v", err), false
	}
	return dir, true
}

func checkActiveIdentity(s *identity.Store) (string, bool) {
	id, err := s.Active()
	if err != nil {
		if errors.Is(err, identity.ErrNoActive) {
			return "none configured — run 'ethos create'", true
		}
		return fmt.Sprintf("error: %v", err), false
	}
	return id.Name, true
}

func runWhoami(args []string) {
	s := store()
	if len(args) > 0 {
		handle := args[0]
		if err := s.SetActive(handle); err != nil {
			fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
			os.Exit(1)
		}
		if jsonOutput {
			printJSON(map[string]string{"active": handle})
		} else {
			fmt.Printf("Active identity set to %q\n", handle)
		}
		return
	}

	id, err := s.Active()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ethos: no active identity. Run 'ethos create' or 'ethos whoami <handle>'.")
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(id)
	} else {
		fmt.Printf("%s (%s)\n", id.Name, id.Handle)
	}
}

func runServe() {
	runServeImpl()
}

func runCreate(args []string) {
	runCreateImpl(args)
}

func runList() {
	s := store()
	result, err := s.List()
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
	active, _ := s.Active()
	for _, id := range result.Identities {
		marker := "  "
		if active != nil && active.Handle == id.Handle {
			marker = "* "
		}
		fmt.Printf("%s%-16s %s\n", marker, id.Handle, id.Name)
	}
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

	id, err := store().Load(handle, opts...)
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
	showField("Name", id.Name)
	showField("Handle", id.Handle)
	showField("Kind", id.Kind)
	showField("Email", id.Email)
	showField("GitHub", id.GitHub)
	showField("Voice", voiceValue(id.Voice))
	showField("Agent", id.Agent)

	// Show attribute slugs and resolved content.
	if id.WritingStyle != "" {
		showField("Writing", id.WritingStyle)
		if id.WritingStyleContent != "" {
			fmt.Println()
			fmt.Print(id.WritingStyleContent)
		}
	}
	if id.Personality != "" {
		showField("Personality", id.Personality)
		if id.PersonalityContent != "" {
			fmt.Println()
			fmt.Print(id.PersonalityContent)
		}
	}
	if len(id.Skills) > 0 {
		showField("Skills", joinSkills(id.Skills))
		for i, slug := range id.Skills {
			if i < len(id.SkillContents) && id.SkillContents[i] != "" {
				fmt.Println()
				fmt.Printf("--- %s ---\n", slug)
				fmt.Print(id.SkillContents[i])
			}
		}
	}
	showExtensions(id.Ext)
}

// voiceValue formats a voice binding for display.
func voiceValue(v *identity.Voice) string {
	if v == nil || v.Provider == "" {
		return ""
	}
	if v.VoiceID != "" {
		return v.Provider + "/" + v.VoiceID
	}
	return v.Provider
}

// joinSkills formats a skills slice for display.
func joinSkills(skills []string) string {
	var filtered []string
	for _, sk := range skills {
		if s := strings.TrimSpace(sk); s != "" {
			filtered = append(filtered, s)
		}
	}
	return strings.Join(filtered, ", ")
}

// showExtensions prints sorted extension key-value pairs.
func showExtensions(ext map[string]map[string]string) {
	nsNames := make([]string, 0, len(ext))
	for ns := range ext {
		nsNames = append(nsNames, ns)
	}
	sort.Strings(nsNames)
	for _, ns := range nsNames {
		keys := ext[ns]
		keyNames := make([]string, 0, len(keys))
		for k := range keys {
			keyNames = append(keyNames, k)
		}
		sort.Strings(keyNames)
		for _, k := range keyNames {
			showField("ext:"+ns+"."+k, keys[k])
		}
	}
}

// showField prints a labeled field if the value is non-empty.
func showField(label, value string) {
	if value != "" {
		fmt.Printf("%-13s %s\n", label+":", value)
	}
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
