package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/internal/adr"
	"github.com/punt-labs/ethos/internal/hook"
	"github.com/punt-labs/ethos/internal/resolve"

	"github.com/spf13/cobra"
)

// adrStore returns the global ADR store rooted at ~/.punt-labs/ethos/adrs.
func adrStore() (*adr.Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}
	return adr.NewStore(filepath.Join(home, ".punt-labs", "ethos", "adrs")), nil
}

// --- adr (bare command) ---

var adrCmd = &cobra.Command{
	Use:     "adr",
	Short:   "Manage architecture decision records",
	GroupID: "admin",
	Args:    cobra.NoArgs,
}

// --- adr create ---

var (
	adrCreateTitle     string
	adrCreateContext    string
	adrCreateDecision  string
	adrCreateStatus    string
	adrCreateAuthor    string
	adrCreateMissionID string
	adrCreateBeadID    string
)

var adrCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new ADR",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runADRCreate(cmd)
	},
}

// --- adr list ---

var adrListStatus string

var adrListCmd = &cobra.Command{
	Use:   "list",
	Short: "List ADRs",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runADRList(cmd)
	},
}

// --- adr show ---

var adrShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show ADR details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runADRShow(cmd, args[0])
	},
}

// --- adr settle ---

var adrSettleCmd = &cobra.Command{
	Use:   "settle <id>",
	Short: "Transition ADR status to settled",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runADRSettle(cmd, args[0])
	},
}

func init() {
	adrCreateCmd.Flags().StringVar(&adrCreateTitle, "title", "", "ADR title (required)")
	adrCreateCmd.Flags().StringVar(&adrCreateContext, "context", "", "What prompted the decision")
	adrCreateCmd.Flags().StringVar(&adrCreateDecision, "decision", "", "What was decided (required)")
	adrCreateCmd.Flags().StringVar(&adrCreateStatus, "status", "proposed", "Initial status")
	adrCreateCmd.Flags().StringVar(&adrCreateAuthor, "author", "", "Author handle (defaults to session identity)")
	adrCreateCmd.Flags().StringVar(&adrCreateMissionID, "mission-id", "", "Link to mission")
	adrCreateCmd.Flags().StringVar(&adrCreateBeadID, "bead-id", "", "Link to bead")

	adrListCmd.Flags().StringVar(&adrListStatus, "status", "all", "Filter by status (proposed|settled|superseded|all)")

	adrCmd.AddCommand(adrCreateCmd, adrListCmd, adrShowCmd, adrSettleCmd)
	rootCmd.AddCommand(adrCmd)
}

func runADRCreate(cmd *cobra.Command) error {
	if adrCreateTitle == "" {
		return fmt.Errorf("--title is required")
	}
	if adrCreateDecision == "" {
		return fmt.Errorf("--decision is required")
	}

	author := adrCreateAuthor
	if author == "" {
		// Try to resolve from session/git.
		is := identityStore()
		ss := sessionStore()
		h, err := resolve.Resolve(is, ss)
		if err == nil {
			author = h
		}
	}

	a := &adr.ADR{
		Title:     adrCreateTitle,
		Status:    adrCreateStatus,
		Author:    author,
		Context:   adrCreateContext,
		Decision:  adrCreateDecision,
		MissionID: adrCreateMissionID,
		BeadID:    adrCreateBeadID,
	}

	s, err := adrStore()
	if err != nil {
		return err
	}
	if err := s.Create(a); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if jsonOutput {
		return writeJSON(out, a)
	}
	fmt.Fprintf(out, "Created %s: %s\n", a.ID, a.Title)
	return nil
}

func runADRList(cmd *cobra.Command) error {
	if err := adr.ValidateStatusFilter(adrListStatus); err != nil {
		return err
	}

	s, err := adrStore()
	if err != nil {
		return err
	}
	ids, err := s.List()
	if err != nil {
		return err
	}

	// Load all, filter by status.
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()
	var adrs []*adr.ADR
	for _, id := range ids {
		a, err := s.Load(id)
		if err != nil {
			fmt.Fprintf(errOut, "ethos: loading %s: %v\n", id, err)
			continue
		}
		if adrListStatus != "all" && a.Status != adrListStatus {
			continue
		}
		adrs = append(adrs, a)
	}

	if jsonOutput {
		if adrs == nil {
			adrs = []*adr.ADR{}
		}
		return writeJSON(out, adrs)
	}

	if len(adrs) == 0 {
		fmt.Fprintln(out, "No ADRs found.")
		return nil
	}

	headers := []string{"ID", "STATUS", "TITLE", "AUTHOR"}
	rows := make([][]string, len(adrs))
	for i, a := range adrs {
		author := a.Author
		if author == "" {
			author = "-"
		}
		rows[i] = []string{a.ID, a.Status, a.Title, author}
	}
	fmt.Fprintln(out, hook.FormatTable(headers, rows))
	return nil
}

func runADRShow(cmd *cobra.Command, id string) error {
	s, err := adrStore()
	if err != nil {
		return err
	}
	a, err := s.Load(id)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if jsonOutput {
		return writeJSON(out, a)
	}

	fmt.Fprintf(out, "ID:       %s\n", a.ID)
	fmt.Fprintf(out, "Title:    %s\n", a.Title)
	fmt.Fprintf(out, "Status:   %s\n", a.Status)
	fmt.Fprintf(out, "Author:   %s\n", a.Author)
	fmt.Fprintf(out, "Created:  %s\n", hook.FormatLocalTime(a.CreatedAt))
	fmt.Fprintf(out, "Updated:  %s\n", hook.FormatLocalTime(a.UpdatedAt))
	if a.MissionID != "" {
		fmt.Fprintf(out, "Mission:  %s\n", a.MissionID)
	}
	if a.BeadID != "" {
		fmt.Fprintf(out, "Bead:     %s\n", a.BeadID)
	}
	fmt.Fprintln(out)
	if a.Context != "" {
		fmt.Fprintln(out, "Context:")
		fmt.Fprintf(out, "  %s\n", a.Context)
		fmt.Fprintln(out)
	}
	fmt.Fprintln(out, "Decision:")
	fmt.Fprintf(out, "  %s\n", a.Decision)
	if len(a.Alternatives) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Alternatives:")
		for _, alt := range a.Alternatives {
			fmt.Fprintf(out, "  - %s\n", alt)
		}
	}
	return nil
}

func runADRSettle(cmd *cobra.Command, id string) error {
	s, err := adrStore()
	if err != nil {
		return err
	}
	if err := s.Update(id, func(a *adr.ADR) {
		a.Status = adr.StatusSettled
	}); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if jsonOutput {
		a, err := s.Load(id)
		if err != nil {
			return err
		}
		return writeJSON(out, a)
	}
	fmt.Fprintf(out, "Settled %s\n", id)
	return nil
}
