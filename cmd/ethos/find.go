package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/punt-labs/ethos/internal/hook"
	"github.com/punt-labs/ethos/internal/mission"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/spf13/cobra"
)

// --- find (parent command) ---

var findCmd = &cobra.Command{
	Use:     "find",
	Short:   "Query ethos state files",
	GroupID: "admin",
	Args:    cobra.NoArgs,
}

// --- find missions ---

var (
	findMissionsSince  string
	findMissionsWorker string
	findMissionsStatus string
	findMissionsFormat string
)

var findMissionsCmd = &cobra.Command{
	Use:   "missions",
	Short: "Query the missions trace log",
	Long: `Query the missions trace log at <repo>/.punt-labs/ethos/missions.jsonl.

Reads the flat JSONL index that Store.Close appends to on every mission
close. Each line is a TraceSummary with id, status, leader, worker,
evaluator, created_at, closed_at, verdict, and other contract metadata.

Filters are AND-composed: --since DATE, --worker HANDLE, --status STATUS.
All default to unset (include everything).

Output modes:
  --format json    one JSONL line per mission (default, same shape as missions.jsonl)
  --format table   human-readable columns
  --format paths   one line per mission directory path for shell piping

When missions.jsonl does not exist, the command prints nothing on stdout
and exits 0.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFindMissions(cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func init() {
	findMissionsCmd.Flags().StringVar(&findMissionsSince, "since", "",
		"Include missions created on or after DATE (RFC3339 or YYYY-MM-DD)")
	findMissionsCmd.Flags().StringVar(&findMissionsWorker, "worker", "",
		"Filter by worker handle")
	findMissionsCmd.Flags().StringVar(&findMissionsStatus, "status", "",
		"Filter by status (open, closed, failed, escalated)")
	findMissionsCmd.Flags().StringVar(&findMissionsFormat, "format", "json",
		"Output format: json, table, or paths")

	findCmd.AddCommand(findMissionsCmd)
	rootCmd.AddCommand(findCmd)
}

// runFindMissions reads missions.jsonl, applies filters, and writes
// the result to out in the requested format.
func runFindMissions(out, errOut io.Writer) error {
	repoRoot := resolve.EnvRepoRoot()
	if repoRoot == "" {
		fmt.Fprintln(errOut, "ethos: find missions must run inside a repo")
		return usageError{}
	}

	switch findMissionsFormat {
	case "json", "table", "paths":
	default:
		fmt.Fprintf(errOut, "ethos: --format must be json, table, or paths, got %q\n", findMissionsFormat)
		return usageError{}
	}

	// Parse --since into a time.Time for comparison.
	var sinceTime time.Time
	if findMissionsSince != "" {
		t, err := parseSinceDate(findMissionsSince)
		if err != nil {
			return fmt.Errorf("find missions: --since %q: %w", findMissionsSince, err)
		}
		sinceTime = t
	}

	path := mission.RepoStatePath(repoRoot, "missions.jsonl")
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find missions: opening %s: %w", path, err)
	}
	defer f.Close()

	var traces []mission.TraceSummary
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ts mission.TraceSummary
		if err := json.Unmarshal([]byte(line), &ts); err != nil {
			fmt.Fprintf(errOut, "ethos: find missions: line %d: %v\n", lineNo, err)
			continue
		}
		if !matchesFilters(ts, findMissionsWorker, findMissionsStatus, sinceTime) {
			continue
		}
		traces = append(traces, ts)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("find missions: reading %s: %w", path, err)
	}

	switch findMissionsFormat {
	case "json":
		return renderFindJSON(out, traces)
	case "table":
		return renderFindTable(out, traces)
	case "paths":
		return renderFindPaths(out, repoRoot, traces)
	}
	return nil
}

// parseSinceDate parses an RFC3339 timestamp or a YYYY-MM-DD date.
func parseSinceDate(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("must be RFC3339 or YYYY-MM-DD")
}

// matchesFilters returns true when ts passes all active filters.
func matchesFilters(ts mission.TraceSummary, worker, status string, since time.Time) bool {
	if worker != "" && ts.Worker != worker {
		return false
	}
	if status != "" && ts.Status != status {
		return false
	}
	if !since.IsZero() {
		created, err := time.Parse(time.RFC3339, ts.CreatedAt)
		if err != nil {
			return false
		}
		if created.Before(since) {
			return false
		}
	}
	return true
}

// renderFindJSON writes one JSONL line per trace.
func renderFindJSON(out io.Writer, traces []mission.TraceSummary) error {
	enc := json.NewEncoder(out)
	for _, ts := range traces {
		if err := enc.Encode(ts); err != nil {
			return fmt.Errorf("encoding trace: %w", err)
		}
	}
	return nil
}

// renderFindTable writes a human-readable table.
func renderFindTable(out io.Writer, traces []mission.TraceSummary) error {
	if len(traces) == 0 {
		return nil
	}
	headers := []string{"ID", "STATUS", "LEADER", "WORKER", "EVALUATOR", "CREATED", "VERDICT"}
	rows := make([][]string, len(traces))
	for i, ts := range traces {
		rows[i] = []string{
			ts.ID,
			ts.Status,
			ts.Leader,
			ts.Worker,
			ts.Evaluator,
			formatStarted(ts.CreatedAt),
			ts.Verdict,
		}
	}
	fmt.Fprintln(out, hook.FormatTable(headers, rows))
	return nil
}

// renderFindPaths writes one mission directory path per line.
func renderFindPaths(out io.Writer, repoRoot string, traces []mission.TraceSummary) error {
	for _, ts := range traces {
		dir := mission.RepoStatePath(repoRoot, "missions", ts.ID)
		fmt.Fprintln(out, dir)
	}
	return nil
}
