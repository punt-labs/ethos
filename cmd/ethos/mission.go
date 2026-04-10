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
event log.

A valid result artifact for the current round is required. The close
gate refuses the terminal transition with "no result artifact for
round N" until the worker has submitted a result for that round.
Submit one with "ethos mission result <id> --file <path>" before
closing; see "ethos mission result --help" for the required YAML shape.`,
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

// --- mission log ---

var (
	missionLogEventFilter string
	missionLogSinceFilter string
)

var missionLogCmd = &cobra.Command{
	Use:   "log <id-or-prefix>",
	Short: "Show the append-only mission event log",
	Long: `Show the append-only event log for a mission.

Prints the round-by-round event log in on-disk order: create,
update, result, reflect, round_advanced, close, and any future
event types the writer grows. Use --json for a machine-readable
payload, --event to filter by type (comma-separated), and --since
to filter by RFC3339 timestamp. Both filters are optional and
AND-composed. An empty --event value (or an omitted flag) returns
all event types.

One corrupt line does not erase the log: the reader returns every
parseable event plus a warnings list naming the failing line
numbers. In human mode the warnings print as a trailing Warnings
section on stdout so a caller piping to a file still sees damage.
In JSON mode the warnings surface as a top-level ` + "`warnings`" + `
field (omitempty when absent).

JSON output shape:
  {"events": [...], "warnings": [...]}
  events is always present (empty array if no matches); warnings
  is omitted when the log is clean. This wrapping departs from
  the bare array shape of mission list/results/reflections because
  warnings must travel with events and a bare array cannot carry
  them.

Event type filter values are forward-compatible — an unknown type
is accepted and simply returns no rows, not a flag-parse error.

Examples:

  ethos mission log m-2026-04-08-006
  ethos mission log m-2026-04-08-006 --json
  ethos mission log m-2026-04-08-006 --event create,close
  ethos mission log m-2026-04-08-006 --since 2026-04-08T00:00:00Z
  ethos mission log m-2026-04-08-006 --event result --since 2026-04-08T12:00:00Z`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runMissionLog(args[0], missionLogEventFilter, missionLogSinceFilter)
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

	missionLogCmd.Flags().StringVar(&missionLogEventFilter, "event", "",
		"Filter by event type (comma-separated, e.g. create,close)")
	missionLogCmd.Flags().StringVar(&missionLogSinceFilter, "since", "",
		"Filter by RFC3339 timestamp (events on or after)")

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
		missionLogCmd,
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
	// Text mode echoes a one-line summary so a scripting caller can
	// tell the write landed without a follow-up `ethos mission show`.
	// Fields mirror the `create` event-log summary in
	// summarizeEventDetails so the CLI echo and the audit log use the
	// same k=v shape. Mission ID leads so it is grep-able and
	// chain-able.
	fmt.Printf("created: %s worker=%s evaluator=%s\n",
		c.MissionID, c.Worker, c.Evaluator.Handle)
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
		// JSON shape wraps the contract in ShowPayload: the
		// contract's own json tags drive serialization (so
		// `session`, `repo`, and every omitempty field behave
		// exactly as they would on a bare contract), plus a
		// top-level `results` array and an optional `warnings`
		// field. Round 2 hand-rolled a map and silently dropped
		// session/repo; the struct embedding keeps CLI and MCP
		// in lockstep and auto-propagates any future Contract
		// field to both surfaces.
		results, loadErr := ms.LoadResults(id)
		if results == nil {
			// Pre-initialize BEFORE constructing the payload so
			// JSON emits `[]`, not `null`. A typed-nil
			// []mission.Result slice still marshals as `null`
			// through struct embedding — the empty-slice fix
			// and the struct-embedding fix are complementary,
			// not alternatives.
			results = []mission.Result{}
		}
		payload := mission.ShowPayload{Contract: c, Results: results}
		if loadErr != nil {
			// Surface the load failure on stderr for human
			// operators AND in the JSON warnings field for
			// scriptability. A corrupt sibling file must not
			// be indistinguishable from "no result submitted".
			fmt.Fprintf(os.Stderr, "ethos: warning: loading results: %v\n", loadErr)
			payload.Warnings = append(payload.Warnings,
				fmt.Sprintf("loading results: %v", loadErr))
		}
		printJSON(payload)
		return
	}
	printContract(c)

	// Reflections and results are advisory in show — load them
	// after the contract render so a corrupt sibling file does not
	// block the operator from seeing the contract. Both sections
	// render their header + `(none)` marker unconditionally so an
	// operator piping `show` through `less` never loses the signal
	// on stdout; the stderr warning carries the load failure. Round
	// 4 fixed the Results case (mdm N1); round 6 closed the parallel
	// miss for Reflections (Bugbot).
	reflections, err := ms.LoadReflections(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: warning: loading reflections: %v\n", err)
	}
	printReflections(reflections)

	results, err := ms.LoadResults(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: warning: loading results: %v\n", err)
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
	// Surface the round and verdict that authorized the close so a
	// scripting caller does not need a follow-up `mission log` to
	// learn which result satisfied the gate. Close does not touch
	// CurrentRound, so the contract's current round after close is
	// the same round checkResultGateLocked matched against.
	c, err := ms.Load(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission close: loading closed contract: %v\n", err)
		os.Exit(1)
	}
	r, err := ms.LoadResult(id, c.CurrentRound)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission close: loading satisfying result: %v\n", err)
		os.Exit(1)
	}
	// LoadResult returns (nil, nil) when no result matches the
	// requested round. Close's gate guarantees a matching result
	// existed at the moment the lock was held, but the lock is
	// released before LoadResult runs — a concurrent process (or a
	// filesystem fault) could remove the .results.yaml file in
	// between. Guard the echo path so that race produces a clean
	// diagnostic instead of a nil-pointer panic.
	if r == nil {
		fmt.Fprintf(os.Stderr,
			"ethos: mission close: result for round %d of mission %q disappeared after close; re-run `ethos mission show %s` to inspect state\n",
			c.CurrentRound, id, id)
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(map[string]any{
			"mission_id": id,
			"round":      r.Round,
			"verdict":    r.Verdict,
			"status":     status,
		})
		return
	}
	// Text mode echoes a one-line summary; round, verdict, and
	// status mirror the close event-log summary in
	// summarizeEventDetails so CLI echo and audit log read the same.
	fmt.Printf("closed: %s round=%d verdict=%s status=%s\n",
		id, r.Round, r.Verdict, status)
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
	// Text mode echoes a one-line summary; the rec= tag matches the
	// reflect event-log summary in summarizeEventDetails.
	fmt.Printf("reflected: %s round=%d rec=%s\n",
		id, r.Round, r.Recommendation)
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
	// Text mode echoes a one-line summary; round and verdict mirror
	// the result event-log summary in summarizeEventDetails.
	fmt.Printf("result: %s round=%d verdict=%s\n",
		id, r.Round, r.Verdict)
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
		// Surface both endpoints of the round transition so the JSON
		// shape carries the same information as the text echo and
		// the round_advanced event-log entry. from_round and
		// to_round match the event-log field names.
		printJSON(map[string]any{
			"mission_id":    id,
			"from_round":    newRound - 1,
			"to_round":      newRound,
			"current_round": newRound,
		})
		return
	}
	// Text mode echoes the round transition; format mirrors the
	// round_advanced event-log summary in summarizeEventDetails so
	// CLI echo and audit log read the same.
	fmt.Printf("advanced: %s round %d -> %d\n", id, newRound-1, newRound)
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
// the contract block. Empty input renders "Reflections: (none)" so
// an operator running `mission show` on a fresh mission — or on one
// whose sibling `.reflections.yaml` failed to load — sees the
// section exists but has no entries. Each reflection is rendered as
// a small block: round number, recommendation, signals, and reason
// (when present), so the operator can read the leader's decision
// history without parsing YAML.
//
// Terminal recommendations (stop, escalate) are uppercased so an
// operator scanning a long reflection log can spot a blocking
// decision at a glance — a lowercase "stop" between two "continue"
// rows is easy to miss.
//
// Round 6 of Phase 3.6 added the empty-state marker, parallel to
// the round-3 E1 fix for printResults. Bugbot caught the Reflections
// case when round 4 fixed only the Results side of the pair.
func printReflections(rs []mission.Reflection) {
	fmt.Println()
	fmt.Println("Reflections:")
	if len(rs) == 0 {
		fmt.Println("  (none)")
		return
	}
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

// runMissionLog handles `ethos mission log <id> [flags]`, the
// read-only post-mortem surface for the append-only mission event
// log. The event log is JSONL, so corrupt lines are surfaced as
// warnings rather than fatal errors — one partially-damaged line
// must not erase the rest of the audit trail.
//
// In JSON mode the output is a LogPayload struct: events slice
// plus an optional warnings slice. Empty state is `[]` (never
// `null`) so scripted consumers can decode into []Event without a
// nil guard. In human mode the events render as one-per-line with
// timestamp, actor, type, and a short payload summary; warnings
// render as an in-band "Warnings:" footer on stdout so a caller
// piping the output to a file still sees damage.
//
// Both filter flags are optional and AND-composed. `--event`
// accepts a comma-separated list; unknown types are not rejected
// because event types are forward-compatible (future phases will
// add new ones without a reader change). `--since` is RFC3339;
// an invalid value is a fatal flag-parse error so the operator
// sees it immediately.
func runMissionLog(idOrPrefix, eventFilter, sinceFilter string) {
	ms := missionStore()
	id, err := ms.MatchByPrefix(idOrPrefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission log: %v\n", err)
		os.Exit(1)
	}
	events, warnings, err := ms.LoadEvents(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission log: %v\n", err)
		os.Exit(1)
	}
	types := parseEventTypes(eventFilter)
	filtered, err := mission.FilterEvents(events, types, sinceFilter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: mission log: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		// Always return a non-nil slice so the payload serializes
		// as `"events": []` instead of `"events": null`.
		if filtered == nil {
			filtered = []mission.Event{}
		}
		payload := mission.LogPayload{Events: filtered, Warnings: warnings}
		printJSON(payload)
		return
	}
	// Human mode: events first, then a Warnings footer on stdout
	// so an operator piping `ethos mission log <id> > events.txt`
	// still sees the damage. Round 1 routed warnings to stderr
	// only, which hid corruption from any stdout-only consumer —
	// exactly the silent failure silent-failure-hunter flagged.
	// The footer format matches the MCP walker's convention in
	// internal/hook/format_output.go: a blank line separator,
	// `Warnings:` header, one `  - <warning>` bullet per entry.
	printEventLog(filtered)
	printEventWarnings(warnings)
}

// printEventWarnings emits a trailing Warnings section for the
// human-mode mission log output. The section is omitted on a
// clean log (nil or empty warnings slice). The format mirrors
// the MCP walker in internal/hook/format_output.go so post-mortem
// tooling that scrapes either surface sees the same shape.
func printEventWarnings(warnings []string) {
	if len(warnings) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("Warnings:")
	for _, w := range warnings {
		fmt.Printf("  - %s\n", w)
	}
}

// parseEventTypes splits a comma-separated --event flag into
// trimmed, non-empty slugs. Returns nil for an empty string so
// FilterEvents treats the filter as absent (include all types).
//
// mirror: internal/mcp/mission_tools.go parseEventTypeList — the
// MCP package cannot import cmd/ethos and hoisting into
// internal/mission would drag string-list parsing into the
// trust-boundary package. Round 2 (K1): the two copies stay in
// lockstep via explicit cross-reference comments; add or remove
// in both places.
func parseEventTypes(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// printEventLog renders the event slice as one bullet per event:
//
//	  - <local time>  <type>  by <actor>  <short details>
//
// Empty input renders "Events: (none)" so an operator running the
// command on a brand-new mission — or a mission whose log has been
// filtered to zero rows — sees the section exists but is empty.
// The short-details column picks the two or three fields that
// matter for the current event type; anything else is elided to
// keep the column narrow. Full payload is visible via --json.
//
// The leading "  - " dash matches the MCP formatter walker in
// internal/hook/format_output.go and the sibling subcommands
// (mission show, mission results, mission reflections). Round 1
// shipped without the dash; round 2 aligns the prefix so every
// mission-family subcommand renders the same bullet shape.
func printEventLog(events []mission.Event) {
	fmt.Println("Events:")
	if len(events) == 0 {
		fmt.Println("  (none)")
		return
	}
	for _, e := range events {
		ts := hook.FormatLocalTime(e.TS)
		details := summarizeEventDetails(e)
		if details == "" {
			fmt.Printf("  - %s  %s  by %s\n", ts, e.Event, e.Actor)
		} else {
			fmt.Printf("  - %s  %s  by %s  %s\n", ts, e.Event, e.Actor, details)
		}
	}
}

// summarizeEventDetails extracts a short human-readable payload
// summary for an event. Each known event type picks the two or
// three fields the operator actually wants to see at a glance; an
// unknown event type returns an empty string so the event row
// still renders cleanly. The full payload is always available via
// --json — this helper only decides what to show in the one-line
// human rendering.
func summarizeEventDetails(e mission.Event) string {
	if len(e.Details) == 0 {
		return ""
	}
	switch e.Event {
	case "create":
		worker, _ := e.Details["worker"].(string)
		evaluator, _ := e.Details["evaluator"].(string)
		bead, _ := e.Details["bead"].(string)
		parts := []string{}
		if worker != "" {
			parts = append(parts, "worker="+worker)
		}
		if evaluator != "" {
			parts = append(parts, "evaluator="+evaluator)
		}
		if bead != "" {
			parts = append(parts, "bead="+bead)
		}
		return strings.Join(parts, " ")
	case "close":
		status, _ := e.Details["status"].(string)
		verdict, _ := e.Details["verdict"].(string)
		round, _ := e.Details["round"].(float64)
		// round may come in as int or float64 depending on whether
		// the event was decoded from JSON or constructed in-process;
		// the json.Unmarshal path always produces float64.
		if roundInt, ok := e.Details["round"].(int); ok {
			round = float64(roundInt)
		}
		parts := []string{}
		if status != "" {
			parts = append(parts, "status="+status)
		}
		if verdict != "" {
			parts = append(parts, "verdict="+verdict)
		}
		if round > 0 {
			parts = append(parts, fmt.Sprintf("round=%d", int(round)))
		}
		return strings.Join(parts, " ")
	case "result":
		verdict, _ := e.Details["verdict"].(string)
		round, _ := e.Details["round"].(float64)
		if roundInt, ok := e.Details["round"].(int); ok {
			round = float64(roundInt)
		}
		parts := []string{}
		if round > 0 {
			parts = append(parts, fmt.Sprintf("round=%d", int(round)))
		}
		if verdict != "" {
			parts = append(parts, "verdict="+verdict)
		}
		return strings.Join(parts, " ")
	case "reflect":
		rec, _ := e.Details["recommendation"].(string)
		round, _ := e.Details["round"].(float64)
		if roundInt, ok := e.Details["round"].(int); ok {
			round = float64(roundInt)
		}
		parts := []string{}
		if round > 0 {
			parts = append(parts, fmt.Sprintf("round=%d", int(round)))
		}
		if rec != "" {
			parts = append(parts, "rec="+rec)
		}
		return strings.Join(parts, " ")
	case "round_advanced":
		from, _ := e.Details["from_round"].(float64)
		to, _ := e.Details["to_round"].(float64)
		if fromInt, ok := e.Details["from_round"].(int); ok {
			from = float64(fromInt)
		}
		if toInt, ok := e.Details["to_round"].(int); ok {
			to = float64(toInt)
		}
		if from > 0 && to > 0 {
			return fmt.Sprintf("round %d -> %d", int(from), int(to))
		}
		return ""
	default:
		// Unknown event type — render no details so forward-
		// compatible event types still produce a clean row.
		return ""
	}
}

// printResults renders the round-by-round result log under the
// contract and reflections blocks. Each result is rendered as a
// small block: round number, verdict, confidence, author,
// files_changed count, evidence count, and the first line of prose
// (if present) so the operator can read the worker's own assessment
// without parsing YAML.
//
// Empty input renders "Results: (none)" so an operator running
// `mission show` on a fresh mission sees the section exists but
// has no entries yet. Round 2 of Phase 3.6 added the section;
// round 3 added the empty-state marker so the operator does not
// mistake silence for "no results expected".
//
// Round 2 of Phase 3.6 added this — mdm flagged that `mission show`
// on a closed mission printed nothing about the result that
// authorized the close. The typed artifact was invisible to the
// CLI; operators had to `cat` the sibling YAML to see the verdict.
func printResults(rs []mission.Result) {
	fmt.Println()
	fmt.Println("Results:")
	if len(rs) == 0 {
		fmt.Println("  (none)")
		return
	}
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
