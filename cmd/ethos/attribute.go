package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
)

// attributeStore returns an attribute.Store for the given kind using the
// same root as the identity store.
func attributeStore(kind attribute.Kind) *attribute.Store {
	s := store()
	return attribute.NewStore(s.Root(), kind)
}

// runAttributeSubcmd dispatches create/list/show/add/remove/set for an attribute kind.
func runAttributeSubcmd(kind attribute.Kind, args []string) {
	if len(args) == 0 {
		printAttributeUsage(kind)
		os.Exit(1)
	}

	sub := args[0]
	subArgs := args[1:]

	switch sub {
	case "create":
		runAttributeCreate(kind, subArgs)
	case "list":
		runAttributeList(kind)
	case "show":
		runAttributeShow(kind, subArgs)
	case "add":
		runAttributeAdd(kind, subArgs)
	case "remove":
		runAttributeRemove(kind, subArgs)
	case "set":
		runAttributeSet(kind, subArgs)
	case "help", "-h", "--help":
		printAttributeUsage(kind)
	default:
		fmt.Fprintf(os.Stderr, "ethos %s: unknown subcommand %q\n", kind.DisplayName, sub)
		printAttributeUsage(kind)
		os.Exit(1)
	}
}

func runAttributeCreate(kind attribute.Kind, args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "ethos %s create: slug required\n", kind.DisplayName)
		os.Exit(1)
	}
	slug := args[0]
	remaining := args[1:]

	var content string

	// Check for --file flag.
	for i, arg := range remaining {
		if arg == "--file" || arg == "-f" {
			if i+1 >= len(remaining) {
				fmt.Fprintf(os.Stderr, "ethos %s create: --file requires a path\n", kind.DisplayName)
				os.Exit(1)
			}
			data, err := os.ReadFile(remaining[i+1])
			if err != nil {
				fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
				os.Exit(1)
			}
			content = string(data)
			break
		}
	}

	// If no --file, open $EDITOR.
	if content == "" {
		var err error
		content, err = editContent(kind, slug)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
			os.Exit(1)
		}
	}

	if strings.TrimSpace(content) == "" {
		fmt.Fprintf(os.Stderr, "ethos %s create: empty content, aborting\n", kind.DisplayName)
		os.Exit(1)
	}

	s := attributeStore(kind)
	a := &attribute.Attribute{Slug: slug, Content: content}
	if err := s.Save(a); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}

	p, _ := s.Path(slug)
	fmt.Printf("Created %s %q (%s)\n", kind.DisplayName, slug, p)
}

func runAttributeList(kind attribute.Kind) {
	s := attributeStore(kind)
	result, err := s.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "ethos: %s\n", w)
	}
	if jsonOutput {
		printJSON(result.Attributes)
		return
	}
	if len(result.Attributes) == 0 {
		fmt.Printf("No %s found. Run 'ethos %s create <slug>' to create one.\n", kind.PluralName, kind.CmdName)
		return
	}
	for _, a := range result.Attributes {
		fmt.Println(a.Slug)
	}
}

func runAttributeShow(kind attribute.Kind, args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "ethos %s show: slug required\n", kind.DisplayName)
		os.Exit(1)
	}
	s := attributeStore(kind)
	a, err := s.Load(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(a)
		return
	}
	fmt.Print(a.Content)
}

// runAttributeAdd adds an attribute slug to an identity's skills list.
// Only valid for skills (list field).
func runAttributeAdd(kind attribute.Kind, args []string) {
	if kind.CmdName != "skills" {
		fmt.Fprintf(os.Stderr, "ethos %s add: use 'set' for single-value attributes\n", kind.DisplayName)
		os.Exit(1)
	}
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: ethos skill add <handle> <slug>\n")
		os.Exit(1)
	}
	handle, slug := args[0], args[1]

	// Verify the skill exists.
	as := attributeStore(kind)
	if !as.Exists(slug) {
		fmt.Fprintf(os.Stderr, "ethos: skill %q not found — create it with 'ethos skill create %s'\n", slug, slug)
		os.Exit(1)
	}

	s := store()
	if err := s.Update(handle, func(id *identity.Identity) error {
		for _, existing := range id.Skills {
			if existing == slug {
				return fmt.Errorf("skill %q already on %q", slug, handle)
			}
		}
		id.Skills = append(id.Skills, slug)
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Added skill %q to %q\n", slug, handle)
}

// runAttributeRemove removes an attribute slug from an identity's skills list.
// Only valid for skills (list field).
func runAttributeRemove(kind attribute.Kind, args []string) {
	if kind.CmdName != "skills" {
		fmt.Fprintf(os.Stderr, "ethos %s remove: use 'set' to change single-value attributes\n", kind.DisplayName)
		os.Exit(1)
	}
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: ethos skill remove <handle> <slug>\n")
		os.Exit(1)
	}
	handle, slug := args[0], args[1]

	s := store()
	if err := s.Update(handle, func(id *identity.Identity) error {
		found := false
		filtered := make([]string, 0, len(id.Skills))
		for _, existing := range id.Skills {
			if existing == slug {
				found = true
			} else {
				filtered = append(filtered, existing)
			}
		}
		if !found {
			return fmt.Errorf("skill %q not found on %q", slug, handle)
		}
		id.Skills = filtered
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Removed skill %q from %q\n", slug, handle)
}

// runAttributeSet sets a single-value attribute on an identity.
// Valid for personality and writing-style.
func runAttributeSet(kind attribute.Kind, args []string) {
	if kind.CmdName == "skills" {
		fmt.Fprintf(os.Stderr, "ethos skill set: use 'add' and 'remove' for list attributes\n")
		os.Exit(1)
	}
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: ethos %s set <handle> <slug>\n", kind.CmdName)
		os.Exit(1)
	}
	handle, slug := args[0], args[1]

	// Verify the attribute exists.
	as := attributeStore(kind)
	if !as.Exists(slug) {
		fmt.Fprintf(os.Stderr, "ethos: %s %q not found — create it with 'ethos %s create %s'\n",
			kind.DisplayName, slug, kind.CmdName, slug)
		os.Exit(1)
	}

	s := store()
	if err := s.Update(handle, func(id *identity.Identity) error {
		switch kind.CmdName {
		case "personalities":
			id.Personality = slug
		case "writing-styles":
			id.WritingStyle = slug
		default:
			return fmt.Errorf("set not supported for %s", kind.DisplayName)
		}
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Set %s %q on %q\n", kind.DisplayName, slug, handle)
}

func printAttributeUsage(kind attribute.Kind) {
	fmt.Fprintf(os.Stderr, "Usage: ethos %s <subcommand>\n\n", kind.CmdName)
	fmt.Fprintf(os.Stderr, "Subcommands:\n")
	fmt.Fprintf(os.Stderr, "  create <slug>           Create a new %s\n", kind.DisplayName)
	fmt.Fprintf(os.Stderr, "  list                    List all %s\n", kind.PluralName)
	fmt.Fprintf(os.Stderr, "  show <slug>             Show %s content\n", kind.DisplayName)
	if kind.CmdName == "skills" {
		fmt.Fprintf(os.Stderr, "  add <handle> <slug>     Add %s to an identity\n", kind.DisplayName)
		fmt.Fprintf(os.Stderr, "  remove <handle> <slug>  Remove %s from an identity\n", kind.DisplayName)
	} else {
		fmt.Fprintf(os.Stderr, "  set <handle> <slug>     Set %s on an identity\n", kind.DisplayName)
	}
}

// editContent opens $EDITOR for the user to write attribute content.
func editContent(kind attribute.Kind, slug string) (string, error) {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}

	tmpDir := filepath.Join(os.TempDir(), "ethos")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return "", fmt.Errorf("creating temp directory: %w", err)
	}

	tmpFile := filepath.Join(tmpDir, fmt.Sprintf("%s-%s.md", kind.CmdName, slug))
	// Write a starter template.
	starter := fmt.Sprintf("# %s\n\n", slug)
	if err := os.WriteFile(tmpFile, []byte(starter), 0o600); err != nil {
		return "", fmt.Errorf("writing temp file: %w", err)
	}
	defer os.Remove(tmpFile)

	cmd := exec.Command(editor, tmpFile)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("editor failed: %w", err)
	}

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		return "", fmt.Errorf("reading edited file: %w", err)
	}
	return string(data), nil
}
