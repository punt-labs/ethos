package main

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

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

func init() {
	pipelineCmd.AddCommand(pipelineListCmd, pipelineShowCmd)
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
