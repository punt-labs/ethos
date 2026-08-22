package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/punt-labs/ethos/internal/hook"
	"github.com/punt-labs/ethos/internal/mission"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/spf13/cobra"
)

// missionShowPayload extends mission.ShowPayload with a corrections
// section (DES-072). Defined here rather than adding a field to
// mission.ShowPayload itself: Go's JSON marshaler flattens an
// embedded struct's fields to the top level regardless of how many
// levels are embedded, so wrapping ShowPayload (which itself embeds
// *Contract) still produces one flat JSON object — mission_id,
// status, results, warnings, and now corrections all as siblings.
type missionShowPayload struct {
	mission.ShowPayload
	Corrections []mission.Correction `json:"corrections"`
}

// missionResultsPayload is the JSON shape for `ethos mission results
// --json` (DES-072): results plus the corrections filed against them,
// sourced separately since a correction never touches results.yaml.
type missionResultsPayload struct {
	Results     []mission.Result     `json:"results"`
	Corrections []mission.Correction `json:"corrections"`
}

var (
	missionCorrectKind       string
	missionCorrectClaim      string
	missionCorrectCorrected  string
	missionCorrectRound      int
	missionCorrectSupersedes string
	missionCorrectEvidence   []string
	missionCorrectFile       string
)

var missionCorrectCmd = &cobra.Command{
	Use:   "correct <id-or-prefix>",
	Short: "File a correction against a closed mission",
	Long: `File a correction against a closed mission (DES-072).

A correction is an additive-only annotation: it appends a "correct"
event to the mission's event log and never rewrites contract.yaml,
results.yaml, or reflections.yaml. Corrections apply only to closed
missions — an open mission's story isn't finished yet.

--kind is required and closed: factual (true when written, false
now), fabrication (never true — an integrity finding), or decision (a
leader ruling with no prior claim to correct). --claim is required
for factual and fabrication; omit it for decision. --corrected is
always required: what's actually true, or what was decided.

--round defaults to 0, the whole-mission sentinel. Any other value
must not exceed the mission's current round.

--evidence accepts "name=status" (status one of pass, fail, skip) and
may be repeated.

--file reads the same fields from a YAML file instead, matching
"ethos mission reflect"/"ethos mission result"'s --file convention:

  mission: m-2026-04-08-005
  round: 0
  kind: fabrication
  author: claude
  claim: "make check (full suite): fail — pre-existing, unrelated"
  corrected: "make check failed because of a stale worktree base"
  evidence:
    - name: re-ran make check on a fresh worktree
      status: pass

--file and the individual flags are mutually exclusive.

The correction's author must resolve to a real, fully-configured
identity — unlike a result's author, who says the record on file is
wrong is load-bearing.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMissionCorrect(cmd, args[0])
	},
}

func init() {
	missionCorrectCmd.Flags().StringVar(&missionCorrectKind, "kind", "",
		"Correction kind: factual, fabrication, or decision (required)")
	missionCorrectCmd.Flags().StringVar(&missionCorrectClaim, "claim", "",
		"The claim being corrected (required unless --kind decision)")
	missionCorrectCmd.Flags().StringVar(&missionCorrectCorrected, "corrected", "",
		"What's actually true, or what was decided (required)")
	missionCorrectCmd.Flags().IntVar(&missionCorrectRound, "round", 0,
		"Round being corrected (0 = whole-mission)")
	missionCorrectCmd.Flags().StringVar(&missionCorrectSupersedes, "supersedes", "",
		"A prior correction this one supersedes")
	missionCorrectCmd.Flags().StringArrayVar(&missionCorrectEvidence, "evidence", nil,
		`Evidence entry "name=status" (repeatable)`)
	missionCorrectCmd.Flags().StringVarP(&missionCorrectFile, "file", "f", "",
		"Read the correction YAML from file instead of the flags above")
	missionCmd.AddCommand(missionCorrectCmd)
}

// runMissionCorrect handles `ethos mission correct <id> [flags | --file <path>]`.
//
// The mission is resolved by ID or unambiguous prefix, matching the
// show/close/reflect/result convention. Author resolution runs before
// Store.Correct: unlike Result.Author (a bookkeeping field), WHO says
// the record on file is wrong is load-bearing, so an unresolvable
// handle is refused here rather than silently accepted by the store.
func runMissionCorrect(cmd *cobra.Command, idOrPrefix string) error {
	ms := missionStore()
	id, err := ms.MatchByPrefix(idOrPrefix)
	if err != nil {
		return fmt.Errorf("mission correct: %w", err)
	}

	c, err := buildCorrection(cmd, id)
	if err != nil {
		return fmt.Errorf("mission correct: %w", err)
	}

	is := identityStore()
	sources, err := mission.NewLiveHashSources(is, layeredRoleStore(is), layeredTeamStore(is))
	if err != nil {
		return fmt.Errorf("mission correct: %w", err)
	}
	if err := mission.ValidateCorrectionAuthor(c.Author, sources.Identities); err != nil {
		return fmt.Errorf("mission correct: %w", err)
	}

	if err := ms.Correct(id, *c); err != nil {
		return fmt.Errorf("mission correct: %w", err)
	}
	// Parity with close/abandon: seal the checkout's mission-log tail
	// so the correction is durable on disk even if no commit
	// immediately follows. Store.Correct cannot do this itself — the
	// mission package cannot import internal/hook without cycling.
	//
	// Surface a seal failure on stderr AND in the --json warnings
	// field, matching runMissionShow/runMissionResults: a scriptable
	// caller reading only stdout must not be blind to a durability
	// warning that a human running interactively would see on stderr.
	var warnings []string
	if repoRoot := resolve.EnvRepoRoot(); repoRoot != "" {
		if _, sErr := hook.SealMission(repoRoot, id, time.Now().UTC(), hook.SealOptions{}); sErr != nil {
			msg := fmt.Sprintf("sealing mission log: %v", sErr)
			fmt.Fprintf(os.Stderr, "ethos: mission correct: %s\n", msg)
			warnings = append(warnings, msg)
		}
	}

	if jsonOutput {
		payload := map[string]any{
			"mission_id": id,
			"kind":       string(c.Kind),
			"round":      c.Round,
			"author":     c.Author,
		}
		if len(warnings) > 0 {
			payload["warnings"] = warnings
		}
		printJSON(payload)
		return nil
	}
	fmt.Printf("corrected: %s kind=%s round=%d by %s\n", id, c.Kind, c.Round, c.Author)
	return nil
}

// buildCorrection assembles a mission.Correction from either --file or
// the individual flags — the two input modes are mutually exclusive.
// Mission and Author default to the resolved mission ID and the
// current leader when left unset, so the common flag-based case
// (author = whoever is running the command) needs no --author flag.
func buildCorrection(cmd *cobra.Command, id string) (*mission.Correction, error) {
	// cmd.Flags().Changed("round") catches an explicit "--round 0",
	// which the string/slice checks below cannot: 0 is also --round's
	// zero value, so a bare presence check on missionCorrectRound
	// would silently treat "--file c.yaml --round 0" as file-only
	// instead of refusing the mutually-exclusive combination the
	// error message already names.
	usingFlags := missionCorrectKind != "" || missionCorrectClaim != "" ||
		missionCorrectCorrected != "" || missionCorrectSupersedes != "" ||
		len(missionCorrectEvidence) > 0 || cmd.Flags().Changed("round")

	var c *mission.Correction
	if missionCorrectFile != "" {
		if usingFlags {
			return nil, fmt.Errorf(
				"--file is mutually exclusive with --kind/--claim/--corrected/--round/--supersedes/--evidence")
		}
		data, err := os.ReadFile(missionCorrectFile)
		if err != nil {
			return nil, err
		}
		c, err = mission.DecodeCorrectionStrict(data, missionCorrectFile)
		if err != nil {
			return nil, err
		}
	} else {
		ev, err := parseEvidenceFlags(missionCorrectEvidence)
		if err != nil {
			return nil, err
		}
		c = &mission.Correction{
			Mission:    id,
			Round:      missionCorrectRound,
			Kind:       mission.CorrectionKind(missionCorrectKind),
			Author:     resolveLeader(),
			Claim:      missionCorrectClaim,
			Corrected:  missionCorrectCorrected,
			Supersedes: missionCorrectSupersedes,
			Evidence:   ev,
		}
	}
	if c.Mission == "" {
		c.Mission = id
	}
	if strings.TrimSpace(c.Author) == "" {
		c.Author = resolveLeader()
	}
	return c, nil
}

// parseEvidenceFlags parses repeated "name=status" --evidence flag
// values into EvidenceCheck entries. An entry with no "=" or an empty
// name/status is refused with the offending entry named, rather than
// silently dropped or misparsed.
func parseEvidenceFlags(raw []string) ([]mission.EvidenceCheck, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]mission.EvidenceCheck, 0, len(raw))
	for _, entry := range raw {
		name, status, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(status) == "" {
			return nil, fmt.Errorf("invalid --evidence %q: must be name=status", entry)
		}
		out = append(out, mission.EvidenceCheck{Name: name, Status: status})
	}
	return out, nil
}
