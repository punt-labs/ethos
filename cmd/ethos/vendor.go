package main

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/internal/enable"
	"github.com/punt-labs/ethos/internal/hook"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/mcp"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/punt-labs/ethos/internal/vendor"
	"github.com/spf13/cobra"
)

var (
	vendorTeam         string
	vendorAll          bool
	vendorTo           string
	vendorPrune        bool
	vendorDryRun       bool
	vendorApply        bool
	vendorAllowExtKeys []string
)

var vendorCmd = &cobra.Command{
	Use:   "vendor [handle...]",
	Short: "Snapshot a complete, self-standing identity set into this repo",
	Long: "Snapshot a complete, self-standing identity set into .punt-labs/ethos/.\n\n" +
		"Vendor follows references to a fixed point — attributes, roles, and the teams\n" +
		"an identity belongs to, plus those teams' other members — so the result\n" +
		"resolves on a machine with no global ethos store. Pair it with\n" +
		"`resolution: repo-only` in .punt-labs/ethos.yaml.\n\n" +
		"The closure can be much larger than the handles you name: it pulls the\n" +
		"connected component of the team graph, so in a dense org vendoring one\n" +
		"identity can vendor most of the roster. Vendor therefore PLANS by default\n" +
		"and writes only under --apply.\n\n" +
		"Unlike `ethos export`, which converts one identity to a foreign format and\n" +
		"drops roles, teams, and extensions by contract, vendor copies native files\n" +
		"and loses nothing.",
	GroupID: "identity",
	RunE:    runVendor,
}

func init() {
	vendorCmd.Flags().StringVar(&vendorTeam, "team", "", "Seed the closure from a team's members")
	vendorCmd.Flags().BoolVar(&vendorAll, "all", false, "Seed the closure from every readable identity")
	vendorCmd.Flags().StringVar(&vendorTo, "to", "", "Destination root (default: <repo>/.punt-labs/ethos)")
	vendorCmd.Flags().BoolVar(&vendorPrune, "prune", false, "Remove vendored files the closure no longer contains")
	vendorCmd.Flags().BoolVar(&vendorDryRun, "dry-run", false, "Plan only (the default)")
	vendorCmd.Flags().BoolVar(&vendorApply, "apply", false, "Write the snapshot")
	vendorCmd.Flags().StringArrayVar(&vendorAllowExtKeys, "allow-ext-key", nil,
		"Allow one credential-named extension key, as <namespace>/<key> (repeatable)")

	// Rejected rather than absent: a user who reaches for either is
	// asking for a snapshot that vendor would still call complete, and a
	// silent "unknown flag" would not tell them why they cannot have it.
	vendorCmd.Flags().Bool("no-teams", false, "")
	vendorCmd.Flags().String("from", "", "")
	_ = vendorCmd.Flags().MarkHidden("no-teams")
	_ = vendorCmd.Flags().MarkHidden("from")

	vendorCmd.MarkFlagsMutuallyExclusive("all", "team")

	rootCmd.AddCommand(vendorCmd)
}

func runVendor(cmd *cobra.Command, args []string) error {
	for _, name := range []string{"no-teams", "from"} {
		if cmd.Flags().Changed(name) {
			return usageError{fmt.Sprintf(
				"--%s is not supported: it would produce a partial snapshot that vendor still reports as complete. "+
					"Vendor always follows the full closure; use --dry-run to see its size first", name)}
		}
	}
	if vendorApply && vendorDryRun {
		return usageError{"--apply and --dry-run are mutually exclusive"}
	}
	if vendorAll && len(args) > 0 {
		return usageError{"--all is not supported with explicit handles: --all already selects every readable identity"}
	}

	// The FULL chain, not identityStore(): vendor copies from global into
	// the repo layer, so it must see global even in a repo that has
	// already set `resolution: repo-only`.
	is := vendorSourceStore()
	dest, err := vendorDest(is)
	if err != nil {
		return err
	}

	v, err := vendor.New(vendor.Sources{
		Roots:      vendorRoots(is),
		Identities: is,
		Roles:      layeredRoleStore(is),
		Teams:      layeredTeamStore(is),
	}, vendor.Options{
		Handles:      args,
		Team:         vendorTeam,
		All:          vendorAll,
		Dest:         dest,
		Prune:        vendorPrune,
		Apply:        vendorApply,
		AllowExtKeys: vendorAllowExtKeys,
	})
	if err != nil {
		return err
	}

	plan, err := v.Run()
	if err != nil {
		return err
	}

	// Vendor just put extension files into git-tracked space, so this is
	// the moment the .local rule must exist — before the operator's first
	// `ethos ext set --local` has anywhere to leak to.
	if plan.Applied {
		added, ignoreErr := ensureLocalExtIgnored(resolve.EnvRepoRoot())
		if ignoreErr != nil {
			return ignoreErr
		}
		if added && !jsonOutput {
			fmt.Fprintf(cmd.ErrOrStderr(), "ethos: added the %s rule to .gitignore\n", enable.LocalIgnoreRule)
		}
	}

	out := cmd.OutOrStdout()
	if jsonOutput {
		return writeJSON(out, plan)
	}
	writeVendorPlan(out, plan)
	return nil
}

// vendorRunner adapts the vendor package for the MCP tool. Building a
// Vendorer needs the repo roots and layered stores that only the command
// layer resolves, so the tool takes the closure rather than the stores.
//
// It applies the same defaults the CLI does, including emitting the
// .local gitignore rule after a successful apply — the two surfaces must
// leave the repo in the same state.
func vendorRunner() mcp.VendorRunner {
	return func(opts vendor.Options) (*vendor.Plan, error) {
		is := vendorSourceStore()
		if opts.Dest == "" {
			dest, err := vendorDest(is)
			if err != nil {
				return nil, err
			}
			opts.Dest = dest
		}
		v, err := vendor.New(vendor.Sources{
			Roots:      vendorRoots(is),
			Identities: is,
			Roles:      layeredRoleStore(is),
			Teams:      layeredTeamStore(is),
		}, opts)
		if err != nil {
			return nil, err
		}
		plan, err := v.Run()
		if err != nil {
			return nil, err
		}
		if plan.Applied {
			if _, err := ensureLocalExtIgnored(resolve.EnvRepoRoot()); err != nil {
				return nil, err
			}
		}
		return plan, nil
	}
}

// vendorDest resolves where the snapshot is written: --to, else the
// repo's own .punt-labs/ethos/.
func vendorDest(is identity.IdentityStore) (string, error) {
	if vendorTo != "" {
		return vendorTo, nil
	}
	if ls, ok := is.(*identity.LayeredStore); ok && ls.RepoRoot() != "" {
		return ls.RepoRoot(), nil
	}
	repoRoot := resolve.StoreRepoRoot()
	if repoRoot == "" {
		return "", fmt.Errorf("not in a git repository — name a destination with --to")
	}
	return filepath.Join(repoRoot, ".punt-labs", "ethos"), nil
}

// vendorRoots returns the read chain in precedence order. Vendor probes
// these itself to find the FILE behind each reference, because a layered
// store reports resolved content, not which layer supplied the bytes.
func vendorRoots(is identity.IdentityStore) []string {
	ls, ok := is.(*identity.LayeredStore)
	if !ok {
		return []string{is.Root()}
	}
	var roots []string
	for _, r := range []string{ls.RepoRoot(), ls.BundleRoot(), ls.GlobalRoot()} {
		if r != "" {
			roots = append(roots, r)
		}
	}
	return roots
}

// writeVendorPlan renders the closure, the blast radius, and what would
// change. A plan the user cannot read is a plan they will skip.
func writeVendorPlan(w io.Writer, p *vendor.Plan) {
	verb := "Would vendor"
	if p.Applied {
		verb = "Vendored"
	}
	fmt.Fprintf(w, "%s %d identities into %s\n", verb, len(p.Identities), p.Dest)
	fmt.Fprintf(w, "  seeds: %s\n", strings.Join(p.Seeds, ", "))

	if extra := len(p.Identities) - len(p.Seeds); extra > 0 {
		fmt.Fprintf(w, "  blast radius: %d more identities pulled in through team membership\n", extra)
	}

	rows := [][]string{
		{"identities", strings.Join(p.Identities, ", ")},
		{"personalities", strings.Join(p.Personalities, ", ")},
		{"writing-styles", strings.Join(p.WritingStyles, ", ")},
		{"talents", strings.Join(p.Talents, ", ")},
		{"roles", strings.Join(p.Roles, ", ")},
		{"teams", strings.Join(p.Teams, ", ")},
		{"extensions", fmt.Sprintf("%d base files", p.ExtCount())},
	}
	var nonEmpty [][]string
	for _, r := range rows {
		if r[1] != "" {
			nonEmpty = append(nonEmpty, r)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, hook.FormatTable([]string{"KIND", "MEMBERS"}, nonEmpty))

	if len(p.Warnings) > 0 {
		fmt.Fprintf(w, "\nExtension keys worth a look (names only — values are never inspected):\n")
		for _, f := range p.Warnings {
			fmt.Fprintf(w, "  %s\n", f)
		}
	}

	if len(p.Pruned) > 0 {
		head := "Would remove"
		if p.Applied && vendorPrune {
			head = "Removed"
		}
		fmt.Fprintf(w, "\n%s %d file(s) no longer in the closure", head, len(p.Pruned))
		if !vendorPrune {
			fmt.Fprintf(w, " (with --prune)")
		}
		fmt.Fprintln(w, ":")
		pruned := append([]string(nil), p.Pruned...)
		sort.Strings(pruned)
		for _, path := range pruned {
			fmt.Fprintf(w, "  %s\n", path)
		}
	}

	if p.Applied {
		fmt.Fprintf(w, "\nWrote %d files and %s. The set resolves on its own.\n", p.FilesWritten, vendor.ManifestName)
		return
	}
	fmt.Fprintf(w, "\nNothing written. Re-run with --apply to write into %s.\n", p.Dest)
}
