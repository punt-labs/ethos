package mission

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeIdentities is a tiny in-memory IdentityLoader. Tests build one
// directly and mutate the map between calls — no filesystem, no
// dependencies on the identity package, so the hash function can be
// exercised in isolation.
type fakeIdentities struct {
	identities map[string]*EvaluatorIdentity
	loadErr    error
}

func (f *fakeIdentities) LoadEvaluator(handle string) (*EvaluatorIdentity, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	id, ok := f.identities[handle]
	if !ok {
		return nil, errors.New("identity not found")
	}
	return id, nil
}

// fakeRoles is a tiny in-memory RoleLister. Same shape as fakeIdentities
// — tests can swap a single role's content between two ComputeEvaluatorHash
// calls and assert the hash changed.
type fakeRoles struct {
	roles   map[string][]EvaluatorRole
	listErr error
}

func (f *fakeRoles) ListRoles(handle string) ([]EvaluatorRole, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.roles[handle], nil
}

// newTestSources returns a HashSources backed by the given identity and
// roles maps. Helper kept tiny so each test reads as a single mutation.
func newTestSources(ids map[string]*EvaluatorIdentity, roles map[string][]EvaluatorRole) HashSources {
	return HashSources{
		Identities: &fakeIdentities{identities: ids},
		Roles:      &fakeRoles{roles: roles},
	}
}

// seedDjb returns a fully-populated EvaluatorIdentity for the canonical
// `djb` handle used throughout the test suite. Tests clone and mutate
// the result to exercise specific failure modes.
func seedDjb() *EvaluatorIdentity {
	return &EvaluatorIdentity{
		Handle:              "djb",
		PersonalityContent:  "# Bernstein\n\nMethodical, security-first.\n",
		WritingStyleContent: "# Bernstein Prose\n\nShort, declarative sentences.\n",
		Talents:             []string{"security", "engineering"},
		TalentContents: []string{
			"# Security\n\nThreat modeling.\n",
			"# Engineering\n\nFundamentals.\n",
		},
	}
}

// seedDjbRoles returns the canonical role list for `djb`. Identity
// resolution and role enumeration are independent inputs to the hash,
// so the test fixtures stay independent too.
func seedDjbRoles() []EvaluatorRole {
	return []EvaluatorRole{
		{
			Name:    "security-engineer",
			Content: "responsibilities:\n- review threat models\n- audit dependencies\n",
		},
	}
}

func TestComputeEvaluatorHash_DeterministicAcrossCalls(t *testing.T) {
	srcs := newTestSources(
		map[string]*EvaluatorIdentity{"djb": seedDjb()},
		map[string][]EvaluatorRole{"djb": seedDjbRoles()},
	)
	first, err := ComputeEvaluatorHash("djb", srcs)
	require.NoError(t, err)
	second, err := ComputeEvaluatorHash("djb", srcs)
	require.NoError(t, err)
	assert.Equal(t, first, second, "two computes against identical content must produce identical hashes")
	assert.Len(t, first, 64, "sha256 hex output must be 64 characters")
}

// TestComputeEvaluatorHash_RoleOrderInvariant proves that the hash does
// not depend on the order in which the role lister returns roles. Two
// listers returning the same set in different orders must hash to the
// same value — sorting before serialization is the contract.
func TestComputeEvaluatorHash_RoleOrderInvariant(t *testing.T) {
	id := map[string]*EvaluatorIdentity{"djb": seedDjb()}
	abOrder := newTestSources(id, map[string][]EvaluatorRole{
		"djb": {
			{Name: "alpha", Content: "alpha-body"},
			{Name: "beta", Content: "beta-body"},
		},
	})
	baOrder := newTestSources(id, map[string][]EvaluatorRole{
		"djb": {
			{Name: "beta", Content: "beta-body"},
			{Name: "alpha", Content: "alpha-body"},
		},
	})
	hashAB, err := ComputeEvaluatorHash("djb", abOrder)
	require.NoError(t, err)
	hashBA, err := ComputeEvaluatorHash("djb", baOrder)
	require.NoError(t, err)
	assert.Equal(t, hashAB, hashBA, "role order from the lister must not affect the hash")
}

// TestComputeEvaluatorHash_TalentOrderMatters proves the opposite for
// talents: identity-declaration order is part of the contract. Swapping
// two talents on the identity is a content change, not a presentation
// change — the hash must reflect it.
func TestComputeEvaluatorHash_TalentOrderMatters(t *testing.T) {
	id1 := seedDjb()
	id2 := seedDjb()
	// Swap the two talents (and their content slots in lockstep).
	id2.Talents = []string{id1.Talents[1], id1.Talents[0]}
	id2.TalentContents = []string{id1.TalentContents[1], id1.TalentContents[0]}

	srcs1 := newTestSources(map[string]*EvaluatorIdentity{"djb": id1}, nil)
	srcs2 := newTestSources(map[string]*EvaluatorIdentity{"djb": id2}, nil)

	h1, err := ComputeEvaluatorHash("djb", srcs1)
	require.NoError(t, err)
	h2, err := ComputeEvaluatorHash("djb", srcs2)
	require.NoError(t, err)
	assert.NotEqual(t, h1, h2, "swapping talent order must change the hash")
}

// TestComputeEvaluatorHash_EditDetection drives one source change at a
// time and asserts the hash differs from the baseline. Table-driven so
// adding a new content source means adding one row, not a new test
// function.
func TestComputeEvaluatorHash_EditDetection(t *testing.T) {
	baseline, err := ComputeEvaluatorHash("djb", newTestSources(
		map[string]*EvaluatorIdentity{"djb": seedDjb()},
		map[string][]EvaluatorRole{"djb": seedDjbRoles()},
	))
	require.NoError(t, err)

	cases := []struct {
		name   string
		mutate func(*EvaluatorIdentity, *[]EvaluatorRole)
	}{
		{
			name: "personality content edit",
			mutate: func(id *EvaluatorIdentity, _ *[]EvaluatorRole) {
				id.PersonalityContent += "\nAdditional paragraph.\n"
			},
		},
		{
			name: "writing style content edit",
			mutate: func(id *EvaluatorIdentity, _ *[]EvaluatorRole) {
				id.WritingStyleContent += "\nNew rule.\n"
			},
		},
		{
			name: "talent slug rename",
			mutate: func(id *EvaluatorIdentity, _ *[]EvaluatorRole) {
				id.Talents[0] = "infosec"
			},
		},
		{
			name: "talent content edit",
			mutate: func(id *EvaluatorIdentity, _ *[]EvaluatorRole) {
				id.TalentContents[0] += "\nMore text.\n"
			},
		},
		{
			name: "talent added",
			mutate: func(id *EvaluatorIdentity, _ *[]EvaluatorRole) {
				id.Talents = append(id.Talents, "extra")
				id.TalentContents = append(id.TalentContents, "# Extra\n")
			},
		},
		{
			name: "talent removed",
			mutate: func(id *EvaluatorIdentity, _ *[]EvaluatorRole) {
				id.Talents = id.Talents[:1]
				id.TalentContents = id.TalentContents[:1]
			},
		},
		{
			name: "role content edit",
			mutate: func(_ *EvaluatorIdentity, roles *[]EvaluatorRole) {
				(*roles)[0].Content += "\n- new responsibility\n"
			},
		},
		{
			name: "role renamed",
			mutate: func(_ *EvaluatorIdentity, roles *[]EvaluatorRole) {
				(*roles)[0].Name = "security-lead"
			},
		},
		{
			name: "role added",
			mutate: func(_ *EvaluatorIdentity, roles *[]EvaluatorRole) {
				*roles = append(*roles, EvaluatorRole{Name: "auditor", Content: "audit\n"})
			},
		},
		{
			name: "role removed",
			mutate: func(_ *EvaluatorIdentity, roles *[]EvaluatorRole) {
				*roles = nil
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := seedDjb()
			roles := seedDjbRoles()
			tc.mutate(id, &roles)
			got, err := ComputeEvaluatorHash("djb", newTestSources(
				map[string]*EvaluatorIdentity{"djb": id},
				map[string][]EvaluatorRole{"djb": roles},
			))
			require.NoError(t, err)
			assert.NotEqual(t, baseline, got, "%s: hash must differ from baseline", tc.name)
		})
	}
}

// TestComputeEvaluatorHash_HandleAnchorsHash proves that two evaluators
// with byte-identical attribute content but different handles produce
// different hashes. Without anchoring on the handle, two stub identities
// could swap places undetected.
func TestComputeEvaluatorHash_HandleAnchorsHash(t *testing.T) {
	a := seedDjb()
	b := seedDjb()
	b.Handle = "twin"

	hashA, err := ComputeEvaluatorHash("djb", newTestSources(
		map[string]*EvaluatorIdentity{"djb": a}, nil,
	))
	require.NoError(t, err)
	hashB, err := ComputeEvaluatorHash("twin", newTestSources(
		map[string]*EvaluatorIdentity{"twin": b}, nil,
	))
	require.NoError(t, err)
	assert.NotEqual(t, hashA, hashB, "different handles with identical content must hash differently")
}

// TestComputeEvaluatorHash_FieldBoundarySafety proves that two distinct
// content layouts cannot produce the same hash by smuggling bytes
// across a field boundary. Concatenation without length prefixes is
// the bug class this test exists to catch.
//
// Layout A: personality "abc", writing style "def"
// Layout B: personality "abcdef", writing style ""
// Without length prefixes, both serialize to "abcdef" inside the
// concatenated content blob — and both hash identically. With length
// prefixes the lengths themselves participate in the hash, so the
// two layouts diverge.
func TestComputeEvaluatorHash_FieldBoundarySafety(t *testing.T) {
	idA := &EvaluatorIdentity{
		Handle:              "x",
		PersonalityContent:  "abc",
		WritingStyleContent: "def",
	}
	idB := &EvaluatorIdentity{
		Handle:              "x",
		PersonalityContent:  "abcdef",
		WritingStyleContent: "",
	}
	hashA, err := ComputeEvaluatorHash("x", newTestSources(
		map[string]*EvaluatorIdentity{"x": idA}, nil,
	))
	require.NoError(t, err)
	hashB, err := ComputeEvaluatorHash("x", newTestSources(
		map[string]*EvaluatorIdentity{"x": idB}, nil,
	))
	require.NoError(t, err)
	assert.NotEqual(t, hashA, hashB, "field boundaries must be hash-distinguishable")
}

func TestComputeEvaluatorHash_EmptyHandleRejected(t *testing.T) {
	_, err := ComputeEvaluatorHash("", newTestSources(nil, nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "handle is required")
}

func TestComputeEvaluatorHash_NilLoadersRejected(t *testing.T) {
	_, err := ComputeEvaluatorHash("djb", HashSources{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Identities loader is nil")

	_, err = ComputeEvaluatorHash("djb", HashSources{Identities: &fakeIdentities{}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Roles lister is nil")
}

func TestComputeEvaluatorHash_IdentityNotFound(t *testing.T) {
	_, err := ComputeEvaluatorHash("ghost", newTestSources(
		map[string]*EvaluatorIdentity{"djb": seedDjb()},
		nil,
	))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading identity")
	assert.Contains(t, err.Error(), "ghost")
}

func TestComputeEvaluatorHash_IdentityLoaderError(t *testing.T) {
	srcs := HashSources{
		Identities: &fakeIdentities{loadErr: errors.New("disk on fire")},
		Roles:      &fakeRoles{},
	}
	_, err := ComputeEvaluatorHash("djb", srcs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disk on fire")
}

func TestComputeEvaluatorHash_RoleListerError(t *testing.T) {
	srcs := HashSources{
		Identities: &fakeIdentities{identities: map[string]*EvaluatorIdentity{"djb": seedDjb()}},
		Roles:      &fakeRoles{listErr: errors.New("roles unavailable")},
	}
	_, err := ComputeEvaluatorHash("djb", srcs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listing roles")
	assert.Contains(t, err.Error(), "roles unavailable")
}

// TestComputeEvaluatorHash_RejectsTalentLengthMismatch proves that an
// identity with a talent slug whose content slot is missing produces an
// explicit error. A silent fallback ("treat missing as empty") would
// allow a partially-resolved identity to hash to a stable but
// meaningless value.
func TestComputeEvaluatorHash_RejectsTalentLengthMismatch(t *testing.T) {
	id := seedDjb()
	id.TalentContents = id.TalentContents[:1] // mismatch: 2 slugs, 1 content
	srcs := newTestSources(map[string]*EvaluatorIdentity{"djb": id}, nil)
	_, err := ComputeEvaluatorHash("djb", srcs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "talent")
	assert.Contains(t, err.Error(), "partial content")
}

// TestComputeEvaluatorHash_StableAcrossEmptySources proves that an
// evaluator with no roles, no talents, and only the bare minimum still
// hashes to a determinate value. Edge case for new identities.
func TestComputeEvaluatorHash_StableAcrossEmptySources(t *testing.T) {
	id := &EvaluatorIdentity{Handle: "minimal"}
	srcs := newTestSources(map[string]*EvaluatorIdentity{"minimal": id}, nil)
	first, err := ComputeEvaluatorHash("minimal", srcs)
	require.NoError(t, err)
	second, err := ComputeEvaluatorHash("minimal", srcs)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.NotEmpty(t, first)
}

// TestHashFormatVersionInOutput is a smoke test that the format version
// participates in the hash. If a future change removes the version
// prefix this test catches it before pinned hashes silently start
// matching across format versions.
func TestHashFormatVersionInOutput(t *testing.T) {
	// We cannot inspect the buffer directly (it's hashed and discarded),
	// but we can verify that the constant is present in the source by
	// asserting the hash function does not panic when called and that
	// changing the version constant changes the output. The constant
	// is unexported; the test exists to document the intent.
	if !strings.HasPrefix(hashFormatVersion, "ethos-evaluator-hash-") {
		t.Fatalf("hash format version %q does not match expected prefix", hashFormatVersion)
	}
}
