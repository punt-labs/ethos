package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

var version = "dev"

// jsonOutput is set by the --json global flag.
var jsonOutput bool

func main() {
	// Extract global flags before command dispatch.
	var args []string
	for _, a := range os.Args[1:] {
		if a == "--json" {
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

	// Check for subcommand-level --help.
	if hasHelpFlag(cmdArgs) {
		printSubcommandHelp(cmd)
		return
	}

	switch cmd {
	case "version":
		fmt.Printf("ethos %s\n", version)
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
		fmt.Print("Usage: ethos whoami [handle]\n\n  Show the active identity, or set it to <handle>.\n")
	case "create":
		fmt.Print("Usage: ethos create [--file <path>]\n\n  Create a new identity interactively, or from a YAML file.\n")
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
	default:
		printUsage()
	}
}

func printUsage() {
	fmt.Print(`ethos: identity binding for humans and AI agents

Product commands:
  whoami [name]     Show or set the active identity
  create            Create a new identity
  list              List all identities
  show <handle>     Show identity details

Admin commands:
  version           Print version
  doctor            Check installation health
  serve             Start MCP server (stdio transport)

Flags:
  --json            JSON output
  --help, -h        Show this help
`)
}

func runDoctor() {
	type checkResult struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Detail string `json:"detail"`
	}

	checks := []struct {
		name string
		fn   func() (string, bool)
	}{
		{"Identity directory", checkIdentityDir},
		{"Active identity", checkActiveIdentity},
	}

	allPassed := true
	var results []checkResult
	for _, c := range checks {
		detail, ok := c.fn()
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

func checkIdentityDir() (string, bool) {
	dir := identityDir()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Sprintf("not found: %s", dir), false
	}
	return dir, true
}

func checkActiveIdentity() (string, bool) {
	id, err := activeIdentity()
	if err != nil {
		return "none configured", false
	}
	return id.Name, true
}

func runWhoami(args []string) {
	if len(args) > 0 {
		// Set active identity
		handle := args[0]
		if err := setActiveIdentity(handle); err != nil {
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

	// Show active identity
	id, err := activeIdentity()
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
	identities, err := listIdentities()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		if identities == nil {
			identities = []*Identity{}
		}
		printJSON(identities)
		return
	}
	if len(identities) == 0 {
		fmt.Println("No identities found. Run 'ethos create' to create one.")
		return
	}
	active, _ := activeIdentity()
	for _, id := range identities {
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
	id, err := loadIdentity(args[0])
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
