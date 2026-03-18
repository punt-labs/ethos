package main

import (
	"fmt"
	"os"
	"strings"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "version":
		fmt.Printf("ethos %s\n", version)
	case "doctor":
		runDoctor()
	case "whoami":
		runWhoami(os.Args[2:])
	case "serve":
		runServe()
	case "create":
		runCreate(os.Args[2:])
	case "list":
		runList()
	case "show":
		runShow(os.Args[2:])
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "ethos: unknown command %q\n", os.Args[1])
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
	checks := []struct {
		name string
		fn   func() (string, bool)
	}{
		{"Identity directory", checkIdentityDir},
		{"Active identity", checkActiveIdentity},
	}

	allPassed := true
	for _, c := range checks {
		detail, ok := c.fn()
		status := "PASS"
		if !ok {
			status = "FAIL"
			allPassed = false
		}
		fmt.Printf("  %-24s %s  %s\n", c.name, status, detail)
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
		fmt.Printf("Active identity set to %q\n", handle)
		return
	}

	// Show active identity
	id, err := activeIdentity()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ethos: no active identity. Run 'ethos create' or 'ethos whoami <handle>'.")
		os.Exit(1)
	}
	fmt.Printf("%s (%s)\n", id.Name, id.Handle)
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
	showField("Name", id.Name)
	showField("Handle", id.Handle)
	showField("Kind", id.Kind)
	showField("Email", id.Email)
	showField("GitHub", id.GitHub)
	if id.Voice.Provider != "" && id.Voice.VoiceID != "" {
		showField("Voice", id.Voice.Provider+"/"+id.Voice.VoiceID)
	} else if id.Voice.Provider != "" {
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
