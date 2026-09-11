package repomiss

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestMissingRefString(t *testing.T) {
	cases := []struct {
		name string
		ref  MissingRef
		want string
	}{
		{
			name: "attribute kind reads as the path it names",
			ref:  MissingRef{Kind: KindTalent, Slug: "engineering", Path: ".punt-labs/ethos/talents/engineering.md"},
			want: "talents/engineering",
		},
		{
			name: "identity",
			ref:  MissingRef{Kind: KindIdentity, Slug: "bwk", Path: ".punt-labs/ethos/identities/bwk.yaml"},
			want: "identities/bwk",
		},
		{
			name: "ext is the one kind that is not a store directory",
			ref:  MissingRef{Kind: KindExt, Slug: "quarry", Path: ".punt-labs/ethos/identities/bwk.ext/quarry.yaml"},
			want: "ext/quarry",
		},
		{
			name: "path does not appear in the short form",
			ref:  MissingRef{Kind: KindRole, Slug: "coo", Path: "/elsewhere/roles/coo.yaml"},
			want: "roles/coo",
		},
		{
			name: "zero value",
			ref:  MissingRef{},
			want: "/",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ref.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestErrorNamesEveryMissAndItsPath pins the aggregate contract: the
// message must be actionable on its own, so every miss and the repo
// path ethos looked in have to survive into the text.
func TestErrorNamesEveryMissAndItsPath(t *testing.T) {
	err := &ErrIncompleteRepoSet{
		Handle: "bwk",
		Missing: []MissingRef{
			{Kind: KindPersonality, Slug: "principal-engineer", Path: ".punt-labs/ethos/personalities/principal-engineer.md"},
			{Kind: KindTalent, Slug: "engineering", Path: ".punt-labs/ethos/talents/engineering.md"},
			{Kind: KindWritingStyle, Slug: "kernighan", Path: ".punt-labs/ethos/writing-styles/kernighan.md"},
		},
	}
	msg := err.Error()
	for _, m := range err.Missing {
		if !strings.Contains(msg, m.String()) {
			t.Errorf("Error() = %q, missing ref %q", msg, m)
		}
		if !strings.Contains(msg, m.Path) {
			t.Errorf("Error() = %q, missing path %q", msg, m.Path)
		}
	}
	if !strings.Contains(msg, `"bwk"`) {
		t.Errorf("Error() = %q, does not name the handle", msg)
	}
}

// TestErrorAdvisesApply guards the load-bearing flag. `ethos vendor
// <handle>` alone only plans, so advice without --apply leaves the user
// exactly as broken as before running it.
func TestErrorAdvisesApply(t *testing.T) {
	err := &ErrIncompleteRepoSet{
		Handle:  "bwk",
		Missing: []MissingRef{{Kind: KindRole, Slug: "coo", Path: "roles/coo.yaml"}},
	}
	want := "ethos vendor bwk --apply"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("Error() = %q, want it to advise %q", err.Error(), want)
	}
}

func TestNewReturnsNilWhenNothingIsMissing(t *testing.T) {
	cases := []struct {
		name    string
		missing []MissingRef
	}{
		{name: "nil slice", missing: nil},
		{name: "empty slice", missing: []MissingRef{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := New("bwk", tc.missing); err != nil {
				t.Errorf("New(%q, %v) = %v, want nil", "bwk", tc.missing, err)
			}
		})
	}
}

// TestNewIsRecoverableWithErrorsAs pins the type callers unwrap to.
// cmd/ethos, internal/mcp, internal/vendor, and internal/hook all
// branch on errors.As against *ErrIncompleteRepoSet; returning a
// different concrete type would silently demote those branches.
func TestNewIsRecoverableWithErrorsAs(t *testing.T) {
	refs := []MissingRef{{Kind: KindTalent, Slug: "engineering", Path: "talents/engineering.md"}}
	err := New("bwk", refs)
	if err == nil {
		t.Fatal("New with one miss returned nil")
	}
	var incomplete *ErrIncompleteRepoSet
	if !errors.As(err, &incomplete) {
		t.Fatalf("errors.As did not recover *ErrIncompleteRepoSet from %T", err)
	}
	if incomplete.Handle != "bwk" {
		t.Errorf("Handle = %q, want %q", incomplete.Handle, "bwk")
	}
	if len(incomplete.Missing) != 1 || incomplete.Missing[0] != refs[0] {
		t.Errorf("Missing = %v, want %v", incomplete.Missing, refs)
	}
}

// TestNewWrappedErrorIsRecoverable covers the callers that wrap the
// aggregate with %w before it reaches the errors.As branch.
func TestNewWrappedErrorIsRecoverable(t *testing.T) {
	err := fmt.Errorf("resolving identity %q: %w", "bwk",
		New("bwk", []MissingRef{{Kind: KindRole, Slug: "coo", Path: "roles/coo.yaml"}}))
	var incomplete *ErrIncompleteRepoSet
	if !errors.As(err, &incomplete) {
		t.Fatalf("errors.As did not recover *ErrIncompleteRepoSet from wrapped %v", err)
	}
}

func TestNewSortsMissing(t *testing.T) {
	unsorted := []MissingRef{
		{Kind: KindTalent, Slug: "engineering"},
		{Kind: KindIdentity, Slug: "bwk"},
		{Kind: KindIdentity, Slug: "adb"},
	}
	err := New("bwk", unsorted)
	var incomplete *ErrIncompleteRepoSet
	if !errors.As(err, &incomplete) {
		t.Fatalf("errors.As did not recover *ErrIncompleteRepoSet from %v", err)
	}
	want := []string{"identities/adb", "identities/bwk", "talents/engineering"}
	if got := strs(incomplete.Missing); !equal(got, want) {
		t.Errorf("Missing = %v, want %v", got, want)
	}
}

// TestNewDoesNotMutateCaller matters because the four layered stores
// keep the miss slice they hand to New — identity.Identity.MissingExt
// is read again after the error is built.
func TestNewDoesNotMutateCaller(t *testing.T) {
	caller := []MissingRef{
		{Kind: KindTalent, Slug: "engineering"},
		{Kind: KindIdentity, Slug: "bwk"},
	}
	before := strs(caller)
	_ = New("bwk", caller)
	if got := strs(caller); !equal(got, before) {
		t.Errorf("New reordered the caller's slice: %v, want %v", got, before)
	}
}

func TestSorted(t *testing.T) {
	cases := []struct {
		name string
		in   []MissingRef
		want []string
	}{
		{
			name: "nil",
			in:   nil,
			want: nil,
		},
		{
			name: "empty",
			in:   []MissingRef{},
			want: nil,
		},
		{
			name: "single entry",
			in:   []MissingRef{{Kind: KindRole, Slug: "coo"}},
			want: []string{"roles/coo"},
		},
		{
			name: "kind before slug",
			in: []MissingRef{
				{Kind: KindTalent, Slug: "aardvark"},
				{Kind: KindIdentity, Slug: "zed"},
			},
			want: []string{"identities/zed", "talents/aardvark"},
		},
		{
			name: "slug breaks a kind tie",
			in: []MissingRef{
				{Kind: KindTeam, Slug: "engineering"},
				{Kind: KindTeam, Slug: "design"},
			},
			want: []string{"teams/design", "teams/engineering"},
		},
		{
			name: "all seven kinds",
			in: []MissingRef{
				{Kind: KindWritingStyle, Slug: "kernighan"},
				{Kind: KindTeam, Slug: "engineering"},
				{Kind: KindTalent, Slug: "go"},
				{Kind: KindRole, Slug: "coo"},
				{Kind: KindPersonality, Slug: "principal-engineer"},
				{Kind: KindIdentity, Slug: "bwk"},
				{Kind: KindExt, Slug: "quarry"},
			},
			want: []string{
				"ext/quarry",
				"identities/bwk",
				"personalities/principal-engineer",
				"roles/coo",
				"talents/go",
				"teams/engineering",
				"writing-styles/kernighan",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := strs(Sorted(tc.in)); !equal(got, tc.want) {
				t.Errorf("Sorted(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestSortedIsStableAcrossInputOrder proves the property the doc
// comment claims: the diagnostic must not change between runs, or it is
// unusable in CI output. Every permutation of the same misses has to
// produce one message.
func TestSortedIsStableAcrossInputOrder(t *testing.T) {
	a := MissingRef{Kind: KindIdentity, Slug: "adb", Path: "identities/adb.yaml"}
	b := MissingRef{Kind: KindIdentity, Slug: "bwk", Path: "identities/bwk.yaml"}
	c := MissingRef{Kind: KindTalent, Slug: "engineering", Path: "talents/engineering.md"}
	permutations := [][]MissingRef{
		{a, b, c}, {a, c, b}, {b, a, c}, {b, c, a}, {c, a, b}, {c, b, a},
	}
	want := New("bwk", []MissingRef{a, b, c}).Error()
	for i, p := range permutations {
		if got := New("bwk", p).Error(); got != want {
			t.Errorf("permutation %d: Error() = %q, want %q", i, got, want)
		}
	}
}

// TestSortedReturnsACopy is the property most likely to be lost to a
// future "sort in place, it is already a slice" optimization, and it is
// invisible without a test: the caller's ordering survives.
func TestSortedReturnsACopy(t *testing.T) {
	caller := []MissingRef{
		{Kind: KindTalent, Slug: "engineering"},
		{Kind: KindIdentity, Slug: "bwk"},
	}
	before := strs(caller)

	out := Sorted(caller)
	if got := strs(caller); !equal(got, before) {
		t.Fatalf("Sorted reordered its input: %v, want %v", got, before)
	}

	// A copy must not share backing storage either: writing through the
	// result must leave the caller's slice alone.
	out[0] = MissingRef{Kind: KindRole, Slug: "overwritten"}
	if got := strs(caller); !equal(got, before) {
		t.Errorf("writing to the result changed the input: %v, want %v", got, before)
	}
}

// strs renders refs in their short form so a failure prints the
// ordering under test rather than a wall of struct literals.
func strs(refs []MissingRef) []string {
	if len(refs) == 0 {
		return nil
	}
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.String()
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
