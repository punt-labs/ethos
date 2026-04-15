package main

import (
	"bufio"
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
		return role.NewLayeredStoreWithBundle(ls.RepoRoot(), ls.BundleRoot(), ls.GlobalRoot())
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
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRoleCreate(cmd, args[0])
	},
}

var roleListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all roles",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRoleList(cmd)
	},
}

var roleShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show role details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRoleShow(cmd, args[0])
	},
}

var roleDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a role",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRoleDelete(cmd, args[0])
	},
}

func init() {
	roleCreateCmd.Flags().StringVarP(&roleCreateFile, "file", "f", "", "Read role definition from YAML file")
	roleCmd.AddCommand(roleCreateCmd, roleListCmd, roleShowCmd, roleDeleteCmd)
	rootCmd.AddCommand(roleCmd)
}

func runRoleCreate(cmd *cobra.Command, name string) error {
	var r role.Role

	if roleCreateFile != "" {
		data, err := os.ReadFile(roleCreateFile)
		if err != nil {
			return fmt.Errorf("reading %s: %w", roleCreateFile, err)
		}
		if err := yaml.Unmarshal(data, &r); err != nil {
			return fmt.Errorf("parsing role file: %w", err)
		}
		// Name from argument overrides file.
		r.Name = name
	} else {
		r.Name = name
		// Read responsibilities and permissions from stdin if available.
		fmt.Fprintln(cmd.OutOrStdout(), "Enter responsibilities (one per line, empty line to finish):")
		r.Responsibilities = readLines()
		fmt.Fprintln(cmd.OutOrStdout(), "Enter permissions (one per line, empty line to finish):")
		r.Permissions = readLines()
	}

	s := roleStore()
	if err := s.Save(&r); err != nil {
		return err
	}

	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), &r)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Created role %q\n", name)
	return nil
}

func runRoleList(cmd *cobra.Command) error {
	s := roleStore()
	names, err := s.List()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if jsonOutput {
		return writeJSON(out, names)
	}
	if len(names) == 0 {
		fmt.Fprintln(out, "No roles found. Run 'ethos role create <name>' to create one.")
		return nil
	}
	for _, n := range names {
		fmt.Fprintln(out, n)
	}
	return nil
}

func runRoleShow(cmd *cobra.Command, name string) error {
	s := roleStore()
	r, err := s.Load(name)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if jsonOutput {
		return writeJSON(out, r)
	}
	fmt.Fprintf(out, "Name: %s\n", r.Name)
	if len(r.Responsibilities) > 0 {
		fmt.Fprintln(out, "Responsibilities:")
		for _, resp := range r.Responsibilities {
			fmt.Fprintf(out, "  - %s\n", resp)
		}
	}
	if len(r.Permissions) > 0 {
		fmt.Fprintln(out, "Permissions:")
		for _, perm := range r.Permissions {
			fmt.Fprintf(out, "  - %s\n", perm)
		}
	}
	return nil
}

func runRoleDelete(cmd *cobra.Command, name string) error {
	s := roleStore()
	// Check referential integrity: no team should reference this role.
	// Fail closed — if we can't check, don't delete.
	ts := teamStore()
	teamNames, err := ts.List()
	if err != nil {
		return fmt.Errorf("cannot verify references for role %q: %w", name, err)
	}
	for _, tn := range teamNames {
		t, err := ts.Load(tn)
		if err != nil {
			return fmt.Errorf("cannot verify references for role %q: failed to load team %q: %w", name, tn, err)
		}
		for _, m := range t.Members {
			if m.Role == name {
				return fmt.Errorf("cannot delete role %q: referenced by team %q (member %s)", name, tn, m.Identity)
			}
		}
	}
	if err := s.Delete(name); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if jsonOutput {
		return writeJSON(out, map[string]string{"deleted": name})
	}
	fmt.Fprintf(out, "Deleted role %q\n", name)
	return nil
}

// readLines reads lines from stdin until an empty line.
func readLines() []string {
	scanner := bufio.NewScanner(os.Stdin)
	var lines []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			break
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return nil
	}
	return lines
}
