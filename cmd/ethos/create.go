package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var validHandle = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// runCreateImpl implements both interactive and declarative identity creation.
// Declarative: ethos create --file <path>
// Interactive: ethos create (prompts for each field)
func runCreateImpl(args []string) {
	// Check for --file flag (declarative mode)
	for i, arg := range args {
		if arg == "--file" || arg == "-f" {
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "ethos: --file requires a path argument")
				os.Exit(1)
			}
			createFromFile(args[i+1])
			return
		}
	}

	// Interactive mode
	createInteractive()
}

func createFromFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	var id Identity
	if err := yaml.Unmarshal(data, &id); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: invalid YAML: %v\n", err)
		os.Exit(1)
	}
	if err := validateIdentity(&id); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if err := saveIdentity(&id); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}

	if setActiveIfFirst(id.Handle) {
		fmt.Fprintf(os.Stderr, "Set as active identity (first identity created)\n")
	}
	fmt.Printf("Created identity %q (%s)\n", id.Handle, id.Name)
}

func createInteractive() {
	reader := bufio.NewReader(os.Stdin)

	name := prompt(reader, "Name", "")
	handle := prompt(reader, "Handle", slugify(name))
	kind := prompt(reader, "Kind (human/agent)", "human")
	email := prompt(reader, "Email (optional)", "")
	github := prompt(reader, "GitHub username (optional)", "")
	voiceProvider := prompt(reader, "Voice provider (optional, e.g. elevenlabs)", "")
	voiceID := ""
	if voiceProvider != "" {
		voiceID = prompt(reader, "Voice ID", "")
	}
	agent := prompt(reader, "Agent definition path (optional, e.g. .claude/agents/name.md)", "")
	writingStyle := prompt(reader, "Writing style (optional, one line)", "")
	personality := prompt(reader, "Personality (optional, one line)", "")
	skillsRaw := prompt(reader, "Skills (optional, comma-separated)", "")

	var skills []string
	if skillsRaw != "" {
		for _, s := range strings.Split(skillsRaw, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				skills = append(skills, s)
			}
		}
	}

	id := &Identity{
		Name:         name,
		Handle:       handle,
		Kind:         kind,
		Email:        email,
		GitHub:       github,
		Voice:        &Voice{Provider: voiceProvider, VoiceID: voiceID},
		Agent:        agent,
		WritingStyle: writingStyle,
		Personality:  personality,
		Skills:       skills,
	}

	if err := validateIdentity(id); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if err := saveIdentity(id); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}

	if setActiveIfFirst(id.Handle) {
		fmt.Fprintf(os.Stderr, "Set as active identity (first identity created)\n")
	}
	fmt.Printf("Created identity %q (%s)\n", id.Handle, id.Name)
}

func prompt(reader *bufio.Reader, label, defaultVal string) string {
	if defaultVal != "" {
		fmt.Fprintf(os.Stderr, "%s [%s]: ", label, defaultVal)
	} else {
		fmt.Fprintf(os.Stderr, "%s: ", label)
	}
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultVal
	}
	return line
}

func slugify(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "-")
	var b strings.Builder
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

func validateIdentity(id *Identity) error {
	if id.Name == "" {
		return fmt.Errorf("name is required")
	}
	if id.Handle == "" {
		return fmt.Errorf("handle is required")
	}
	if !validHandle.MatchString(id.Handle) {
		return fmt.Errorf("handle must be lowercase alphanumeric with hyphens, got %q", id.Handle)
	}
	if id.Kind != "human" && id.Kind != "agent" {
		return fmt.Errorf("kind must be 'human' or 'agent', got %q", id.Kind)
	}
	return nil
}

// setActiveIfFirst sets the identity as active if it's the only one.
// Returns true if it was set. Never writes to stdout.
func setActiveIfFirst(handle string) bool {
	identities, err := listIdentities()
	if err != nil {
		return false
	}
	if len(identities) == 1 {
		_ = setActiveIdentity(handle)
		return true
	}
	return false
}
