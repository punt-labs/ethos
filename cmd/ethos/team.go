package main

import (
	"fmt"
	"io"
	"os"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/punt-labs/ethos/internal/team"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// teamStore returns a layered team store.
func teamStore() *team.LayeredStore {
	return layeredTeamStore(identityStore())
}

// layeredTeamStore creates a layered team store from an identity store.
func layeredTeamStore(is identity.IdentityStore) *team.LayeredStore {
	if ls, ok := is.(*identity.LayeredStore); ok {
		return team.NewLayeredStore(ls.RepoRoot(), ls.GlobalRoot())
	}
	return team.NewLayeredStore("", is.Root())
}

// identityExistsFunc returns a function that checks identity existence.
func identityExistsFunc() func(string) bool {
	is := identityStore()
	return func(handle string) bool { return is.Exists(handle) }
}

// roleExistsFunc returns a function that checks role existence.
func roleExistsFunc() func(string) bool {
	rs := roleStore()
	return func(name string) bool { return rs.Exists(name) }
}

var teamCreateFile string

var teamCmd = &cobra.Command{
	Use:     "team",
	Short:   "Manage teams (create, list, show, delete, add-member, remove-member, add-collab)",
	GroupID: "identity",
}

var teamCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new team",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTeamCreate(cmd, args[0])
	},
}

var teamListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all teams",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTeamList(cmd)
	},
}

var teamShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show team details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTeamShow(cmd, args[0])
	},
}

var teamDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a team",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTeamDelete(cmd, args[0])
	},
}

var teamAddMemberCmd = &cobra.Command{
	Use:   "add-member <team> <identity> <role>",
	Short: "Add a member to a team",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTeamAddMember(cmd, args[0], args[1], args[2])
	},
}

var teamRemoveMemberCmd = &cobra.Command{
	Use:   "remove-member <team> <identity> <role>",
	Short: "Remove a member from a team",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTeamRemoveMember(cmd, args[0], args[1], args[2])
	},
}

var teamAddCollabCmd = &cobra.Command{
	Use:   "add-collab <team> <from> <to> <type>",
	Short: "Add a collaboration to a team",
	Args:  cobra.ExactArgs(4),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTeamAddCollab(cmd, args[0], args[1], args[2], args[3])
	},
}

var teamForRepoCmd = &cobra.Command{
	Use:   "for-repo [repo]",
	Short: "Show team(s) for a repository",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var repo string
		if len(args) > 0 {
			repo = args[0]
		}
		return runTeamForRepo(cmd, repo)
	},
}

func init() {
	teamCreateCmd.Flags().StringVarP(&teamCreateFile, "file", "f", "", "Read team definition from YAML file")
	teamCmd.AddCommand(teamCreateCmd, teamListCmd, teamShowCmd, teamDeleteCmd,
		teamAddMemberCmd, teamRemoveMemberCmd, teamAddCollabCmd, teamForRepoCmd)
	rootCmd.AddCommand(teamCmd)
}

func runTeamCreate(cmd *cobra.Command, name string) error {
	var t team.Team

	if teamCreateFile != "" {
		data, err := os.ReadFile(teamCreateFile)
		if err != nil {
			return err
		}
		if err := yaml.Unmarshal(data, &t); err != nil {
			return fmt.Errorf("parsing team file: %w", err)
		}
		t.Name = name
	} else {
		t.Name = name
		// Require at least one member via flags or interactive.
		fmt.Fprintln(cmd.OutOrStdout(), "Enter first member identity handle:")
		var ident string
		if _, err := fmt.Scanln(&ident); err != nil || ident == "" {
			return fmt.Errorf("identity is required")
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Enter first member role:")
		var r string
		if _, err := fmt.Scanln(&r); err != nil || r == "" {
			return fmt.Errorf("role is required")
		}
		t.Members = []team.Member{{Identity: ident, Role: r}}
	}

	s := teamStore()
	if err := s.Save(&t, identityExistsFunc(), roleExistsFunc()); err != nil {
		return err
	}

	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), &t)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Created team %q\n", name)
	return nil
}

func runTeamList(cmd *cobra.Command) error {
	s := teamStore()
	names, err := s.List()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if jsonOutput {
		return writeJSON(out, names)
	}
	if len(names) == 0 {
		fmt.Fprintln(out, "No teams found. Run 'ethos team create <name>' to create one.")
		return nil
	}
	for _, n := range names {
		fmt.Fprintln(out, n)
	}
	return nil
}

func runTeamShow(cmd *cobra.Command, name string) error {
	s := teamStore()
	t, err := s.Load(name)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if jsonOutput {
		return writeJSON(out, t)
	}
	printTeam(out, t)
	return nil
}

// printTeam displays a team in human-readable text format.
func printTeam(w io.Writer, t *team.Team) {
	fmt.Fprintf(w, "Name: %s\n", t.Name)
	if len(t.Repositories) > 0 {
		fmt.Fprintln(w, "Repositories:")
		for _, r := range t.Repositories {
			fmt.Fprintf(w, "  - %s\n", r)
		}
	}
	fmt.Fprintln(w, "Members:")
	for _, m := range t.Members {
		fmt.Fprintf(w, "  - %s (%s)\n", m.Identity, m.Role)
	}
	if len(t.Collaborations) > 0 {
		fmt.Fprintln(w, "Collaborations:")
		for _, c := range t.Collaborations {
			fmt.Fprintf(w, "  - %s -> %s (%s)\n", c.From, c.To, c.Type)
		}
	}
}

func runTeamForRepo(cmd *cobra.Command, repo string) error {
	if repo == "" {
		repo = resolve.RepoName()
	}
	if repo == "" {
		return fmt.Errorf("could not determine repo name (no argument and no git remote)")
	}

	s := teamStore()
	teams, err := s.FindByRepo(repo)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if jsonOutput {
		return writeJSON(out, teams)
	}

	if len(teams) == 0 {
		fmt.Fprintf(out, "no team found for %s\n", repo)
		return nil
	}

	for i, t := range teams {
		if i > 0 {
			fmt.Fprintln(out)
		}
		printTeam(out, t)
	}
	return nil
}

func runTeamDelete(cmd *cobra.Command, name string) error {
	s := teamStore()
	if err := s.Delete(name); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if jsonOutput {
		return writeJSON(out, map[string]string{"deleted": name})
	}
	fmt.Fprintf(out, "Deleted team %q\n", name)
	return nil
}

func runTeamAddMember(cmd *cobra.Command, teamName, ident, r string) error {
	s := teamStore()
	m := team.Member{Identity: ident, Role: r}
	if err := s.AddMember(teamName, m, identityExistsFunc(), roleExistsFunc()); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if jsonOutput {
		return writeJSON(out, m)
	}
	fmt.Fprintf(out, "Added %s (%s) to team %q\n", ident, r, teamName)
	return nil
}

func runTeamRemoveMember(cmd *cobra.Command, teamName, ident, r string) error {
	s := teamStore()
	if err := s.RemoveMember(teamName, ident, r); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if jsonOutput {
		return writeJSON(out, map[string]string{"removed": ident, "role": r, "team": teamName})
	}
	fmt.Fprintf(out, "Removed %s (%s) from team %q\n", ident, r, teamName)
	return nil
}

func runTeamAddCollab(cmd *cobra.Command, teamName, from, to, collabType string) error {
	s := teamStore()
	c := team.Collaboration{From: from, To: to, Type: collabType}
	if err := s.AddCollaboration(teamName, c); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if jsonOutput {
		return writeJSON(out, c)
	}
	fmt.Fprintf(out, "Added collaboration %s -> %s (%s) on team %q\n", from, to, collabType, teamName)
	return nil
}
