package main

import (
	"fmt"
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
	Run: func(cmd *cobra.Command, args []string) {
		runTeamCreate(args[0])
	},
}

var teamListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all teams",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runTeamList()
	},
}

var teamShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show team details",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runTeamShow(args[0])
	},
}

var teamDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a team",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runTeamDelete(args[0])
	},
}

var teamAddMemberCmd = &cobra.Command{
	Use:   "add-member <team> <identity> <role>",
	Short: "Add a member to a team",
	Args:  cobra.ExactArgs(3),
	Run: func(cmd *cobra.Command, args []string) {
		runTeamAddMember(args[0], args[1], args[2])
	},
}

var teamRemoveMemberCmd = &cobra.Command{
	Use:   "remove-member <team> <identity> <role>",
	Short: "Remove a member from a team",
	Args:  cobra.ExactArgs(3),
	Run: func(cmd *cobra.Command, args []string) {
		runTeamRemoveMember(args[0], args[1], args[2])
	},
}

var teamAddCollabCmd = &cobra.Command{
	Use:   "add-collab <team> <from> <to> <type>",
	Short: "Add a collaboration to a team",
	Args:  cobra.ExactArgs(4),
	Run: func(cmd *cobra.Command, args []string) {
		runTeamAddCollab(args[0], args[1], args[2], args[3])
	},
}

var teamForRepoCmd = &cobra.Command{
	Use:   "for-repo [repo]",
	Short: "Show team(s) for a repository",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		var repo string
		if len(args) > 0 {
			repo = args[0]
		}
		runTeamForRepo(repo)
	},
}

func init() {
	teamCreateCmd.Flags().StringVarP(&teamCreateFile, "file", "f", "", "Read team definition from YAML file")
	teamCmd.AddCommand(teamCreateCmd, teamListCmd, teamShowCmd, teamDeleteCmd,
		teamAddMemberCmd, teamRemoveMemberCmd, teamAddCollabCmd, teamForRepoCmd)
	rootCmd.AddCommand(teamCmd)
}

func runTeamCreate(name string) {
	var t team.Team

	if teamCreateFile != "" {
		data, err := os.ReadFile(teamCreateFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
			os.Exit(1)
		}
		if err := yaml.Unmarshal(data, &t); err != nil {
			fmt.Fprintf(os.Stderr, "ethos: parsing team file: %v\n", err)
			os.Exit(1)
		}
		t.Name = name
	} else {
		t.Name = name
		// Require at least one member via flags or interactive.
		fmt.Println("Enter first member identity handle:")
		var ident string
		if _, err := fmt.Scanln(&ident); err != nil || ident == "" {
			fmt.Fprintf(os.Stderr, "ethos: identity is required\n")
			os.Exit(1)
		}
		fmt.Println("Enter first member role:")
		var r string
		if _, err := fmt.Scanln(&r); err != nil || r == "" {
			fmt.Fprintf(os.Stderr, "ethos: role is required\n")
			os.Exit(1)
		}
		t.Members = []team.Member{{Identity: ident, Role: r}}
	}

	s := teamStore()
	if err := s.Save(&t, identityExistsFunc(), roleExistsFunc()); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		printJSON(&t)
		return
	}
	fmt.Printf("Created team %q\n", name)
}

func runTeamList() {
	s := teamStore()
	names, err := s.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(names)
		return
	}
	if len(names) == 0 {
		fmt.Println("No teams found. Run 'ethos team create <name>' to create one.")
		return
	}
	for _, n := range names {
		fmt.Println(n)
	}
}

func runTeamShow(name string) {
	s := teamStore()
	t, err := s.Load(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(t)
		return
	}
	printTeam(t)
}

// printTeam displays a team in human-readable text format.
func printTeam(t *team.Team) {
	fmt.Printf("Name: %s\n", t.Name)
	if len(t.Repositories) > 0 {
		fmt.Println("Repositories:")
		for _, r := range t.Repositories {
			fmt.Printf("  - %s\n", r)
		}
	}
	fmt.Println("Members:")
	for _, m := range t.Members {
		fmt.Printf("  - %s (%s)\n", m.Identity, m.Role)
	}
	if len(t.Collaborations) > 0 {
		fmt.Println("Collaborations:")
		for _, c := range t.Collaborations {
			fmt.Printf("  - %s -> %s (%s)\n", c.From, c.To, c.Type)
		}
	}
}

func runTeamForRepo(repo string) {
	if repo == "" {
		repo = resolve.RepoName()
	}
	if repo == "" {
		fmt.Fprintf(os.Stderr, "ethos: could not determine repo name (no argument and no git remote)\n")
		os.Exit(1)
	}

	s := teamStore()
	teams, err := s.FindByRepo(repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		printJSON(teams)
		return
	}

	if len(teams) == 0 {
		fmt.Fprintf(os.Stderr, "ethos: no team found for %s\n", repo)
		os.Exit(1)
	}

	for i, t := range teams {
		if i > 0 {
			fmt.Println()
		}
		printTeam(t)
	}
}

func runTeamDelete(name string) {
	s := teamStore()
	if err := s.Delete(name); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(map[string]string{"deleted": name})
		return
	}
	fmt.Printf("Deleted team %q\n", name)
}

func runTeamAddMember(teamName, ident, r string) {
	s := teamStore()
	m := team.Member{Identity: ident, Role: r}
	if err := s.AddMember(teamName, m, identityExistsFunc(), roleExistsFunc()); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(m)
		return
	}
	fmt.Printf("Added %s (%s) to team %q\n", ident, r, teamName)
}

func runTeamRemoveMember(teamName, ident, r string) {
	s := teamStore()
	if err := s.RemoveMember(teamName, ident, r); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(map[string]string{"removed": ident, "role": r, "team": teamName})
		return
	}
	fmt.Printf("Removed %s (%s) from team %q\n", ident, r, teamName)
}

func runTeamAddCollab(teamName, from, to, collabType string) {
	s := teamStore()
	c := team.Collaboration{From: from, To: to, Type: collabType}
	if err := s.AddCollaboration(teamName, c); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(c)
		return
	}
	fmt.Printf("Added collaboration %s -> %s (%s) on team %q\n", from, to, collabType, teamName)
}
