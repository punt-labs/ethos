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

	switch cmd {
	case "version":
		if jsonOutput {
			printJSON(map[string]string{"version": version})
		} else {
			fmt.Printf("ethos %s\n", version)
		}
	case "doctor":
		runDoctor()
	case "whoami":
		runWhoami(cmdArgs)
	case "serve":
		runServe()
	case "create":
		runCreate(cmdArgs)
	case "list":
		runList()
	case "show":
		runShow(cmdArgs)
	case "ext":
		runExt(cmdArgs)
	case "iam":
		runIam(cmdArgs)
	case "session":
		runSession(cmdArgs)
	case "uninstall":
		runUninstall(cmdArgs)
	case "help", "-h", "--help":
		printUsage()
	default:
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
	id, err := store().Load(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
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
	if id.Voice != nil && id.Voice.Provider != "" && id.Voice.VoiceID != "" {
		showField("Voice", id.Voice.Provider+"/"+id.Voice.VoiceID)
	} else if id.Voice != nil && id.Voice.Provider != "" {
		showField("Voice", id.Voice.Provider)
	}
	showField("Agent", id.Agent)
	showField("Writing", oneLine(id.WritingStyle))
	showField("Personality", oneLine(id.Personality))
	var skills []string
	for _, sk := range id.Skills {
		if s := strings.TrimSpace(sk); s != "" {
			skills = append(skills, s)
		}
	}
	showField("Skills", strings.Join(skills, ", "))
	if len(id.Ext) > 0 {
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
				showField("ext:"+ns+"."+k, keys[k])
			}
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
