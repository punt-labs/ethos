package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/spf13/cobra"
)

var (
	importFrom   string
	importDir    string
	importHandle string
)

var importCmd = &cobra.Command{
	Use:     "import",
	Short:   "Import identity from an external format",
	Long:    "Import identity from SoulSpec files (SOUL.md, IDENTITY.md, STYLE.md). Only SOUL.md is required.",
	GroupID: "identity",
	Args:    cobra.NoArgs,
	RunE:    runImport,
}

func init() {
	importCmd.Flags().StringVar(&importFrom, "from", "", "Source format: soulspec (required)")
	importCmd.Flags().StringVar(&importDir, "dir", ".", "Directory containing source files")
	importCmd.Flags().StringVar(&importHandle, "handle", "", "Override handle (default: derived from name)")
	_ = importCmd.MarkFlagRequired("from")
	rootCmd.AddCommand(importCmd)
}

func runImport(cmd *cobra.Command, args []string) error {
	switch importFrom {
	case "soulspec":
		return importSoulSpec(importDir, importHandle)
	default:
		return fmt.Errorf("unsupported format %q: must be soulspec", importFrom)
	}
}

// importSoulSpec reads SoulSpec files from dir and creates an ethos identity.
// SOUL.md is required; IDENTITY.md and STYLE.md are optional.
func importSoulSpec(dir, handleOverride string) error {
	// SOUL.md is required.
	soulPath := filepath.Join(dir, "SOUL.md")
	soulData, err := os.ReadFile(soulPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("SOUL.md not found in %s", dir)
		}
		return fmt.Errorf("reading SOUL.md: %w", err)
	}
	soulContent := strings.TrimSpace(string(soulData))
	if soulContent == "" {
		return fmt.Errorf("SOUL.md is empty")
	}

	// Derive name from IDENTITY.md if present.
	name := ""
	identPath := filepath.Join(dir, "IDENTITY.md")
	if data, err := os.ReadFile(identPath); err == nil {
		name = extractHeading(string(data))
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("reading IDENTITY.md: %w", err)
	} else {
		fmt.Fprintln(os.Stderr, "warning: IDENTITY.md not found, using handle for name")
	}

	// Read STYLE.md if present.
	var styleContent string
	stylePath := filepath.Join(dir, "STYLE.md")
	if data, err := os.ReadFile(stylePath); err == nil {
		styleContent = strings.TrimSpace(string(data))
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("reading STYLE.md: %w", err)
	} else {
		fmt.Fprintln(os.Stderr, "warning: STYLE.md not found, skipping writing style")
	}

	// Resolve handle.
	handle := handleOverride
	if handle == "" && name != "" {
		handle = slugify(name)
	}
	if handle == "" {
		return fmt.Errorf("cannot derive handle: provide --handle or include a heading in IDENTITY.md")
	}

	// Use handle as name if IDENTITY.md had no heading.
	if name == "" {
		name = handle
	}

	// Strip leading heading from content to avoid duplication.
	// Export prepends "# Name" to SOUL.md; strip it on import.
	personalityContent := stripLeadingHeading(soulContent)
	if personalityContent == "" {
		return fmt.Errorf("SOUL.md contains only a heading with no content")
	}

	// Save personality attribute.
	is := identityStore()
	persStore := layeredAttributeStore(is, attribute.Personalities)
	persSlug := handle
	if err := persStore.Save(&attribute.Attribute{
		Slug:    persSlug,
		Content: personalityContent,
	}); err != nil {
		return fmt.Errorf("saving personality: %w", err)
	}
	fmt.Printf("created personality %q\n", persSlug)

	// Save writing style attribute if present.
	var styleSlug string
	if styleContent != "" {
		styleSlug = handle
		wsStore := layeredAttributeStore(is, attribute.WritingStyles)
		wsContent := stripLeadingHeading(styleContent)
		if wsContent == "" {
			wsContent = styleContent
		}
		if err := wsStore.Save(&attribute.Attribute{
			Slug:    styleSlug,
			Content: wsContent,
		}); err != nil {
			return fmt.Errorf("saving writing style: %w", err)
		}
		fmt.Printf("created writing style %q\n", styleSlug)
	}

	// Build and save identity.
	id := &identity.Identity{
		Name:         name,
		Handle:       handle,
		Kind:         "agent",
		Personality:  persSlug,
		WritingStyle: styleSlug,
	}
	if err := id.Validate(); err != nil {
		return fmt.Errorf("invalid identity: %w", err)
	}
	if err := is.Save(id); err != nil {
		return fmt.Errorf("saving identity: %w", err)
	}

	fmt.Printf("imported identity %q (%s)\n", handle, name)
	return nil
}

// extractHeading returns the text of the first markdown heading in s.
// Returns empty string if no heading is found.
func extractHeading(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

// stripLeadingHeading removes the first markdown heading line and any
// immediately following blank lines. Returns the remaining content.
func stripLeadingHeading(s string) string {
	lines := strings.Split(s, "\n")
	i := 0
	// Skip leading blank lines.
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	// If the first non-blank line is a heading, skip it.
	if i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "# ") {
		i++
		// Skip blank lines after the heading.
		for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
			i++
		}
	}
	result := strings.TrimSpace(strings.Join(lines[i:], "\n"))
	return result
}
