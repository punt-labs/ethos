package team

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validTeam returns a minimal structurally-valid team. Every test in
// this file that exercises a failure case starts from this shape and
// corrupts exactly one field so the test fails for only the intended
// reason. Shared between TestValidateStructural_Invariants and
// TestValidate_DelegatesToStructural so the two suites stay locked to
// the same baseline — if the baseline is ever corrupted, both suites
// surface it together.
func validTeam() *Team {
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

// structuralInvariantCase is one failure-path fixture: a mutation on
// top of validTeam() and the error-message substring the resulting
// error must contain. Shared between TestValidateStructural_Invariants
// (which calls ValidateStructural directly) and
// TestValidate_DelegatesToStructural (which calls Validate and asserts
// the byte-for-byte equality of the two error strings). One table of
// cases, two tests iterating it — a future refactor that changes an
// error message has to update exactly one place and both tests move
// together.
type structuralInvariantCase struct {
	name      string
	mutate    func(*Team)
	wantError string
}

// structuralInvariantCases enumerates every failure path
// ValidateStructural must reject. Each entry corresponds to one of
// the 9 invariants documented on ValidateStructural's doc comment.
// Members 3 and collaboration empty-from/empty-to are split into
// sub-cases because they are structurally distinct branches inside
// the validator even though the doc comment groups them.
var structuralInvariantCases = []structuralInvariantCase{
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

// TestValidateStructural_Invariants is a table-driven test that hits
// every structural check in ValidateStructural with a minimal fixture
// and asserts the exact error-message substring a future refactor
// must not drop. The baseline (unmutated validTeam) is asserted as
// its own sub-test before the failure cases run; every failure case
// starts from the same validTeam() and corrupts exactly one field.
//
// Error messages are matched with require.ErrorContains (not Equal)
// because some wrap an underlying attribute.ValidateSlug error whose
// text may evolve. The substring anchor each case picks is the part
// of the message Validate callers are known to read.
func TestValidateStructural_Invariants(t *testing.T) {
	t.Run("valid team passes", func(t *testing.T) {
		require.NoError(t, ValidateStructural(validTeam()))
	})

	for _, tc := range structuralInvariantCases {
		t.Run(tc.name, func(t *testing.T) {
			tm := validTeam()
			tc.mutate(tm)
			err := ValidateStructural(tm)
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

// TestValidate_DelegatesToStructural iterates the same failure table
// as TestValidateStructural_Invariants and asserts that, for every
// structural failure, Validate returns the structural error
// byte-for-byte — not wrapped, not augmented, not replaced. This
// locks the contract that callers matching on structural error text
// via direct string comparison or strings.Contains still work after
// the round-1 split. A refactor that wraps any of the 9 errors in
// Validate fails the matching sub-case.
//
// The two always-true callbacks guarantee the identity and role
// existence checks inside Validate are never reached — the structural
// check must fire and return first for every case in the table.
func TestValidate_DelegatesToStructural(t *testing.T) {
	for _, tc := range structuralInvariantCases {
		t.Run(tc.name, func(t *testing.T) {
			tm := validTeam()
			tc.mutate(tm)

			structErr := ValidateStructural(tm)
			require.Error(t, structErr)

			valErr := Validate(tm, alwaysTrue, alwaysTrue)
			require.Error(t, valErr)

			assert.Equal(t, structErr.Error(), valErr.Error(),
				"Validate must return the ValidateStructural error byte-for-byte for %q", tc.name)
		})
	}
}

// TestValidate_StructuralBeforeCallbacks locks the ordering
// invariant: Validate calls ValidateStructural to completion before
// running the identity and role existence callbacks. The fixture has
// BOTH a structural failure (member 0's identity is empty) AND what
// would be a callback failure (member 1's identity is rejected by
// the always-false identityExists callback), so the test can
// distinguish which check fired first by inspecting the error text.
//
// Pre-round-1, Validate interleaved the two check types inside a
// single member loop: identity and role existence were checked
// alongside the duplicate-key check on each iteration. Post-round-1,
// ValidateStructural runs to completion first, then Validate runs
// the callback checks in a separate loop. A future refactor that
// moves the callback check earlier — or merges the two loops again —
// fails this test because the callback error would appear first.
func TestValidate_StructuralBeforeCallbacks(t *testing.T) {
	// Team with two members: member 0 has an empty identity
	// (structural failure on first iteration of the structural
	// loop), member 1 has a non-empty identity that the callback
	// would reject. Only the structural error should surface.
	tm := &Team{
		Name: "eng",
		Members: []Member{
			{Identity: "", Role: "dev"},              // structural: empty
			{Identity: "nonexistent", Role: "lead"}, // callback would reject
		},
	}

	// identityExists rejects every handle. If the callback loop ran
	// first (or interleaved), "nonexistent" would hit this and the
	// returned error would name it. Post-round-1, ValidateStructural
	// short-circuits on member 0's empty identity before any
	// callback is consulted.
	identityExists := func(string) bool { return false }
	roleExists := func(string) bool { return true }

	err := Validate(tm, identityExists, roleExists)
	require.Error(t, err)

	// Positive anchor: the structural error fires first.
	assert.Contains(t, err.Error(), "member 0: identity is required",
		"structural check must fire before callback check")

	// Negative anchor: the callback error must NOT appear. If a
	// future refactor merges the loops or moves the callback check
	// earlier, the error string would carry the callback message
	// instead (or in addition).
	assert.NotContains(t, err.Error(), `identity "nonexistent" not found`,
		"callback check must not fire when structural check already failed")
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
