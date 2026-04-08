package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/punt-labs/ethos/internal/hook"
	"github.com/punt-labs/ethos/internal/mission"
	"github.com/spf13/cobra"
)

// missionStore returns a bare mission store rooted at
// ~/.punt-labs/ethos. Mirrors sessionStore() — global-only, no
// layering. Used by read-only commands (`mission show`, `list`,
// `close`, `reflect`, `advance`, `reflections`) where the Phase 3.5
// role-overlap check is irrelevant — it fires only at create time.
//
// A read-only command never needs the RoleLister, and wiring one
// here would force every `ethos mission show` to stand up the
// identity, role, and team stores just to print a contract. Worse,
// a broken role fixture would print the role-overlap warning for
// every unrelated read command.
//
// Create paths (CLI `mission create` and MCP `mission create`) go
// through missionStoreForCreate instead, which wires the lister
// and fails loudly on a misconfigured role store.
func missionStore() *mission.Store {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}
	root := filepath.Join(home, ".punt-labs", "ethos")
	return mission.NewStore(root)
}

// missionStoreForCreate returns a mission store with the Phase 3.5
// role-overlap RoleLister wired from the live identity, role, and
// team stores. Used by:
//
//   - `runMissionCreate` — the CLI create path
//   - `serve.go` — the MCP server shares one Store instance across
//     every mission tool method; a `mission create` call made via
//     MCP must see the same role-overlap gate as the CLI
//
// A RoleLister wiring failure is FATAL here: silently degrading
// would let a mis-seeded role store through the gate, which is the
// bug Phase 3.5 exists to prevent. The operator sees an actionable
// error at the create path instead of a silently-disabled check.
func missionStoreForCreate() *mission.Store {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}
	root := filepath.Join(home, ".punt-labs", "ethos")
	ms := mission.NewStore(root)
	is := identityStore()
	sources, err := mission.NewLiveHashSources(is, layeredRoleStore(is), layeredTeamStore(is))
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos: mission: cannot wire role overlap check: %v\n", err)
		os.Exit(1)
	}
	return ms.WithRoleLister(sources.Roles)
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

Required fields: leader, worker, evaluator, write_set,
success_criteria, and budget. Optional fields: inputs, context,
session, repo. Server-controlled fields (mission_id, status,
created_at, updated_at, closed_at, evaluator.pinned_at,
evaluator.hash) are overwritten regardless of what the file supplies.

Unknown fields are rejected (KnownFields strict decode), and
multi-document YAML or trailing content after the first document is
also rejected. Validation runs before the contract is persisted.

Creation also fails if the new contract's write_set overlaps any
currently-open mission's write_set; the error names the blocking
mission(s) and the overlapping path(s).

Creation also fails if the evaluator handle cannot be resolved to a
valid identity with personality, writing style, talent content, and
role assignments; the error names the handle. Use ` + "`ethos identity list`" + `
to see available handles.

Creation also fails if ` + "`worker`" + ` and ` + "`evaluator.handle`" + ` resolve to
the same handle, or if the worker and evaluator are bound to the same
role (after canonicalizing ` + "`team/role`" + ` to ` + "`role`" + `) — the verifier
must not share a role with the worker. To recover, name a different
evaluator handle, or rebind one of the two identities to a distinct
role via ` + "`ethos team add-member`" + `.

budget.rounds is now a hard cap: after round N the operator must
submit a reflection via ` + "`ethos mission reflect`" + ` and advance via
` + "`ethos mission advance`" + ` before beginning round N+1; the round
budget cannot be extended without re-scoping.`,
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

// --- mission result ---

var missionResultFile string

var missionResultCmd = &cobra.Command{
	Use:   "result <id-or-prefix>",
	Short: "Submit a structured worker result for the current round",
	Long: `Submit a structured worker result for the mission's current round.

The result is read from a YAML file containing mission, round, author,
verdict, confidence, files_changed, evidence, and (optionally)
open_questions and prose. The mission and round number must match
the mission's current state; results are append-only and a second
submission for the same round is refused.

verdict must be one of: pass, fail, escalate. confidence must be in
[0.0, 1.0]. evidence must contain at least one entry. Every
files_changed path must live inside the contract's write_set.

Submitting a result is a prerequisite for closing the mission. The
close gate (ethos mission close) refuses the terminal transition
until a valid result exists for the current round.

Examples:

  # Minimal valid result (YAML file body):
  #
  #   mission: m-2026-04-08-005
  #   round: 1
  #   author: bwk
  #   verdict: pass
  #   confidence: 0.95
  #   files_changed:
  #     - path: internal/mission/result.go
  #       added: 120
  #       removed: 0
  #   evidence:
  #     - name: go test ./internal/mission/... -race
  #       status: pass
  #     - name: make check
  #       status: pass
  #
  # Then:
  #   ethos mission result m-2026-04-08-005 --file result.yaml`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runMissionResult(args[0], missionResultFile)
	},
}

// --- mission reflect ---

var missionReflectFile string

var missionReflectCmd = &cobra.Command{
	Use:   "reflect <id-or-prefix>",
	Short: "Submit a structured reflection for the current round",
	Long: `Submit a structured reflection for the mission's current round.

The reflection is read from a YAML file containing round, author,
converging, signals, recommendation, and (when the recommendation is
stop or escalate) reason. The round number must equal the mission's
current round; reflections are append-only and a duplicate is refused.

After reflecting, run "ethos mission advance" to move to the next
round. The advance gate refuses to proceed when the latest
reflection recommends stop or escalate, or when the budget would be
exceeded.

recommendation must be one of: continue, pivot, stop, escalate. The
gate refuses to advance after a stop or escalate. signals must
contain at least one entry.

Examples:

  # Minimal valid reflection (YAML file body):
  #
  #   round: 1
  #   author: claude
  #   converging: true
  #   signals:
  #     - tests passing
  #     - no new lint findings
  #   recommendation: continue
  #   reason: round 1 finished cleanly; round 2 will tackle edge cases
  #
  # Then:
  #   ethos mission reflect m-2026-04-08-005 --file reflection.yaml`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runMissionReflect(args[0], missionReflectFile)
	},
}

// --- mission reflections ---

var missionReflectionsCmd = &cobra.Command{
	Use:   "reflections <id-or-prefix>",
	Short: "Show the round-by-round reflection log",
	Long: `Show the round-by-round reflection log for a mission.

Prints only the round-by-round reflection log for a mission; unlike
"mission show", the contract header is omitted so the output parses
as a single JSON array with --json (always an array, even when there
are no reflections yet — empty rather than null).`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runMissionReflections(args[0])
	},
}

// --- mission results ---

var missionResultsCmd = &cobra.Command{
	Use:   "results <id-or-prefix>",
	Short: "Show the round-by-round result log",
	Long: `Show the round-by-round result log for a mission.

Prints only the round-by-round worker result log for a mission;
unlike "mission show", the contract header is omitted so the output
parses as a single JSON array with --json (always an array, even
when there are no results yet — empty rather than null).

Each result carries the round, verdict, confidence, author,
files_changed, evidence, open_questions, and prose fields. This is
the read-only counterpart to "mission result", mirroring the
reflection/reflections pair.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runMissionResults(args[0])
	},
}

// --- mission advance ---

var missionAdvanceCmd = &cobra.Command{
	Use:   "advance <id-or-prefix>",
	Short: "Advance the mission to the next round",
	Long: `Advance the mission from its current round to the next.

The advance is refused if any of the following hold:
  - the current round has no reflection on file
  - the current round's reflection recommends stop or escalate
  - the mission has exhausted its round budget
  - the mission is in a terminal state

On success, the contract's current_round is bumped and a
round_advanced event is appended to the mission event log.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runMissionAdvance(args[0])
	},
}

func init() {
	missionCreateCmd.Flags().StringVarP(&missionCreateFile, "file", "f", "", "Read contract YAML from file (required)")
	_ = missionCreateCmd.MarkFlagRequired("file")

	missionListCmd.Flags().StringVar(&missionListStatus, "status", "open",
		"Filter by status (open|closed|failed|escalated|all)")

	missionCloseCmd.Flags().StringVar(&missionCloseStatus, "status", mission.StatusClosed,
		"Terminal status (closed|failed|escalated)")

	missionReflectCmd.Flags().StringVarP(&missionReflectFile, "file", "f", "", "Read reflection YAML from file (required)")
	_ = missionReflectCmd.MarkFlagRequired("file")

	missionResultCmd.Flags().StringVarP(&missionResultFile, "file", "f", "", "Read result YAML from file (required)")
	_ = missionResultCmd.MarkFlagRequired("file")

	missionCmd.AddCommand(
		missionCreateCmd,
		missionShowCmd,
		missionListCmd,
		missionCloseCmd,
		missionReflectCmd,
		missionReflectionsCmd,
		missionAdvanceCmd,
		missionResultCmd,
		missionResultsCmd,
	)
	rootCmd.AddCommand(missionCmd)
}

// runMissionCreate handles `ethos mission create --file <path>`.
//
// There is exactly one creation path: strict YAML decode from a file.
// Flag-only creation was removed in round 2 — it could only produce
// placeholder contracts, which defeats the purpose of the contract as
// a trust boundary.
//
// Uses missionStoreForCreate so the Phase 3.5 role-overlap gate
// fires; read-only subcommands use the bare missionStore.
func runMissionCreate() {
	ms := missionStoreForCreate()

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

	// Apply server-controlled fields (mission_id, status, timestamps,
	// evaluator.pinned_at, evaluator.hash). Shared with the MCP create
	// path via Store.ApplyServerFields so any caller-supplied values
	// for these fields are overwritten identically regardless of entry
	// point. The hash sources resolve the evaluator handle through
	// the live identity, role, and team stores; an unresolvable
	// evaluator is fatal — see DES-033.
	is := identityStore()
	sources, err := mission.NewLiveHashSources(is, layeredRoleStore(is), layeredTeamStore(is))
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission create: %v\n", err)
		os.Exit(1)
	}
	if err := ms.ApplyServerFields(&c, time.Now(), sources); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission create: %v\n", err)
		os.Exit(1)
	}

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
		// JSON shape extends the bare contract with a top-level
		// `results` array so a consumer that previously decoded
		// only the contract still decodes the same fields, and a
		// consumer that wants to reconstruct the close verdict
		// does not need a second round trip. Reflections stay out
		// of the payload — fetch via `ethos mission reflections`
		// to keep the show call's wire size bounded by the
		// contract. Round 2 added results because mdm found the
		// operator could not see the verdict that authorized a
		// close without `cat`-ing the YAML file.
		results, _ := ms.LoadResults(id)
		payload := map[string]any{
			"mission_id":      c.MissionID,
			"status":          c.Status,
			"created_at":      c.CreatedAt,
			"updated_at":      c.UpdatedAt,
			"closed_at":       c.ClosedAt,
			"leader":          c.Leader,
			"worker":          c.Worker,
			"evaluator":       c.Evaluator,
			"inputs":          c.Inputs,
			"write_set":       c.WriteSet,
			"tools":           c.Tools,
			"success_criteria": c.SuccessCriteria,
			"budget":          c.Budget,
			"current_round":   c.CurrentRound,
			"context":         c.Context,
			"results":         results,
		}
		if payload["results"] == nil {
			payload["results"] = []mission.Result{}
		}
		printJSON(payload)
		return
	}
	printContract(c)

	// Reflections and results are advisory in show — load them
	// after the contract render so a corrupt sibling file does not
	// block the operator from seeing the contract.
	reflections, err := ms.LoadReflections(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: warning: loading reflections: %v\n", err)
	} else {
		printReflections(reflections)
	}

	results, err := ms.LoadResults(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: warning: loading results: %v\n", err)
		return
	}
	printResults(results)
}

// runMissionReflections handles `ethos mission reflections <id>`,
// the read-only counterpart to `mission reflect`. Returns the
// round-by-round reflection log as a YAML-friendly JSON array (or a
// human-readable bullet list).
func runMissionReflections(idOrPrefix string) {
	ms := missionStore()
	id, err := ms.MatchByPrefix(idOrPrefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission reflections: %v\n", err)
		os.Exit(1)
	}
	rs, err := ms.LoadReflections(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission reflections: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		// Always return an array, never null, so consumers can
		// unmarshal into []Reflection without a nil check.
		if rs == nil {
			rs = []mission.Reflection{}
		}
		printJSON(rs)
		return
	}
	printReflections(rs)
}

// runMissionResults handles `ethos mission results <id>`, the
// read-only counterpart to `mission result`. Returns the
// round-by-round result log as a JSON array (or a human-readable
// block list). Round 2 of Phase 3.6 added this subcommand — MCP
// had both `result` and `results`; the CLI only had `result`, so
// operators could not list results from the command line at all.
// Mirrors runMissionReflections byte-for-byte.
func runMissionResults(idOrPrefix string) {
	ms := missionStore()
	id, err := ms.MatchByPrefix(idOrPrefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission results: %v\n", err)
		os.Exit(1)
	}
	rs, err := ms.LoadResults(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission results: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		// Always return an array, never null, so consumers can
		// unmarshal into []Result without a nil check.
		if rs == nil {
			rs = []mission.Result{}
		}
		printJSON(rs)
		return
	}
	printResults(rs)
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

	entries := []mission.ListEntry{}
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
		entries = append(entries, mission.NewListEntry(c))
	}

	if jsonOutput {
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

// runMissionReflect handles `ethos mission reflect <id> --file <path>`.
//
// The reflection YAML is decoded strictly, validated, and appended
// via Store.AppendReflection. The mission is resolved by ID or
// unambiguous prefix to match the show/close convention. The
// caller's reflection round must equal the mission's current round
// — passing a stale round produces a precise error at submit time
// rather than a vague one at advance time.
func runMissionReflect(idOrPrefix, file string) {
	ms := missionStore()
	id, err := ms.MatchByPrefix(idOrPrefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission reflect: %v\n", err)
		os.Exit(1)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission reflect: %v\n", err)
		os.Exit(1)
	}
	r, err := mission.DecodeReflectionStrict(data, file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission reflect: %v\n", err)
		os.Exit(1)
	}
	if err := ms.AppendReflection(id, r); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission reflect: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(map[string]any{
			"mission_id":     id,
			"round":          r.Round,
			"recommendation": r.Recommendation,
			"created_at":     r.CreatedAt,
		})
		return
	}
	// Non-JSON mode is silent on success — matches session.go pattern.
}

// runMissionResult handles `ethos mission result <id> --file <path>`.
//
// The result YAML is decoded strictly, validated, and appended via
// Store.AppendResult. The mission is resolved by ID or unambiguous
// prefix to match the show/close/reflect convention. The caller's
// result round and mission ID must match the mission's current
// state — passing a stale round or a mismatched mission ID produces
// a precise error at submit time rather than a vague one at close
// time.
func runMissionResult(idOrPrefix, file string) {
	ms := missionStore()
	id, err := ms.MatchByPrefix(idOrPrefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission result: %v\n", err)
		os.Exit(1)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission result: %v\n", err)
		os.Exit(1)
	}
	r, err := mission.DecodeResultStrict(data, file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission result: %v\n", err)
		os.Exit(1)
	}
	// Wrap AppendResult errors with the file path so structural
	// Validate failures — empty verdict, out-of-range confidence,
	// empty evidence — carry the same locator the unknown-field
	// path already includes. Without this wrapper the operator
	// sees "invalid result: invalid verdict" with no hint which
	// file produced it.
	if err := ms.AppendResult(id, r); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission result: %s: %v\n", file, err)
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(map[string]any{
			"mission_id": id,
			"round":      r.Round,
			"verdict":    r.Verdict,
			"confidence": r.Confidence,
			"created_at": r.CreatedAt,
		})
		return
	}
	// Non-JSON mode is silent on success — matches every other
	// mission subcommand.
}

// runMissionAdvance handles `ethos mission advance <id>`. The gate
// refuses to advance when the current round has no reflection, when
// the reflection recommends stop or escalate, or when the budget
// would be exceeded; in all three cases the operator-facing message
// surfaces the reason verbatim.
func runMissionAdvance(idOrPrefix string) {
	ms := missionStore()
	id, err := ms.MatchByPrefix(idOrPrefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission advance: %v\n", err)
		os.Exit(1)
	}
	// Resolve the actor to record on the round_advanced event. A
	// load failure is fatal here — recording an "unknown" actor on
	// the audit trail would pollute the event log with empty
	// attribution and make post-hoc review of who advanced which
	// round impossible.
	actor, err := resolveActor(ms, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission advance: %v\n", err)
		os.Exit(1)
	}
	newRound, err := ms.AdvanceRound(id, actor)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission advance: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(map[string]any{
			"mission_id":    id,
			"current_round": newRound,
		})
		return
	}
	// Non-JSON mode is silent on success — matches every other
	// mission subcommand (create, close, reflect). Exit code 0 tells
	// the story; a chatty success message would be out of family.
	_ = newRound
}

// resolveActor returns the handle to record on a round_advanced
// event. The leader stored in the contract is the right answer for
// 3.4 because every advance is a leader operation; future phases
// may resolve the calling persona via /ethos:whoami.
//
// A load failure is returned to the caller so it can surface a
// concrete error. Falling back to an "unknown" string would pollute
// the audit trail and mask a real problem — an unreadable contract
// should fail loudly, not silently.
func resolveActor(ms *mission.Store, id string) (string, error) {
	c, err := ms.Load(id)
	if err != nil {
		return "", fmt.Errorf("cannot resolve actor for mission %q: %w", id, err)
	}
	leader := strings.TrimSpace(c.Leader)
	if leader == "" {
		return "", fmt.Errorf("cannot resolve actor for mission %q: contract has no leader", id)
	}
	return leader, nil
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
	fmt.Fprintf(tw, "Leader:\t%s\n", c.Leader)
	fmt.Fprintf(tw, "Worker:\t%s\n", c.Worker)
	// Evaluator line carries the handle and the pinned timestamp. The
	// hash goes on its own row so it does not wrap on 80-column
	// terminals — a sha256 hex is 64 characters, which overflows the
	// typical continuation budget.
	pinned := formatStarted(c.Evaluator.PinnedAt)
	fmt.Fprintf(tw, "Evaluator:\t%s (pinned %s)\n", c.Evaluator.Handle, pinned)
	if c.Evaluator.Hash != "" {
		fmt.Fprintf(tw, "Hash:\t%s\n", c.Evaluator.Hash)
	}
	fmt.Fprintf(tw, "Budget:\t%d round(s), reflection_after_each=%t\n",
		c.Budget.Rounds, c.Budget.ReflectionAfterEach)
	fmt.Fprintf(tw, "Round:\t%d of %d\n", c.CurrentRound, c.Budget.Rounds)
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

// printReflections renders the round-by-round reflection log under
// the contract block. Empty input is silent — a fresh mission with
// no reflections does not need a section header. Each reflection is
// rendered as a small block: round number, recommendation, signals,
// and reason (when present), so the operator can read the leader's
// decision history without parsing YAML.
//
// Terminal recommendations (stop, escalate) are uppercased so an
// operator scanning a long reflection log can spot a blocking
// decision at a glance — a lowercase "stop" between two "continue"
// rows is easy to miss.
func printReflections(rs []mission.Reflection) {
	if len(rs) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("Reflections:")
	for _, r := range rs {
		rec := r.Recommendation
		if mission.IsTerminalRecommendation(rec) {
			rec = strings.ToUpper(rec)
		}
		fmt.Printf("  - round %d (%s) by %s — converging=%t\n",
			r.Round, rec, r.Author, r.Converging)
		for _, sig := range r.Signals {
			fmt.Printf("      • %s\n", sig)
		}
		if r.Reason != "" {
			fmt.Printf("      reason: %s\n", r.Reason)
		}
	}
}

// printResults renders the round-by-round result log under the
// contract and reflections blocks. Empty input is silent — a fresh
// mission without a result is not ready for the close gate to care.
// Each result is rendered as a small block: round number, verdict,
// confidence, author, files_changed count, evidence count, and the
// first line of prose (if present) so the operator can read the
// worker's own assessment without parsing YAML.
//
// Round 2 of Phase 3.6 added this — mdm flagged that `mission show`
// on a closed mission printed nothing about the result that
// authorized the close. The typed artifact was invisible to the
// CLI; operators had to `cat` the sibling YAML to see the verdict.
func printResults(rs []mission.Result) {
	if len(rs) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("Results:")
	for _, r := range rs {
		fmt.Printf("  - round %d (%s) by %s — confidence=%.2f\n",
			r.Round, r.Verdict, r.Author, r.Confidence)
		fmt.Printf("      files_changed: %d, evidence: %d\n",
			len(r.FilesChanged), len(r.Evidence))
		if r.Prose != "" {
			// First line of prose only — multi-line narrative is
			// rendered in full by `ethos mission results <id>`,
			// which is the dedicated command.
			line := strings.SplitN(r.Prose, "\n", 2)[0]
			fmt.Printf("      prose: %s\n", line)
		}
	}
}
