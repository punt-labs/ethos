package main

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/punt-labs/ethos/internal/hook"
	"github.com/punt-labs/ethos/internal/mission"
	"github.com/spf13/cobra"
)

// missionStore returns the default mission store rooted at
// ~/.punt-labs/ethos. Mirrors sessionStore() — global-only, no layering.
func missionStore() *mission.Store {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}
	return mission.NewStore(filepath.Join(home, ".punt-labs", "ethos"))
}

// --- mission (bare command) ---
//
// missionCmd has no Run — cobra prints help automatically when a command
// with subcommands is invoked with no arguments. This matches the role
// and team command patterns.
var missionCmd = &cobra.Command{
	Use:     "mission",
	Short:   "Manage mission contracts",
	GroupID: "session",
	Args:    cobra.NoArgs,
}

// --- mission create ---

var missionCreateFile string

var missionCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a mission contract from a YAML file",
	Long: `Create a mission contract from a complete YAML file.

All required fields (leader, worker, evaluator, inputs, write_set,
success_criteria, budget) must be present in the file. Unknown fields
are rejected (KnownFields strict decode). Validation runs before the
contract is persisted.`,
	Args: cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runMissionCreate()
	},
}

// --- mission show ---

var missionShowCmd = &cobra.Command{
	Use:   "show <id-or-prefix>",
	Short: "Show mission contract details",
	Long: `Show mission contract details.

Accepts a full mission ID (m-YYYY-MM-DD-NNN) or any unambiguous prefix.
Use --json to emit the raw contract for piping.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runMissionShow(args[0])
	},
}

// --- mission list ---

var missionListStatus string

var missionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List mission contracts",
	Long: `List mission contracts.

Filters by --status (default "open"). Pass --status all to include
closed, failed, and escalated missions alongside open ones. Pass
--json for a machine-readable summary.`,
	Args: cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runMissionList(missionListStatus)
	},
}

// --- mission close ---

var missionCloseStatus string

var missionCloseCmd = &cobra.Command{
	Use:   "close <id-or-prefix>",
	Short: "Close a mission contract",
	Long: `Close a mission contract with a terminal status.

Accepts a full mission ID or unambiguous prefix. Default terminal
status is "closed"; use --status failed or --status escalated for
the other terminal states. The close event is appended to the mission
event log.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runMissionClose(args[0], missionCloseStatus)
	},
}

func init() {
	missionCreateCmd.Flags().StringVarP(&missionCreateFile, "file", "f", "", "Read contract YAML from file (required)")
	_ = missionCreateCmd.MarkFlagRequired("file")

	missionListCmd.Flags().StringVar(&missionListStatus, "status", "open",
		"Filter by status (open|closed|failed|escalated|all)")

	missionCloseCmd.Flags().StringVar(&missionCloseStatus, "status", mission.StatusClosed,
		"Terminal status (closed|failed|escalated)")

	missionCmd.AddCommand(
		missionCreateCmd,
		missionShowCmd,
		missionListCmd,
		missionCloseCmd,
	)
	rootCmd.AddCommand(missionCmd)
}

// runMissionCreate handles `ethos mission create --file <path>`.
//
// There is exactly one creation path: strict YAML decode from a file.
// Flag-only creation was removed in round 2 — it could only produce
// placeholder contracts, which defeats the purpose of the contract as
// a trust boundary.
func runMissionCreate() {
	ms := missionStore()

	data, err := os.ReadFile(missionCreateFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission create: %v\n", err)
		os.Exit(1)
	}

	// Strict decode via the shared helper: unknown fields, multiple
	// documents, and trailing content are all rejected. CLI and MCP
	// share this entry point so the input trust boundary is enforced
	// identically regardless of how the YAML reached the store.
	parsed, err := mission.DecodeContractStrict(data, missionCreateFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission create: %v\n", err)
		os.Exit(1)
	}
	c := *parsed

	// Server-controlled fields. The contract YAML may suggest timestamps
	// or a status; the store is the source of truth.
	now := time.Now().UTC()
	if c.MissionID == "" {
		id, err := mission.NewID(ms.Root(), now)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ethos: mission create: %v\n", err)
			os.Exit(1)
		}
		c.MissionID = id
	}
	c.Status = mission.StatusOpen
	c.CreatedAt = now.Format(time.RFC3339)
	c.UpdatedAt = c.CreatedAt
	// ClosedAt is a terminal-only field. A newly-created mission is
	// always open, so clear anything the YAML supplied. The Validate
	// invariant (open ⇒ closed_at empty) would reject a non-empty
	// value anyway, but clearing it explicitly keeps the trust
	// boundary one-sided: the server owns this field on create.
	c.ClosedAt = ""
	// PinnedAt is server-controlled by definition: the evaluator is
	// pinned AT mission launch. Any value the YAML supplies for
	// pinned_at would mean a mission whose evaluator was pinned before
	// the mission was created — incoherent. Always overwrite.
	c.Evaluator.PinnedAt = c.CreatedAt

	if err := ms.Create(&c); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission create: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		printJSON(&c)
		return
	}
	// Non-JSON mode is silent on success — matches session.go pattern.
}

func runMissionShow(idOrPrefix string) {
	ms := missionStore()
	id, err := ms.MatchByPrefix(idOrPrefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission show: %v\n", err)
		os.Exit(1)
	}
	c, err := ms.Load(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission show: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(c)
		return
	}
	printContract(c)
}

func runMissionList(status string) {
	// Validate the filter at the boundary so `ethos mission list
	// --status bogus` returns an explicit error instead of an empty
	// table. Symmetric with the MCP handler's defense.
	if !mission.IsValidStatusFilter(status) {
		fmt.Fprintf(os.Stderr,
			"ethos: mission list: invalid --status %q: must be one of open, closed, failed, escalated, all\n",
			status)
		os.Exit(1)
	}
	ms := missionStore()
	ids, err := ms.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission list: %v\n", err)
		os.Exit(1)
	}

	type entry struct {
		MissionID string `json:"mission_id"`
		Status    string `json:"status"`
		Leader    string `json:"leader"`
		Worker    string `json:"worker"`
		Evaluator string `json:"evaluator"`
		CreatedAt string `json:"created_at"`
	}

	var entries []entry
	for _, id := range ids {
		c, loadErr := ms.Load(id)
		if loadErr != nil {
			// Include the path in the warning so the operator can jump
			// straight to the corrupt file.
			fmt.Fprintf(os.Stderr, "ethos: warning: %s: %v\n",
				filepath.Join(ms.Root(), "missions", id+".yaml"), loadErr)
			continue
		}
		if !mission.StatusMatches(status, c.Status) {
			continue
		}
		entries = append(entries, entry{
			MissionID: c.MissionID,
			Status:    c.Status,
			Leader:    c.Leader,
			Worker:    c.Worker,
			Evaluator: c.Evaluator.Handle,
			CreatedAt: c.CreatedAt,
		})
	}

	if jsonOutput {
		if entries == nil {
			entries = []entry{}
		}
		printJSON(entries)
		return
	}

	if len(entries) == 0 {
		fmt.Println("No missions found.")
		return
	}

	headers := []string{"MISSION", "STATUS", "LEADER", "WORKER", "EVALUATOR", "CREATED"}
	rows := make([][]string, len(entries))
	for i, e := range entries {
		// Mission IDs are human-scale (16 chars m-YYYY-MM-DD-NNN) and
		// printed in full. Sessions use shortID(...) because their IDs
		// are 36-char UUIDs — the mission case does not need truncation.
		rows[i] = []string{
			e.MissionID,
			e.Status,
			e.Leader,
			e.Worker,
			e.Evaluator,
			formatStarted(e.CreatedAt),
		}
	}
	fmt.Println(hook.FormatTable(headers, rows))
}

func runMissionClose(idOrPrefix, status string) {
	ms := missionStore()
	id, err := ms.MatchByPrefix(idOrPrefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission close: %v\n", err)
		os.Exit(1)
	}
	if err := ms.Close(id, status); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission close: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(map[string]string{"mission_id": id, "status": status})
		return
	}
	// Non-JSON mode is silent on success — matches session.go pattern.
}

// printContract emits a human-readable summary of a contract. The
// header block uses text/tabwriter for aligned field/value columns;
// multi-value sections (write_set, tools, success_criteria) are
// rendered as bullet lists because hook.FormatTable is reserved for
// truly tabular data.
func printContract(c *mission.Contract) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Mission:\t%s\n", c.MissionID)
	fmt.Fprintf(tw, "Status:\t%s\n", c.Status)
	fmt.Fprintf(tw, "Created:\t%s\n", formatStarted(c.CreatedAt))
	if c.UpdatedAt != "" && c.UpdatedAt != c.CreatedAt {
		fmt.Fprintf(tw, "Updated:\t%s\n", formatStarted(c.UpdatedAt))
	}
	if c.ClosedAt != "" {
		fmt.Fprintf(tw, "Closed:\t%s\n", formatStarted(c.ClosedAt))
	}
	if c.Bead != "" {
		fmt.Fprintf(tw, "Bead:\t%s\n", c.Bead)
	}
	fmt.Fprintf(tw, "Leader:\t%s\n", c.Leader)
	fmt.Fprintf(tw, "Worker:\t%s\n", c.Worker)
	// Fold the evaluator's hash inline. The continuation-row pattern
	// (a row that starts with a tab) is fragile in tabwriter — once
	// the hash field exists, the column widths get recomputed in
	// surprising ways. One row, one Evaluator: line.
	pinned := formatStarted(c.Evaluator.PinnedAt)
	evaluatorLine := fmt.Sprintf("%s (pinned %s", c.Evaluator.Handle, pinned)
	if c.Evaluator.Hash != "" {
		evaluatorLine += ", hash " + c.Evaluator.Hash
	}
	evaluatorLine += ")"
	fmt.Fprintf(tw, "Evaluator:\t%s\n", evaluatorLine)
	fmt.Fprintf(tw, "Budget:\t%d round(s), reflection_after_each=%t\n",
		c.Budget.Rounds, c.Budget.ReflectionAfterEach)
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission show: %v\n", err)
		os.Exit(1)
	}

	if len(c.Inputs.Files) > 0 || c.Inputs.Bead != "" || len(c.Inputs.References) > 0 {
		fmt.Println()
		fmt.Println("Inputs:")
		if c.Inputs.Bead != "" {
			fmt.Printf("  bead: %s\n", c.Inputs.Bead)
		}
		for _, f := range c.Inputs.Files {
			fmt.Printf("  file: %s\n", f)
		}
		for _, r := range c.Inputs.References {
			fmt.Printf("  ref:  %s\n", r)
		}
	}

	if len(c.WriteSet) > 0 {
		fmt.Println()
		fmt.Println("Write set:")
		for _, w := range c.WriteSet {
			fmt.Printf("  - %s\n", w)
		}
	}

	if len(c.Tools) > 0 {
		fmt.Println()
		fmt.Println("Tools:")
		for _, t := range c.Tools {
			fmt.Printf("  - %s\n", t)
		}
	}

	if len(c.SuccessCriteria) > 0 {
		fmt.Println()
		fmt.Println("Success criteria:")
		for _, sc := range c.SuccessCriteria {
			fmt.Printf("  - %s\n", sc)
		}
	}

	if c.Context != "" {
		fmt.Println()
		fmt.Println("Context:")
		fmt.Println(c.Context)
	}
}
