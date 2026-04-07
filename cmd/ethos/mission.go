package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/punt-labs/ethos/internal/hook"
	"github.com/punt-labs/ethos/internal/mission"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// missionStore returns the default mission store rooted at
// ~/.punt-labs/ethos. Mirrors sessionStore() — global-only, no layering.
func missionStore() *mission.Store {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}
	return mission.NewStore(home + "/.punt-labs/ethos")
}

// --- mission ---

var missionCmd = &cobra.Command{
	Use:     "mission",
	Short:   "Manage mission contracts",
	GroupID: "session",
	Args:    cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runMissionList(missionListStatus)
	},
}

// --- mission create ---

var (
	missionCreateFile      string
	missionCreateLeader    string
	missionCreateWorker    string
	missionCreateEvaluator string
	missionCreateBead      string
	missionCreateRounds    int
)

var missionCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new mission contract",
	Long: `Create a new mission contract.

The contract is the typed delegation artifact: leader, worker, evaluator,
write_set, tools, success_criteria, and budget. The recommended path is
to write the contract as YAML and pass --file; flag-based creation is a
fallback for the simplest cases.`,
	Args: cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runMissionCreate()
	},
}

// --- mission show ---

var missionShowCmd = &cobra.Command{
	Use:   "show <id-or-prefix>",
	Short: "Show mission contract details",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runMissionShow(args[0])
	},
}

// --- mission list ---

var missionListStatus string

var missionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List mission contracts",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runMissionList(missionListStatus)
	},
}

// --- mission close ---

var missionCloseStatus string

var missionCloseCmd = &cobra.Command{
	Use:   "close <id-or-prefix>",
	Short: "Close a mission contract",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runMissionClose(args[0], missionCloseStatus)
	},
}

func init() {
	missionCreateCmd.Flags().StringVarP(&missionCreateFile, "file", "f", "", "Read full contract YAML from file")
	missionCreateCmd.Flags().StringVar(&missionCreateLeader, "leader", "", "Leader handle")
	missionCreateCmd.Flags().StringVar(&missionCreateWorker, "worker", "", "Worker handle")
	missionCreateCmd.Flags().StringVar(&missionCreateEvaluator, "evaluator", "", "Evaluator handle")
	missionCreateCmd.Flags().StringVar(&missionCreateBead, "bead", "", "Bead ID")
	missionCreateCmd.Flags().IntVar(&missionCreateRounds, "rounds", 3, "Round budget")

	missionListCmd.Flags().StringVar(&missionListStatus, "status", "open", "Filter by status: open, closed, all")
	missionCloseCmd.Flags().StringVar(&missionCloseStatus, "status", mission.StatusClosed, "Terminal status: closed, failed, escalated")

	missionCmd.AddCommand(
		missionCreateCmd,
		missionShowCmd,
		missionListCmd,
		missionCloseCmd,
	)
	rootCmd.AddCommand(missionCmd)
}

// runMissionCreate handles `ethos mission create`. Two paths: file-driven
// or flag-driven. The file path is recommended for anything beyond the
// simplest contracts; flags exist so the COO can spike a quick mission
// from the command line.
func runMissionCreate() {
	ms := missionStore()

	var c mission.Contract
	if missionCreateFile != "" {
		data, err := os.ReadFile(missionCreateFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
			os.Exit(1)
		}
		// Strict unmarshal: unknown fields would mask typos in the
		// contract YAML and silently degrade safety. yaml.v3 returns
		// a parse error for malformed input; KnownFields makes
		// unrecognized keys an error too.
		dec := yaml.NewDecoder(strings.NewReader(string(data)))
		dec.KnownFields(true)
		if err := dec.Decode(&c); err != nil {
			fmt.Fprintf(os.Stderr, "ethos: parsing contract file: %v\n", err)
			os.Exit(1)
		}
	} else {
		if missionCreateLeader == "" || missionCreateWorker == "" || missionCreateEvaluator == "" {
			fmt.Fprintf(os.Stderr, "ethos: --leader, --worker, and --evaluator are required when --file is not given\n")
			os.Exit(1)
		}
		c = mission.Contract{
			Leader:          missionCreateLeader,
			Worker:          missionCreateWorker,
			SuccessCriteria: []string{"placeholder — replace via show/edit"},
			WriteSet:        []string{"placeholder/"},
			Bead:            missionCreateBead,
			Evaluator: mission.Evaluator{
				Handle: missionCreateEvaluator,
			},
			Budget: mission.Budget{
				Rounds:              missionCreateRounds,
				ReflectionAfterEach: true,
			},
		}
	}

	// Always force authoritative server-side fields. The contract YAML may
	// suggest a status or timestamps; the store is the source of truth.
	now := time.Now().UTC()
	if c.MissionID == "" {
		id, err := mission.NewID(ms.Root(), now)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
			os.Exit(1)
		}
		c.MissionID = id
	}
	c.Status = mission.StatusOpen
	c.CreatedAt = now.Format(time.RFC3339)
	c.UpdatedAt = c.CreatedAt
	if c.Evaluator.PinnedAt == "" {
		c.Evaluator.PinnedAt = c.CreatedAt
	}

	if err := ms.Create(&c); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		printJSON(&c)
		return
	}
	fmt.Printf("Created mission %s\n", c.MissionID)
}

func runMissionShow(idOrPrefix string) {
	ms := missionStore()
	id, err := ms.MatchByPrefix(idOrPrefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	c, err := ms.Load(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(c)
		return
	}
	printContract(c)
}

func runMissionList(status string) {
	ms := missionStore()
	ids, err := ms.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
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
			fmt.Fprintf(os.Stderr, "ethos: warning: mission %s: %v\n", id, loadErr)
			continue
		}
		if !statusMatches(status, c.Status) {
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
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if err := ms.Close(id, status); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(map[string]string{"mission_id": id, "status": status})
		return
	}
	fmt.Printf("Closed mission %s as %s\n", id, status)
}

// statusMatches returns true if the contract status passes the filter.
// "all" matches everything; the default ("open") matches only open
// missions; any other value is treated as an exact match.
func statusMatches(filter, contractStatus string) bool {
	if filter == "" || filter == "all" {
		return true
	}
	return filter == contractStatus
}

// printContract emits a human-readable summary of a contract.
func printContract(c *mission.Contract) {
	fmt.Printf("Mission:   %s\n", c.MissionID)
	fmt.Printf("Status:    %s\n", c.Status)
	fmt.Printf("Created:   %s\n", formatStarted(c.CreatedAt))
	if c.UpdatedAt != "" && c.UpdatedAt != c.CreatedAt {
		fmt.Printf("Updated:   %s\n", formatStarted(c.UpdatedAt))
	}
	if c.ClosedAt != "" {
		fmt.Printf("Closed:    %s\n", formatStarted(c.ClosedAt))
	}
	if c.Bead != "" {
		fmt.Printf("Bead:      %s\n", c.Bead)
	}
	fmt.Println()
	fmt.Printf("Leader:    %s\n", c.Leader)
	fmt.Printf("Worker:    %s\n", c.Worker)
	fmt.Printf("Evaluator: %s (pinned %s)\n", c.Evaluator.Handle, formatStarted(c.Evaluator.PinnedAt))
	if c.Evaluator.Hash != "" {
		fmt.Printf("           hash %s\n", c.Evaluator.Hash)
	}
	fmt.Println()
	fmt.Printf("Budget:    %d round(s), reflection_after_each=%t\n", c.Budget.Rounds, c.Budget.ReflectionAfterEach)

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
		fmt.Printf("Tools: %v\n", c.Tools)
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
