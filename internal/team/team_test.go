package team

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateStructural_Invariants is a table-driven test that hits
// every structural check in ValidateStructural with a minimal fixture
// and asserts the exact error-message substring a future refactor
// must not drop. Each case corresponds to one of the 9 invariants
// documented on ValidateStructural's doc comment. The valid baseline
// is the first case; every other case starts from that shape and
// corrupts exactly one field so the test fails for only the intended
// reason.
//
// Error messages are matched with assert.Contains (not Equal) because
// some wrap an underlying attribute.ValidateSlug error whose text may
// evolve. The substring anchors every case picks is the part of the
// message Validate callers are known to read.
func TestValidateStructural_Invariants(t *testing.T) {
	valid := func() *Team {
		return &Team{
			Name: "eng",
			Members: []Member{
				{Identity: "alice", Role: "dev"},
				{Identity: "bob", Role: "lead"},
			},
			Collaborations: []Collaboration{
				{From: "dev", To: "lead", Type: "reports_to"},
			},
		}
	}

	tests := []struct {
		name      string
		mutate    func(t *Team)
		wantError string // empty = expect no error
	}{
		{
			name:      "valid team passes",
			mutate:    func(*Team) {},
			wantError: "",
		},
		{
			name:      "1: invalid team name slug",
			mutate:    func(t *Team) { t.Name = "BAD NAME" },
			wantError: "invalid team name",
		},
		{
			name:      "2: zero members",
			mutate:    func(t *Team) { t.Members = nil },
			wantError: `team "eng" must have at least one member`,
		},
		{
			name: "3a: member with empty identity",
			mutate: func(t *Team) {
				t.Members[0].Identity = ""
			},
			wantError: "member 0: identity is required",
		},
		{
			name: "3b: member with empty role",
			mutate: func(t *Team) {
				t.Members[0].Role = ""
			},
			wantError: "member 0: role is required",
		},
		{
			name: "4: duplicate (identity, role) pair",
			mutate: func(t *Team) {
				t.Members = append(t.Members, Member{Identity: "alice", Role: "dev"})
			},
			wantError: "member 2: duplicate assignment (alice, dev)",
		},
		{
			name: "5a: collaboration with empty from",
			mutate: func(t *Team) {
				t.Collaborations[0].From = ""
			},
			wantError: "collaboration 0: from and to are required",
		},
		{
			name: "5b: collaboration with empty to",
			mutate: func(t *Team) {
				t.Collaborations[0].To = ""
			},
			wantError: "collaboration 0: from and to are required",
		},
		{
			name: "6: self-collaboration",
			mutate: func(t *Team) {
				t.Collaborations[0].To = "dev"
			},
			wantError: "collaboration 0: self-collaboration not allowed (dev)",
		},
		{
			name: "7: invalid collaboration type",
			mutate: func(t *Team) {
				t.Collaborations[0].Type = "manages"
			},
			wantError: `collaboration 0: invalid type "manages"`,
		},
		{
			name: "8: collaboration from role not filled by any member",
			mutate: func(t *Team) {
				t.Collaborations[0].From = "ghost"
			},
			wantError: `collaboration 0: role "ghost" not filled by any member`,
		},
		{
			name: "9: collaboration to role not filled by any member",
			mutate: func(t *Team) {
				t.Collaborations[0].To = "ghost"
			},
			wantError: `collaboration 0: role "ghost" not filled by any member`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tm := valid()
			tc.mutate(tm)
			err := ValidateStructural(tm)
			if tc.wantError == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.ErrorContains(t, err, tc.wantError)
		})
	}
}

// TestValidateStructural_NoCallbacks confirms the invariant that
// ValidateStructural does not depend on identity or role existence
// callbacks — it is a pure function of *Team. A team with a member
// referencing a handle that does not exist in any store still passes
// ValidateStructural as long as the structural rules hold. Only the
// fuller Validate rejects unknown identities and roles.
func TestValidateStructural_NoCallbacks(t *testing.T) {
	tm := &Team{
		Name: "eng",
		Members: []Member{
			// "unknown-handle" is not in any store. ValidateStructural
			// cannot and does not check for its existence.
			{Identity: "unknown-handle", Role: "unknown-role"},
		},
	}
	err := ValidateStructural(tm)
	require.NoError(t, err,
		"ValidateStructural must accept teams with unknown identities and roles")
}

// TestValidate_DelegatesToStructural confirms that Validate runs
// ValidateStructural first and returns its error directly — byte-for-
// byte, not re-wrapped. A caller that currently matches on the
// structural error message must still match after Validate adds its
// callback checks on top.
func TestValidate_DelegatesToStructural(t *testing.T) {
	tm := &Team{
		Name:    "eng",
		Members: []Member{{Identity: "", Role: "dev"}}, // empty identity → structural failure
	}

	structErr := ValidateStructural(tm)
	require.Error(t, structErr)

	valErr := Validate(tm, alwaysTrue, alwaysTrue)
	require.Error(t, valErr)

	// Byte-for-byte equality: Validate must return the structural
	// error unchanged, not wrapped. A refactor that adds a wrap
	// ("validating team: <structural>") would break callers that
	// match on structural error text via errors.Is or strings.Contains
	// with a specific prefix.
	assert.Equal(t, structErr.Error(), valErr.Error(),
		"Validate must return the ValidateStructural error unchanged")
}

// TestValidate_NilCallbacks confirms Validate still enforces the
// non-nil callback contract. The error is unchanged from the pre-2z2
// behavior so callers that assert on this message still work.
func TestValidate_NilCallbacks(t *testing.T) {
	tm := &Team{
		Name:    "eng",
		Members: []Member{{Identity: "a", Role: "r"}},
	}

	err := Validate(tm, nil, alwaysTrue)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "callbacks must not be nil")

	err = Validate(tm, alwaysTrue, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "callbacks must not be nil")
}
