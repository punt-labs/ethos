package team

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// alwaysTrue is a callback that always returns true (identity/role exists).
func alwaysTrue(_ string) bool { return true }

func testStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

func TestStore_SaveAndLoad(t *testing.T) {
	s := testStore(t)

	team := &Team{
		Name:         "engineering",
		Repositories: []string{"punt-labs/ethos"},
		Members: []Member{
			{Identity: "claude", Role: "coo"},
			{Identity: "bwk", Role: "go-specialist"},
		},
		Collaborations: []Collaboration{
			{From: "go-specialist", To: "coo", Type: "reports_to"},
		},
	}
	require.NoError(t, s.Save(team, alwaysTrue, alwaysTrue))

	loaded, err := s.Load("engineering")
	require.NoError(t, err)
	assert.Equal(t, "engineering", loaded.Name)
	assert.Equal(t, []string{"punt-labs/ethos"}, loaded.Repositories)
	assert.Len(t, loaded.Members, 2)
	assert.Len(t, loaded.Collaborations, 1)
}

func TestStore_SaveDuplicate(t *testing.T) {
	s := testStore(t)

	team := &Team{
		Name:    "eng",
		Members: []Member{{Identity: "alice", Role: "dev"}},
	}
	require.NoError(t, s.Save(team, alwaysTrue, alwaysTrue))

	err := s.Save(team, alwaysTrue, alwaysTrue)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestStore_LoadNotFound(t *testing.T) {
	s := testStore(t)

	_, err := s.Load("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_List(t *testing.T) {
	s := testStore(t)

	require.NoError(t, s.Save(&Team{
		Name:    "alpha",
		Members: []Member{{Identity: "a", Role: "r"}},
	}, alwaysTrue, alwaysTrue))
	require.NoError(t, s.Save(&Team{
		Name:    "beta",
		Members: []Member{{Identity: "b", Role: "r"}},
	}, alwaysTrue, alwaysTrue))

	names, err := s.List()
	require.NoError(t, err)
	assert.Len(t, names, 2)
	assert.Contains(t, names, "alpha")
	assert.Contains(t, names, "beta")
}

func TestStore_ListEmpty(t *testing.T) {
	s := testStore(t)

	names, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, names)
}

func TestStore_Delete(t *testing.T) {
	s := testStore(t)

	require.NoError(t, s.Save(&Team{
		Name:    "eng",
		Members: []Member{{Identity: "a", Role: "r"}},
	}, alwaysTrue, alwaysTrue))
	assert.True(t, s.Exists("eng"))

	require.NoError(t, s.Delete("eng"))
	assert.False(t, s.Exists("eng"))
}

func TestStore_DeleteNotFound(t *testing.T) {
	s := testStore(t)

	err := s.Delete("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_Exists(t *testing.T) {
	s := testStore(t)

	assert.False(t, s.Exists("eng"))

	require.NoError(t, s.Save(&Team{
		Name:    "eng",
		Members: []Member{{Identity: "a", Role: "r"}},
	}, alwaysTrue, alwaysTrue))
	assert.True(t, s.Exists("eng"))
}

func TestStore_AddMember(t *testing.T) {
	s := testStore(t)

	require.NoError(t, s.Save(&Team{
		Name:    "eng",
		Members: []Member{{Identity: "alice", Role: "dev"}},
	}, alwaysTrue, alwaysTrue))

	err := s.AddMember("eng", Member{Identity: "bob", Role: "qa"}, alwaysTrue, alwaysTrue)
	require.NoError(t, err)

	loaded, err := s.Load("eng")
	require.NoError(t, err)
	assert.Len(t, loaded.Members, 2)
}

func TestStore_AddMember_Duplicate(t *testing.T) {
	s := testStore(t)

	require.NoError(t, s.Save(&Team{
		Name:    "eng",
		Members: []Member{{Identity: "alice", Role: "dev"}},
	}, alwaysTrue, alwaysTrue))

	err := s.AddMember("eng", Member{Identity: "alice", Role: "dev"}, alwaysTrue, alwaysTrue)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already a member")
}

func TestStore_AddMember_InvalidIdentity(t *testing.T) {
	s := testStore(t)

	require.NoError(t, s.Save(&Team{
		Name:    "eng",
		Members: []Member{{Identity: "alice", Role: "dev"}},
	}, alwaysTrue, alwaysTrue))

	noIdentity := func(s string) bool { return s != "unknown" }
	err := s.AddMember("eng", Member{Identity: "unknown", Role: "dev"}, noIdentity, alwaysTrue)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_AddMember_TeamNotFound(t *testing.T) {
	s := testStore(t)

	err := s.AddMember("nonexistent", Member{Identity: "a", Role: "r"}, alwaysTrue, alwaysTrue)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_RemoveMember(t *testing.T) {
	s := testStore(t)

	require.NoError(t, s.Save(&Team{
		Name: "eng",
		Members: []Member{
			{Identity: "alice", Role: "dev"},
			{Identity: "bob", Role: "qa"},
		},
		Collaborations: []Collaboration{
			{From: "dev", To: "qa", Type: "collaborates_with"},
		},
	}, alwaysTrue, alwaysTrue))

	// Removing bob (qa role) should also remove the collaboration referencing qa.
	err := s.RemoveMember("eng", "bob", "qa")
	require.NoError(t, err)

	loaded, err := s.Load("eng")
	require.NoError(t, err)
	assert.Len(t, loaded.Members, 1)
	assert.Equal(t, "alice", loaded.Members[0].Identity)
	assert.Empty(t, loaded.Collaborations)
}

func TestStore_RemoveMember_LastMember(t *testing.T) {
	s := testStore(t)

	require.NoError(t, s.Save(&Team{
		Name:    "eng",
		Members: []Member{{Identity: "alice", Role: "dev"}},
	}, alwaysTrue, alwaysTrue))

	err := s.RemoveMember("eng", "alice", "dev")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "last member")
}

func TestStore_RemoveMember_NotFound(t *testing.T) {
	s := testStore(t)

	require.NoError(t, s.Save(&Team{
		Name: "eng",
		Members: []Member{
			{Identity: "alice", Role: "dev"},
			{Identity: "bob", Role: "qa"},
		},
	}, alwaysTrue, alwaysTrue))

	err := s.RemoveMember("eng", "charlie", "dev")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a member")
}

func TestStore_RemoveMember_TeamNotFound(t *testing.T) {
	s := testStore(t)

	err := s.RemoveMember("nonexistent", "alice", "dev")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_AddCollaboration(t *testing.T) {
	s := testStore(t)

	require.NoError(t, s.Save(&Team{
		Name: "eng",
		Members: []Member{
			{Identity: "alice", Role: "dev"},
			{Identity: "bob", Role: "lead"},
		},
	}, alwaysTrue, alwaysTrue))

	err := s.AddCollaboration("eng", Collaboration{
		From: "dev", To: "lead", Type: "reports_to",
	})
	require.NoError(t, err)

	loaded, err := s.Load("eng")
	require.NoError(t, err)
	assert.Len(t, loaded.Collaborations, 1)
	assert.Equal(t, "dev", loaded.Collaborations[0].From)
	assert.Equal(t, "lead", loaded.Collaborations[0].To)
	assert.Equal(t, "reports_to", loaded.Collaborations[0].Type)
}

func TestStore_AddCollaboration_SelfCollab(t *testing.T) {
	s := testStore(t)

	require.NoError(t, s.Save(&Team{
		Name:    "eng",
		Members: []Member{{Identity: "alice", Role: "dev"}},
	}, alwaysTrue, alwaysTrue))

	err := s.AddCollaboration("eng", Collaboration{
		From: "dev", To: "dev", Type: "reports_to",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "self-collaboration")
}

func TestStore_AddCollaboration_RoleNotFilled(t *testing.T) {
	s := testStore(t)

	require.NoError(t, s.Save(&Team{
		Name:    "eng",
		Members: []Member{{Identity: "alice", Role: "dev"}},
	}, alwaysTrue, alwaysTrue))

	err := s.AddCollaboration("eng", Collaboration{
		From: "dev", To: "lead", Type: "reports_to",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not filled")
}

func TestStore_AddCollaboration_InvalidType(t *testing.T) {
	s := testStore(t)

	require.NoError(t, s.Save(&Team{
		Name: "eng",
		Members: []Member{
			{Identity: "alice", Role: "dev"},
			{Identity: "bob", Role: "lead"},
		},
	}, alwaysTrue, alwaysTrue))

	err := s.AddCollaboration("eng", Collaboration{
		From: "dev", To: "lead", Type: "invalid",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid type")
}

func TestStore_AddCollaboration_TeamNotFound(t *testing.T) {
	s := testStore(t)

	err := s.AddCollaboration("nonexistent", Collaboration{
		From: "a", To: "b", Type: "reports_to",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// --- Validation tests ---

func TestValidate_EmptyMembers(t *testing.T) {
	team := &Team{Name: "empty"}
	err := Validate(team, alwaysTrue, alwaysTrue)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "at least one member")
}

func TestValidate_InvalidIdentity(t *testing.T) {
	team := &Team{
		Name:    "eng",
		Members: []Member{{Identity: "unknown", Role: "dev"}},
	}
	noIdentity := func(s string) bool { return false }
	err := Validate(team, noIdentity, alwaysTrue)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "identity \"unknown\" not found")
}

func TestValidate_InvalidRole(t *testing.T) {
	team := &Team{
		Name:    "eng",
		Members: []Member{{Identity: "alice", Role: "unknown"}},
	}
	noRole := func(s string) bool { return false }
	err := Validate(team, alwaysTrue, noRole)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "role \"unknown\" not found")
}

func TestValidate_SelfCollaboration(t *testing.T) {
	team := &Team{
		Name:    "eng",
		Members: []Member{{Identity: "alice", Role: "dev"}},
		Collaborations: []Collaboration{
			{From: "dev", To: "dev", Type: "reports_to"},
		},
	}
	err := Validate(team, alwaysTrue, alwaysTrue)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "self-collaboration")
}

func TestValidate_CollabRoleNotFilled(t *testing.T) {
	team := &Team{
		Name:    "eng",
		Members: []Member{{Identity: "alice", Role: "dev"}},
		Collaborations: []Collaboration{
			{From: "dev", To: "lead", Type: "reports_to"},
		},
	}
	err := Validate(team, alwaysTrue, alwaysTrue)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not filled")
}

func TestValidate_InvalidCollabType(t *testing.T) {
	team := &Team{
		Name: "eng",
		Members: []Member{
			{Identity: "alice", Role: "dev"},
			{Identity: "bob", Role: "lead"},
		},
		Collaborations: []Collaboration{
			{From: "dev", To: "lead", Type: "unknown"},
		},
	}
	err := Validate(team, alwaysTrue, alwaysTrue)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid type")
}

func TestValidate_Valid(t *testing.T) {
	team := &Team{
		Name:         "eng",
		Repositories: []string{"punt-labs/ethos"},
		Members: []Member{
			{Identity: "alice", Role: "dev"},
			{Identity: "bob", Role: "lead"},
		},
		Collaborations: []Collaboration{
			{From: "dev", To: "lead", Type: "reports_to"},
		},
	}
	err := Validate(team, alwaysTrue, alwaysTrue)
	assert.NoError(t, err)
}

func TestValidate_InvalidName(t *testing.T) {
	team := &Team{
		Name:    "BAD NAME",
		Members: []Member{{Identity: "alice", Role: "dev"}},
	}
	err := Validate(team, alwaysTrue, alwaysTrue)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid team name")
}

func TestValidate_AllCollabTypes(t *testing.T) {
	tests := []struct {
		collabType string
		valid      bool
	}{
		{"reports_to", true},
		{"collaborates_with", true},
		{"delegates_to", true},
		{"manages", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.collabType, func(t *testing.T) {
			team := &Team{
				Name: "eng",
				Members: []Member{
					{Identity: "a", Role: "dev"},
					{Identity: "b", Role: "lead"},
				},
				Collaborations: []Collaboration{
					{From: "dev", To: "lead", Type: tt.collabType},
				},
			}
			err := Validate(team, alwaysTrue, alwaysTrue)
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestStore_NameValidation(t *testing.T) {
	s := testStore(t)

	tests := []struct {
		name string
		ok   bool
	}{
		{"engineering", true},
		{"web-team", true},
		{"INVALID", false},
		{"-bad", false},
		{"bad-", false},
		{"bad name", false},
		{"", false},
	}

	for _, tc := range tests {
		team := &Team{
			Name:    tc.name,
			Members: []Member{{Identity: "a", Role: "r"}},
		}
		err := s.Save(team, alwaysTrue, alwaysTrue)
		if tc.ok {
			if err != nil {
				assert.NotContains(t, err.Error(), "must be lowercase", "name %q should be valid", tc.name)
			}
		} else {
			assert.Error(t, err, "name %q should be invalid", tc.name)
		}
	}
}

func TestStore_RemoveMember_RoleStillFilled_KeepsCollabs(t *testing.T) {
	s := testStore(t)

	// Two members hold "dev"; remove one. The collaboration referencing "dev"
	// should be kept because dev is still filled by the other member.
	require.NoError(t, s.Save(&Team{
		Name: "eng",
		Members: []Member{
			{Identity: "alice", Role: "dev"},
			{Identity: "bob", Role: "dev"},
			{Identity: "charlie", Role: "lead"},
		},
		Collaborations: []Collaboration{
			{From: "dev", To: "lead", Type: "reports_to"},
		},
	}, alwaysTrue, alwaysTrue))

	err := s.RemoveMember("eng", "alice", "dev")
	require.NoError(t, err)

	loaded, err := s.Load("eng")
	require.NoError(t, err)
	assert.Len(t, loaded.Members, 2)
	// Collaboration kept — bob still fills "dev".
	assert.Len(t, loaded.Collaborations, 1)
	assert.Equal(t, "dev", loaded.Collaborations[0].From)
}

func TestStore_RemoveMember_DanglingCollabs(t *testing.T) {
	s := testStore(t)

	// Create team with 3 members and collaborations.
	require.NoError(t, s.Save(&Team{
		Name: "eng",
		Members: []Member{
			{Identity: "alice", Role: "dev"},
			{Identity: "bob", Role: "lead"},
			{Identity: "charlie", Role: "qa"},
		},
		Collaborations: []Collaboration{
			{From: "dev", To: "lead", Type: "reports_to"},
			{From: "qa", To: "lead", Type: "reports_to"},
			{From: "dev", To: "qa", Type: "collaborates_with"},
		},
	}, alwaysTrue, alwaysTrue))

	// Remove lead role — should remove collabs involving lead but keep dev-qa.
	err := s.RemoveMember("eng", "bob", "lead")
	require.NoError(t, err)

	loaded, err := s.Load("eng")
	require.NoError(t, err)
	assert.Len(t, loaded.Members, 2)
	assert.Len(t, loaded.Collaborations, 1)
	assert.Equal(t, "dev", loaded.Collaborations[0].From)
	assert.Equal(t, "qa", loaded.Collaborations[0].To)
}

func TestStore_LoadNotFound_Sentinel(t *testing.T) {
	s := testStore(t)

	_, err := s.Load("nonexistent")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotFound))
}

func TestStore_FindByRepo(t *testing.T) {
	tests := []struct {
		name      string
		teams     []*Team
		repo      string
		wantNames []string
	}{
		{
			name: "matches one team",
			teams: []*Team{
				{Name: "eng", Repositories: []string{"punt-labs/ethos"}, Members: []Member{{Identity: "a", Role: "r"}}},
				{Name: "ops", Repositories: []string{"punt-labs/infra"}, Members: []Member{{Identity: "b", Role: "r"}}},
			},
			repo:      "punt-labs/ethos",
			wantNames: []string{"eng"},
		},
		{
			name: "matches multiple teams",
			teams: []*Team{
				{Name: "eng", Repositories: []string{"punt-labs/ethos"}, Members: []Member{{Identity: "a", Role: "r"}}},
				{Name: "platform", Repositories: []string{"punt-labs/ethos", "punt-labs/infra"}, Members: []Member{{Identity: "b", Role: "r"}}},
			},
			repo:      "punt-labs/ethos",
			wantNames: []string{"eng", "platform"},
		},
		{
			name: "matches none",
			teams: []*Team{
				{Name: "eng", Repositories: []string{"punt-labs/ethos"}, Members: []Member{{Identity: "a", Role: "r"}}},
			},
			repo:      "punt-labs/other",
			wantNames: nil,
		},
		{
			name:      "no teams directory",
			teams:     nil,
			repo:      "punt-labs/ethos",
			wantNames: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testStore(t)
			for _, team := range tt.teams {
				require.NoError(t, s.Save(team, alwaysTrue, alwaysTrue))
			}

			got, err := s.FindByRepo(tt.repo)
			require.NoError(t, err)
			require.NotNil(t, got, "FindByRepo must return non-nil slice")

			var gotNames []string
			for _, team := range got {
				gotNames = append(gotNames, team.Name)
			}
			if tt.wantNames == nil {
				assert.Empty(t, got)
			} else {
				assert.ElementsMatch(t, tt.wantNames, gotNames)
			}
		})
	}
}
