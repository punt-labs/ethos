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
func adrStore() *adr.Store {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}
	return adr.NewStore(filepath.Join(home, ".punt-labs", "ethos", "adrs"))
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
	Run: func(cmd *cobra.Command, args []string) {
		runADRCreate()
	},
}

// --- adr list ---

var adrListStatus string

var adrListCmd = &cobra.Command{
	Use:   "list",
	Short: "List ADRs",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runADRList()
	},
}

// --- adr show ---

var adrShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show ADR details",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runADRShow(args[0])
	},
}

// --- adr settle ---

var adrSettleCmd = &cobra.Command{
	Use:   "settle <id>",
	Short: "Transition ADR status to settled",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runADRSettle(args[0])
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

func runADRCreate() {
	if adrCreateTitle == "" {
		fmt.Fprintf(os.Stderr, "ethos: --title is required\n")
		os.Exit(1)
	}
	if adrCreateDecision == "" {
		fmt.Fprintf(os.Stderr, "ethos: --decision is required\n")
		os.Exit(1)
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

	s := adrStore()
	if err := s.Create(a); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		printJSON(a)
		return
	}
	fmt.Printf("Created %s: %s\n", a.ID, a.Title)
}

func runADRList() {
	if err := adr.ValidateStatusFilter(adrListStatus); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}

	s := adrStore()
	ids, err := s.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}

	// Load all, filter by status.
	var adrs []*adr.ADR
	for _, id := range ids {
		a, err := s.Load(id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ethos: loading %s: %v\n", id, err)
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
		printJSON(adrs)
		return
	}

	if len(adrs) == 0 {
		fmt.Println("No ADRs found.")
		return
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
	fmt.Println(hook.FormatTable(headers, rows))
}

func runADRShow(id string) {
	s := adrStore()
	a, err := s.Load(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		printJSON(a)
		return
	}

	fmt.Printf("ID:       %s\n", a.ID)
	fmt.Printf("Title:    %s\n", a.Title)
	fmt.Printf("Status:   %s\n", a.Status)
	fmt.Printf("Author:   %s\n", a.Author)
	fmt.Printf("Created:  %s\n", hook.FormatLocalTime(a.CreatedAt))
	fmt.Printf("Updated:  %s\n", hook.FormatLocalTime(a.UpdatedAt))
	if a.MissionID != "" {
		fmt.Printf("Mission:  %s\n", a.MissionID)
	}
	if a.BeadID != "" {
		fmt.Printf("Bead:     %s\n", a.BeadID)
	}
	fmt.Println()
	if a.Context != "" {
		fmt.Println("Context:")
		fmt.Printf("  %s\n", a.Context)
		fmt.Println()
	}
	fmt.Println("Decision:")
	fmt.Printf("  %s\n", a.Decision)
	if len(a.Alternatives) > 0 {
		fmt.Println()
		fmt.Println("Alternatives:")
		for _, alt := range a.Alternatives {
			fmt.Printf("  - %s\n", alt)
		}
	}
}

func runADRSettle(id string) {
	s := adrStore()
	err := s.Update(id, func(a *adr.ADR) {
		a.Status = adr.StatusSettled
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		a, err := s.Load(id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
			os.Exit(1)
		}
		printJSON(a)
		return
	}
	fmt.Printf("Settled %s\n", id)
}
