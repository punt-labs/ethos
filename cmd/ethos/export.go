package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var (
	exportFormat string
	exportDir    string
)

var exportCmd = &cobra.Command{
	Use:     "export <handle>",
	Short:   "Export identity to a portable format (lossy)",
	Long:    "Export identity to SoulSpec or CLAUDE.md format. Both exports are lossy: structural data (roles, teams, extensions) drops because the target format cannot represent it.",
	GroupID: "identity",
	Args:    cobra.ExactArgs(1),
	RunE:    runExport,
}

func init() {
	exportCmd.Flags().StringVar(&exportFormat, "to", "", "Export format: soulspec or claude-md (required)")
	exportCmd.Flags().StringVar(&exportDir, "dir", ".", "Output directory for soulspec files")
	_ = exportCmd.MarkFlagRequired("to")
	rootCmd.AddCommand(exportCmd)
}

func runExport(cmd *cobra.Command, args []string) error {
	handle := args[0]

	switch exportFormat {
	case "soulspec":
		return exportSoulSpec(handle, exportDir)
	case "claude-md":
		return exportClaudeMD(handle)
	default:
		return fmt.Errorf("unsupported format %q: must be soulspec or claude-md", exportFormat)
	}
}

// exportSoulSpec writes SOUL.md, IDENTITY.md, and STYLE.md to dir.
func exportSoulSpec(handle, dir string) error {
	is := identityStore()
	id, err := is.Load(handle)
	if err != nil {
		return fmt.Errorf("loading identity %q: %w", handle, err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	var wrote []string

	// SOUL.md — personality content.
	if id.PersonalityContent == "" {
		fmt.Fprintf(os.Stderr, "warning: %s has no personality content, skipping SOUL.md\n", handle)
	} else {
		p := filepath.Join(dir, "SOUL.md")
		content := fmt.Sprintf("# %s\n\n%s", id.Name, id.PersonalityContent)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			return fmt.Errorf("writing SOUL.md: %w", err)
		}
		wrote = append(wrote, p)
	}

	// IDENTITY.md — name, handle, kind, bindings.
	{
		var b strings.Builder
		fmt.Fprintf(&b, "# %s\n\n", id.Name)
		fmt.Fprintf(&b, "Handle: %s\n", id.Handle)
		fmt.Fprintf(&b, "Kind: %s\n", id.Kind)
		if id.Email != "" {
			fmt.Fprintf(&b, "Email: %s\n", id.Email)
		}
		if id.GitHub != "" {
			fmt.Fprintf(&b, "GitHub: %s\n", id.GitHub)
		}
		p := filepath.Join(dir, "IDENTITY.md")
		if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
			return fmt.Errorf("writing IDENTITY.md: %w", err)
		}
		wrote = append(wrote, p)
	}

	// STYLE.md — writing style content.
	if id.WritingStyleContent == "" {
		fmt.Fprintf(os.Stderr, "warning: %s has no writing style content, skipping STYLE.md\n", handle)
	} else {
		p := filepath.Join(dir, "STYLE.md")
		content := fmt.Sprintf("# %s Writing Style\n\n%s", id.Name, id.WritingStyleContent)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			return fmt.Errorf("writing STYLE.md: %w", err)
		}
		wrote = append(wrote, p)
	}

	for _, f := range wrote {
		fmt.Printf("wrote %s\n", f)
	}
	return nil
}

// exportClaudeMD prints a CLAUDE.md identity section to stdout.
func exportClaudeMD(handle string) error {
	is := identityStore()
	id, err := is.Load(handle)
	if err != nil {
		return fmt.Errorf("loading identity %q: %w", handle, err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Identity: %s\n\n", id.Name)
	fmt.Fprintf(&b, "Handle: %s\n", id.Handle)
	fmt.Fprintf(&b, "Kind: %s\n", id.Kind)

	if id.PersonalityContent != "" {
		fmt.Fprintf(&b, "\n## Personality\n\n%s", id.PersonalityContent)
	} else {
		fmt.Fprintf(os.Stderr, "warning: %s has no personality content\n", handle)
	}

	if id.WritingStyleContent != "" {
		fmt.Fprintf(&b, "\n## Writing Style\n\n%s", id.WritingStyleContent)
	} else {
		fmt.Fprintf(os.Stderr, "warning: %s has no writing style content\n", handle)
	}

	if len(id.Talents) > 0 {
		fmt.Fprintf(&b, "\n## Talents\n\n%s\n", strings.Join(id.Talents, ", "))
	}

	fmt.Print(b.String())
	return nil
}
