package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"gopkg.in/yaml.v3"
)

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
	var id identity.Identity
	if err := yaml.Unmarshal(data, &id); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: invalid YAML: %v\n", err)
		os.Exit(1)
	}
	if err := id.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	s := store()
	if err := s.Save(&id); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}

	if wasFirst, err := setActiveIfFirst(s, id.Handle); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: warning: %v\n", err)
	} else if wasFirst {
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

	// Attribute selection with create-new option.
	personality := pickAttribute(reader, attribute.Personalities)
	writingStyle := pickAttribute(reader, attribute.WritingStyles)
	skills := pickMultiAttribute(reader, attribute.Skills)

	var voice *identity.Voice
	if voiceProvider != "" {
		voice = &identity.Voice{Provider: voiceProvider, VoiceID: voiceID}
	}

	id := &identity.Identity{
		Name:         name,
		Handle:       handle,
		Kind:         kind,
		Email:        email,
		GitHub:       github,
		Voice:        voice,
		Agent:        agent,
		WritingStyle: writingStyle,
		Personality:  personality,
		Skills:       skills,
	}

	if err := id.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	s := store()
	if err := s.Save(id); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}

	if wasFirst, err := setActiveIfFirst(s, id.Handle); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: warning: %v\n", err)
	} else if wasFirst {
		fmt.Fprintf(os.Stderr, "Set as active identity (first identity created)\n")
	}
	fmt.Printf("Created identity %q (%s)\n", id.Handle, id.Name)
}

// pickAttribute shows existing attributes and lets the user pick one,
// create a new one, or skip (empty).
func pickAttribute(reader *bufio.Reader, kind attribute.Kind) string {
	s := attributeStore(kind)
	result, err := s.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: warning: could not list %s: %v\n", kind.PluralName, err)
	}

	fmt.Fprintf(os.Stderr, "\n%s:\n", capitalizeFirst(kind.DisplayName))
	if result != nil && len(result.Attributes) > 0 {
		for i, a := range result.Attributes {
			fmt.Fprintf(os.Stderr, "  %d. %s\n", i+1, a.Slug)
		}
	}
	fmt.Fprintf(os.Stderr, "  n. [create new]\n")
	fmt.Fprintf(os.Stderr, "  (empty to skip)\n")

	choice := prompt(reader, "Choice", "")
	if choice == "" {
		return ""
	}
	if choice == "n" || choice == "N" {
		slug := prompt(reader, fmt.Sprintf("New %s slug", kind.DisplayName), "")
		if slug == "" {
			return ""
		}
		content, err := editContent(kind, slug)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
			return ""
		}
		if strings.TrimSpace(content) == "" {
			fmt.Fprintf(os.Stderr, "ethos: empty content, skipping\n")
			return ""
		}
		a := &attribute.Attribute{Slug: slug, Content: content}
		if err := s.Save(a); err != nil {
			fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
			return ""
		}
		fmt.Fprintf(os.Stderr, "Created %s %q\n", kind.DisplayName, slug)
		return slug
	}

	// Numeric choice.
	if result != nil && len(result.Attributes) > 0 {
		idx := 0
		if _, err := fmt.Sscanf(choice, "%d", &idx); err == nil && idx >= 1 && idx <= len(result.Attributes) {
			return result.Attributes[idx-1].Slug
		}
	}

	// Treat as a slug directly — validate first.
	if err := attribute.ValidateSlug(choice); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: invalid slug %q — must be lowercase alphanumeric with hyphens\n", choice)
		return ""
	}
	return choice
}

// pickMultiAttribute shows existing attributes and lets the user pick
// multiple (comma-separated numbers), create new ones, or skip.
func pickMultiAttribute(reader *bufio.Reader, kind attribute.Kind) []string {
	s := attributeStore(kind)
	result, err := s.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: warning: could not list %s: %v\n", kind.PluralName, err)
	}

	fmt.Fprintf(os.Stderr, "\n%s (select multiple, comma-separated):\n", capitalizeFirst(kind.PluralName))
	if result != nil && len(result.Attributes) > 0 {
		for i, a := range result.Attributes {
			fmt.Fprintf(os.Stderr, "  %d. %s\n", i+1, a.Slug)
		}
	}
	fmt.Fprintf(os.Stderr, "  n. [create new]\n")
	fmt.Fprintf(os.Stderr, "  (empty to skip)\n")

	choice := prompt(reader, "Choice", "")
	if choice == "" {
		return nil
	}

	var selected []string
	parts := strings.Split(choice, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if p == "n" || p == "N" {
			slug := prompt(reader, fmt.Sprintf("New %s slug", kind.DisplayName), "")
			if slug == "" {
				continue
			}
			content, err := editContent(kind, slug)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
				continue
			}
			if strings.TrimSpace(content) == "" {
				fmt.Fprintf(os.Stderr, "ethos: empty content, skipping\n")
				continue
			}
			a := &attribute.Attribute{Slug: slug, Content: content}
			if err := s.Save(a); err != nil {
				fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
				continue
			}
			fmt.Fprintf(os.Stderr, "Created %s %q\n", kind.DisplayName, slug)
			selected = append(selected, slug)
			continue
		}

		// Numeric choice.
		idx := 0
		if result != nil && len(result.Attributes) > 0 {
			if _, err := fmt.Sscanf(p, "%d", &idx); err == nil && idx >= 1 && idx <= len(result.Attributes) {
				selected = append(selected, result.Attributes[idx-1].Slug)
				continue
			}
		}

		// Treat as slug directly — validate first.
		if err := attribute.ValidateSlug(p); err != nil {
			fmt.Fprintf(os.Stderr, "ethos: invalid slug %q — must be lowercase alphanumeric with hyphens\n", p)
			continue
		}
		selected = append(selected, p)
	}
	return selected
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

func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
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

// setActiveIfFirst sets the identity as active if it's the only one.
func setActiveIfFirst(s *identity.Store, handle string) (bool, error) {
	result, err := s.List()
	if err != nil {
		return false, fmt.Errorf("checking identity count: %w", err)
	}
	if len(result.Identities) == 1 {
		if err := s.SetActive(handle); err != nil {
			return false, fmt.Errorf("setting active identity: %w", err)
		}
		return true, nil
	}
	return false, nil
}
