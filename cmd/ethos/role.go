package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/role"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// roleStore returns a layered role store that checks repo-local first,
// then user-global.
func roleStore() *role.LayeredStore {
	return layeredRoleStore(identityStore())
}

// layeredRoleStore creates a layered role store from an identity store.
func layeredRoleStore(is identity.IdentityStore) *role.LayeredStore {
	if ls, ok := is.(*identity.LayeredStore); ok {
		return role.NewLayeredStore(ls.RepoRoot(), ls.GlobalRoot())
	}
	return role.NewLayeredStore("", is.Root())
}


var roleCmd = &cobra.Command{
	Use:     "role",
	Short:   "Manage roles (create, list, show, delete)",
	GroupID: "identity",
}

var roleCreateFile string

var roleCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new role",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runRoleCreate(args[0])
	},
}

var roleListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all roles",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runRoleList()
	},
}

var roleShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show role details",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runRoleShow(args[0])
	},
}

var roleDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a role",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runRoleDelete(args[0])
	},
}

func init() {
	roleCreateCmd.Flags().StringVarP(&roleCreateFile, "file", "f", "", "Read role definition from YAML file")
	roleCmd.AddCommand(roleCreateCmd, roleListCmd, roleShowCmd, roleDeleteCmd)
	rootCmd.AddCommand(roleCmd)
}

func runRoleCreate(name string) {
	var r role.Role

	if roleCreateFile != "" {
		data, err := os.ReadFile(roleCreateFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
			os.Exit(1)
		}
		if err := yaml.Unmarshal(data, &r); err != nil {
			fmt.Fprintf(os.Stderr, "ethos: parsing role file: %v\n", err)
			os.Exit(1)
		}
		// Name from argument overrides file.
		r.Name = name
	} else {
		r.Name = name
		// Read responsibilities and permissions from stdin if available.
		fmt.Println("Enter responsibilities (one per line, empty line to finish):")
		r.Responsibilities = readLines()
		fmt.Println("Enter permissions (one per line, empty line to finish):")
		r.Permissions = readLines()
	}

	s := roleStore()
	if err := s.Save(&r); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		printJSON(&r)
		return
	}
	fmt.Printf("Created role %q\n", name)
}

func runRoleList() {
	s := roleStore()
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
		fmt.Println("No roles found. Run 'ethos role create <name>' to create one.")
		return
	}
	for _, n := range names {
		fmt.Println(n)
	}
}

func runRoleShow(name string) {
	s := roleStore()
	r, err := s.Load(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(r)
		return
	}
	fmt.Printf("Name: %s\n", r.Name)
	if len(r.Responsibilities) > 0 {
		fmt.Println("Responsibilities:")
		for _, resp := range r.Responsibilities {
			fmt.Printf("  - %s\n", resp)
		}
	}
	if len(r.Permissions) > 0 {
		fmt.Println("Permissions:")
		for _, perm := range r.Permissions {
			fmt.Printf("  - %s\n", perm)
		}
	}
}

func runRoleDelete(name string) {
	s := roleStore()
	// Check referential integrity: no team should reference this role.
	ts := teamStore()
	teamNames, _ := ts.List()
	for _, tn := range teamNames {
		t, err := ts.Load(tn)
		if err != nil {
			continue
		}
		for _, m := range t.Members {
			if m.Role == name {
				fmt.Fprintf(os.Stderr, "ethos: cannot delete role %q: referenced by team %q (member %s)\n", name, tn, m.Identity)
				os.Exit(1)
			}
		}
	}
	if err := s.Delete(name); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(map[string]string{"deleted": name})
		return
	}
	fmt.Printf("Deleted role %q\n", name)
}

// readLines reads lines from stdin until an empty line.
func readLines() []string {
	var lines []string
	var line string
	for {
		_, err := fmt.Scanln(&line)
		if err != nil || strings.TrimSpace(line) == "" {
			break
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return nil
	}
	return lines
}
