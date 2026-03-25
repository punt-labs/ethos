package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/spf13/cobra"
)

// attributeStore returns an attribute.Store for the given kind that searches
// both repo and global roots when a layered identity store is in use.
func attributeStore(kind attribute.Kind) *attribute.Store {
	return layeredAttributeStore(identityStore(), kind)
}

// layeredAttributeStore creates an attribute store from an identity store.
// If the identity store is a LayeredStore with both repo and global roots,
// the returned attribute store searches both layers. Otherwise falls back
// to a single-root store.
func layeredAttributeStore(is identity.IdentityStore, kind attribute.Kind) *attribute.Store {
	if ls, ok := is.(*identity.LayeredStore); ok {
		return attribute.NewLayeredStore(ls.RepoRoot(), ls.GlobalRoot(), kind)
	}
	return attribute.NewStore(is.Root(), kind)
}

// registerAttributeCommands registers a parent command with create/list/show/delete
// subcommands for an attribute kind. For Talents, adds add/remove. For others, adds set.
func registerAttributeCommands(root *cobra.Command, kind attribute.Kind, use, short string) {
	parent := &cobra.Command{
		Use:     use,
		Short:   short,
		GroupID: "attributes",
	}

	var createFile string

	createCmd := &cobra.Command{
		Use:   "create <slug>",
		Short: fmt.Sprintf("Create a new %s", kind.DisplayName),
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			runAttributeCreate(kind, args[0], createFile)
		},
	}
	createCmd.Flags().StringVarP(&createFile, "file", "f", "", "Read content from file")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: fmt.Sprintf("List all %s", kind.PluralName),
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			runAttributeList(kind)
		},
	}

	showCmd := &cobra.Command{
		Use:   "show <slug>",
		Short: fmt.Sprintf("Show %s content", kind.DisplayName),
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			runAttributeShow(kind, args[0])
		},
	}

	deleteCmd := &cobra.Command{
		Use:   "delete <slug>",
		Short: fmt.Sprintf("Delete a %s", kind.DisplayName),
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			runAttributeDelete(kind, args[0])
		},
	}

	parent.AddCommand(createCmd, listCmd, showCmd, deleteCmd)

	if kind == attribute.Talents {
		addCmd := &cobra.Command{
			Use:   "add <handle> <slug>",
			Short: fmt.Sprintf("Add %s to an identity", kind.DisplayName),
			Args:  cobra.ExactArgs(2),
			Run: func(cmd *cobra.Command, args []string) {
				runAttributeAdd(kind, args[0], args[1])
			},
		}

		removeCmd := &cobra.Command{
			Use:   "remove <handle> <slug>",
			Short: fmt.Sprintf("Remove %s from an identity", kind.DisplayName),
			Args:  cobra.ExactArgs(2),
			Run: func(cmd *cobra.Command, args []string) {
				runAttributeRemove(args[0], args[1])
			},
		}

		parent.AddCommand(addCmd, removeCmd)
	} else {
		setCmd := &cobra.Command{
			Use:   "set <handle> <slug>",
			Short: fmt.Sprintf("Set %s on an identity", kind.DisplayName),
			Args:  cobra.ExactArgs(2),
			Run: func(cmd *cobra.Command, args []string) {
				runAttributeSet(kind, args[0], args[1])
			},
		}

		parent.AddCommand(setCmd)
	}

	root.AddCommand(parent)
}

func init() {
	registerAttributeCommands(rootCmd, attribute.Talents, "talent", "Manage talents (create, list, show, add, remove)")
	registerAttributeCommands(rootCmd, attribute.Personalities, "personality", "Manage personalities (create, list, show, set)")
	registerAttributeCommands(rootCmd, attribute.WritingStyles, "writing-style", "Manage writing styles (create, list, show, set)")
}

func runAttributeCreate(kind attribute.Kind, slug string, file string) {
	var content string

	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
			os.Exit(1)
		}
		content = string(data)
	} else {
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

func runAttributeShow(kind attribute.Kind, slug string) {
	s := attributeStore(kind)
	a, err := s.Load(slug)
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

func runAttributeDelete(kind attribute.Kind, slug string) {
	s := attributeStore(kind)
	if err := s.Delete(slug); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(map[string]string{"deleted": slug, "kind": kind.DisplayName})
		return
	}
	fmt.Printf("Deleted %s %q\n", kind.DisplayName, slug)
}

// runAttributeAdd adds an attribute slug to an identity's talents list.
func runAttributeAdd(kind attribute.Kind, handle, slug string) {
	if kind != attribute.Talents {
		fmt.Fprintf(os.Stderr, "ethos %s add: use 'set' for single-value attributes\n", kind.DisplayName)
		os.Exit(1)
	}

	// Verify the talent exists.
	as := attributeStore(kind)
	if !as.Exists(slug) {
		fmt.Fprintf(os.Stderr, "ethos: talent %q not found — create it with 'ethos talent create %s'\n", slug, slug)
		os.Exit(1)
	}

	is := identityStore()
	if err := is.Update(handle, func(id *identity.Identity) error {
		for _, existing := range id.Talents {
			if existing == slug {
				return fmt.Errorf("talent %q already on %q", slug, handle)
			}
		}
		id.Talents = append(id.Talents, slug)
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Added talent %q to %q\n", slug, handle)
}

// runAttributeRemove removes a talent slug from an identity.
func runAttributeRemove(handle, slug string) {
	is := identityStore()
	if err := is.Update(handle, func(id *identity.Identity) error {
		found := false
		filtered := make([]string, 0, len(id.Talents))
		for _, existing := range id.Talents {
			if existing == slug {
				found = true
			} else {
				filtered = append(filtered, existing)
			}
		}
		if !found {
			return fmt.Errorf("talent %q not found on %q", slug, handle)
		}
		id.Talents = filtered
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Removed talent %q from %q\n", slug, handle)
}

// runAttributeSet sets a single-value attribute on an identity.
func runAttributeSet(kind attribute.Kind, handle, slug string) {
	if kind == attribute.Talents {
		fmt.Fprintf(os.Stderr, "ethos talent set: use 'add' and 'remove' for list attributes\n")
		os.Exit(1)
	}

	// Verify the attribute exists.
	as := attributeStore(kind)
	if !as.Exists(slug) {
		fmt.Fprintf(os.Stderr, "ethos: %s %q not found — create it with 'ethos %s create %s'\n",
			kind.DisplayName, slug, kind.CmdName, slug)
		os.Exit(1)
	}

	is := identityStore()
	if err := is.Update(handle, func(id *identity.Identity) error {
		switch kind {
		case attribute.Personalities:
			id.Personality = slug
		case attribute.WritingStyles:
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
