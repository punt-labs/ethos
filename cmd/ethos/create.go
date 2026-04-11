package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var createFile string

var createCmd = &cobra.Command{
	Use:    "create",
	Short:  "Create a new identity",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if createFile != "" {
			return createFromFile(createFile)
		}
		return createInteractive()
	},
}

func init() {
	createCmd.Flags().StringVarP(&createFile, "file", "f", "", "Create identity from YAML file")
	rootCmd.AddCommand(createCmd)
}

func createFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var id identity.Identity
	if err := yaml.Unmarshal(data, &id); err != nil {
		return fmt.Errorf("invalid YAML: %w", err)
	}
	if err := id.Validate(); err != nil {
		return err
	}
	s := identityStore()
	if err := s.Save(&id); err != nil {
		return err
	}

	// Legacy: extract voice data from raw YAML and write to ext/vox.
	// The Identity struct has no Voice field (DES-019), so Save drops it.
	// This preserves voice config from old-format YAML files during create.
	// TODO: remove when round-trip YAML preservation is implemented.
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err == nil {
		if v, ok := raw["voice"]; ok {
			if vm, ok := v.(map[string]interface{}); ok {
				if provider, _ := vm["provider"].(string); provider != "" {
					if err := s.ExtSet(id.Handle, "vox", "provider", provider); err != nil {
						fmt.Fprintf(os.Stderr, "ethos: warning: failed to set voice provider: %v\n", err)
					}
				}
				if voiceID, _ := vm["voice_id"].(string); voiceID != "" {
					if err := s.ExtSet(id.Handle, "vox", "voice_id", voiceID); err != nil {
						fmt.Fprintf(os.Stderr, "ethos: warning: failed to set voice id: %v\n", err)
					}
				}
			}
		}
	}

	fmt.Printf("Created identity %q (%s)\n", id.Handle, id.Name)
	return nil
}

func createInteractive() error {
	reader := bufio.NewReader(os.Stdin)

	name := prompt(reader, "Name", "")
	handle := prompt(reader, "Handle", slugify(name))
	kind := prompt(reader, "Kind (human/agent)", "human")
	email := prompt(reader, "Email (optional)", "")
	github := prompt(reader, "GitHub username (optional)", "")
	agent := prompt(reader, "Agent definition path (optional, e.g. .claude/agents/name.md)", "")

	// Attribute selection with create-new option.
	personality := pickAttribute(reader, attribute.Personalities)
	writingStyle := pickAttribute(reader, attribute.WritingStyles)
	talents := pickMultiAttribute(reader, attribute.Talents)

	id := &identity.Identity{
		Name:         name,
		Handle:       handle,
		Kind:         kind,
		Email:        email,
		GitHub:       github,
		Agent:        agent,
		WritingStyle: writingStyle,
		Personality:  personality,
		Talents:      talents,
	}

	if err := id.Validate(); err != nil {
		return err
	}
	s := identityStore()
	if err := s.Save(id); err != nil {
		return err
	}

	fmt.Printf("Created identity %q (%s)\n", id.Handle, id.Name)
	return nil
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
