package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/punt-labs/ethos/internal/mission"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/spf13/cobra"
)

// pipelineStore returns a layered PipelineStore that checks repo-local
// first, then user-global. Mirrors identityStore() — repo layer comes
// from FindRepoEthosRoot, global from ~/.punt-labs/ethos.
func pipelineStore() *mission.PipelineStore {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: pipeline: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}
	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	repoRoot := resolve.FindRepoEthosRoot()
	return mission.NewPipelineStore(repoRoot, globalRoot)
}

// --- mission pipeline (bare command) ---

var pipelineCmd = &cobra.Command{
	Use:   "pipeline",
	Short: "Manage mission pipelines",
	Args:  cobra.NoArgs,
}

// --- mission pipeline list ---

var pipelineListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available pipelines",
	Long: `List available pipelines.

Discovers pipeline YAML files from both the repo-local
.punt-labs/ethos/pipelines/ directory and the user-global
~/.punt-labs/ethos/pipelines/ directory. Repo-local pipelines
override global pipelines with the same name.

Use --json for a machine-readable array of pipeline summaries.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPipelineList()
	},
}

// --- mission pipeline show ---

var pipelineShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show pipeline details",
	Long: `Show pipeline details including all stages.

Accepts a pipeline name (the filename without .yaml extension).
Use --json for the full pipeline object.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPipelineShow(args[0])
	},
}

// --- mission pipeline instantiate ---

var (
	pipelineInstVars      []string
	pipelineInstLeader    string
	pipelineInstEvaluator string
	pipelineInstWorker    string
	pipelineInstID        string
	pipelineInstDryRun    bool
)

var pipelineInstantiateCmd = &cobra.Command{
	Use:   "instantiate <name>",
	Short: "Generate mission contracts from a pipeline template",
	Long: `Generate mission contracts from a pipeline template.

Creates one mission per stage, expanding {key} template variables
in write_set, context, and success_criteria. Each mission carries
the pipeline ID and depends_on wiring from inputs_from declarations.

Use --dry-run to preview contracts without creating them.
Use --json for machine-readable output.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPipelineInstantiate(args[0])
	},
}

func init() {
	pipelineInstantiateCmd.Flags().StringArrayVar(&pipelineInstVars, "var", nil,
		"Template variable as key=value (repeatable)")
	pipelineInstantiateCmd.Flags().StringVar(&pipelineInstLeader, "leader", "",
		"Leader handle for all stages (required)")
	_ = pipelineInstantiateCmd.MarkFlagRequired("leader")
	pipelineInstantiateCmd.Flags().StringVar(&pipelineInstEvaluator, "evaluator", "",
		"Default evaluator handle (stage.evaluator overrides)")
	pipelineInstantiateCmd.Flags().StringVar(&pipelineInstWorker, "worker", "",
		"Default worker handle (stage.worker overrides)")
	pipelineInstantiateCmd.Flags().StringVar(&pipelineInstID, "id", "",
		"Pipeline ID (auto-generated if omitted)")
	pipelineInstantiateCmd.Flags().BoolVar(&pipelineInstDryRun, "dry-run", false,
		"Print contracts without saving")

	pipelineCmd.AddCommand(pipelineListCmd, pipelineShowCmd, pipelineInstantiateCmd)
	missionCmd.AddCommand(pipelineCmd)
}

func runPipelineList() error {
	ps := pipelineStore()
	names, err := ps.List()
	if err != nil {
		return fmt.Errorf("pipeline list: %w", err)
	}
	if jsonOutput {
		type entry struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Stages      int    `json:"stages"`
		}
		entries := []entry{}
		for _, name := range names {
			p, loadErr := ps.Load(name)
			if loadErr != nil {
				fmt.Fprintf(os.Stderr, "ethos: warning: %s: %v\n", name, loadErr)
				continue
			}
			entries = append(entries, entry{
				Name:        p.Name,
				Description: p.Description,
				Stages:      len(p.Stages),
			})
		}
		printJSON(entries)
		return nil
	}
	if len(names) == 0 {
		fmt.Println("No pipelines found.")
		return nil
	}
	// Load each pipeline for the description column. A load failure
	// degrades to a warning row rather than aborting the entire list.
	type row struct {
		name, description string
		stages            int
	}
	var rows []row
	for _, name := range names {
		p, loadErr := ps.Load(name)
		if loadErr != nil {
			fmt.Fprintf(os.Stderr, "ethos: warning: %s: %v\n", name, loadErr)
			continue
		}
		rows = append(rows, row{name: p.Name, description: p.Description, stages: len(p.Stages)})
	}
	if len(rows) == 0 {
		fmt.Println("No pipelines found.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "NAME\tSTAGES\tDESCRIPTION\n")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%d\t%s\n", r.name, r.stages, r.description)
	}
	return tw.Flush()
}

func runPipelineInstantiate(name string) error {
	ps := pipelineStore()
	p, err := ps.Load(name)
	if err != nil {
		return fmt.Errorf("pipeline instantiate: %w", err)
	}

	vars, err := parseVarFlags(pipelineInstVars)
	if err != nil {
		return fmt.Errorf("pipeline instantiate: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("pipeline instantiate: cannot determine home directory: %w", err)
	}
	root := filepath.Join(home, ".punt-labs", "ethos")

	repoRoot := resolve.FindRepoEthosRoot()
	as := mission.NewArchetypeStore(repoRoot, filepath.Join(home, ".punt-labs", "ethos"))

	opts := mission.InstantiateOptions{
		PipelineID: pipelineInstID,
		Vars:       vars,
		Leader:     pipelineInstLeader,
		Evaluator:  pipelineInstEvaluator,
		Worker:     pipelineInstWorker,
		Root:       root,
		Now:        time.Now(),
		Archetypes: as,
	}

	contracts, err := mission.Instantiate(p, opts)
	if err != nil {
		return fmt.Errorf("pipeline instantiate: %w", err)
	}

	if pipelineInstDryRun {
		return printInstantiateResult(contracts, true)
	}

	// Save each contract via the create-path store.
	ms := missionStoreForCreate()
	is := identityStore()
	sources, err := mission.NewLiveHashSources(is, layeredRoleStore(is), layeredTeamStore(is))
	if err != nil {
		return fmt.Errorf("pipeline instantiate: %w", err)
	}

	for i, c := range contracts {
		// ApplyServerFields overwrites MissionID, timestamps, hash etc.
		// We keep the Pipeline and DependsOn the Instantiate set.
		pipeline := c.Pipeline
		dependsOn := c.DependsOn
		if err := ms.ApplyServerFields(c, opts.Now, sources); err != nil {
			return fmt.Errorf("pipeline instantiate: stage %q: %w", p.Stages[i].Name, err)
		}
		// Restore pipeline fields overwritten by ApplyServerFields
		// (it doesn't touch them, but be defensive).
		c.Pipeline = pipeline
		c.DependsOn = dependsOn

		// Re-resolve DependsOn: the upstream mission IDs may have been
		// replaced by ApplyServerFields. But since Instantiate assigned
		// provisional IDs and ApplyServerFields replaces them, we need
		// to update the downstream depends_on to point to the NEW
		// upstream IDs.
		if p.Stages[i].InputsFrom != "" {
			for j := 0; j < i; j++ {
				if p.Stages[j].Name == p.Stages[i].InputsFrom {
					c.DependsOn = []string{contracts[j].MissionID}
					break
				}
			}
		}

		if err := ms.Create(c); err != nil {
			return fmt.Errorf("pipeline instantiate: stage %q (mission %s): %w",
				p.Stages[i].Name, c.MissionID, err)
		}
	}

	return printInstantiateResult(contracts, false)
}

// parseVarFlags parses --var key=value flags into a map.
func parseVarFlags(flags []string) (map[string]string, error) {
	vars := make(map[string]string, len(flags))
	for _, f := range flags {
		eq := strings.IndexByte(f, '=')
		if eq < 0 {
			return nil, fmt.Errorf("invalid --var %q: expected key=value", f)
		}
		key := f[:eq]
		val := f[eq+1:]
		if key == "" {
			return nil, fmt.Errorf("invalid --var %q: empty key", f)
		}
		vars[key] = val
	}
	return vars, nil
}

// printInstantiateResult outputs the table or JSON for instantiate.
func printInstantiateResult(contracts []*mission.Contract, dryRun bool) error {
	if len(contracts) == 0 {
		fmt.Println("No stages in pipeline.")
		return nil
	}

	pipelineID := contracts[0].Pipeline

	if jsonOutput {
		type missionEntry struct {
			Stage     string   `json:"stage"`
			ID        string   `json:"id"`
			Type      string   `json:"type"`
			DependsOn []string `json:"depends_on"`
		}
		type output struct {
			Pipeline string         `json:"pipeline"`
			DryRun   bool           `json:"dry_run,omitempty"`
			Missions []missionEntry `json:"missions"`
		}
		out := output{
			Pipeline: pipelineID,
			DryRun:   dryRun,
			Missions: make([]missionEntry, len(contracts)),
		}
		for i, c := range contracts {
			dep := c.DependsOn
			if dep == nil {
				dep = []string{}
			}
			out.Missions[i] = missionEntry{
				Stage:     fmt.Sprintf("stage-%d", i+1),
				ID:        c.MissionID,
				Type:      c.Type,
				DependsOn: dep,
			}
		}
		printJSON(out)
		return nil
	}

	prefix := "Created"
	if dryRun {
		prefix = "Dry run"
	}
	fmt.Printf("%s pipeline %s:\n", prefix, pipelineID)

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "STAGE\tMISSION_ID\tTYPE\tDEPENDS_ON\n")
	for i, c := range contracts {
		dep := "(none)"
		if len(c.DependsOn) > 0 {
			dep = strings.Join(c.DependsOn, ", ")
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", i+1, c.MissionID, c.Type, dep)
	}
	return tw.Flush()
}

func runPipelineShow(name string) error {
	ps := pipelineStore()
	p, err := ps.Load(name)
	if err != nil {
		return fmt.Errorf("pipeline show: %w", err)
	}
	if jsonOutput {
		printJSON(p)
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Pipeline:\t%s\n", p.Name)
	fmt.Fprintf(tw, "Description:\t%s\n", p.Description)
	fmt.Fprintf(tw, "Stages:\t%d\n", len(p.Stages))
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("pipeline show: %w", err)
	}
	if len(p.Stages) > 0 {
		fmt.Println()
		for i, s := range p.Stages {
			fmt.Printf("  %d. %s", i+1, s.Name)
			if s.Archetype != "" {
				fmt.Printf(" (%s)", s.Archetype)
			}
			fmt.Println()
			if s.Description != "" {
				fmt.Printf("     %s\n", s.Description)
			}
			if s.Worker != "" {
				fmt.Printf("     worker: %s\n", s.Worker)
			}
			if s.Evaluator != "" {
				fmt.Printf("     evaluator: %s\n", s.Evaluator)
			}
			if s.InputsFrom != "" {
				fmt.Printf("     inputs_from: %s\n", s.InputsFrom)
			}
			if len(s.WriteSet) > 0 {
				fmt.Printf("     write_set:")
				for _, w := range s.WriteSet {
					fmt.Printf(" %s", w)
				}
				fmt.Println()
			}
		}
	}
	return nil
}
