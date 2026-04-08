package mission

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/role"
	"github.com/punt-labs/ethos/internal/team"
)

// EvaluatorIdentity is the subset of an ethos identity the hash function
// reads. Three fields are scalar attribute slugs (personality, writing
// style); Talents is a positionally indexed pair of slug + content lists.
//
// The struct mirrors `internal/identity.Identity` exactly enough for the
// hash to be computed without importing that package — keeping the
// mission package a leaf in the dependency graph and the hash function
// trivially testable with hand-built fixtures.
type EvaluatorIdentity struct {
	Handle              string
	PersonalityContent  string
	WritingStyleContent string
	Talents             []string // slugs, in identity declaration order
	TalentContents      []string // resolved content, indexed parallel to Talents
}

// EvaluatorRole is the subset of an ethos role the hash function reads.
// Name uniquely identifies the role within its store; Content is a
// canonicalized rendering of the role's responsibilities/permissions/tools
// supplied by the caller.
type EvaluatorRole struct {
	Name    string
	Content string
}

// IdentityLoader resolves an evaluator handle to its full attribute
// content. Implementations are expected to be the existing identity
// store — typically `*identity.LayeredStore` adapted by the caller.
//
// Load returns the resolved identity (with personality, writing style,
// and talent content populated) or an error. A non-existent handle is
// an error, not a nil-without-error sentinel: an empty hash on a
// missing evaluator is exactly the silent bypass DES-033 forbids.
type IdentityLoader interface {
	LoadEvaluator(handle string) (*EvaluatorIdentity, error)
}

// RoleLister enumerates the role names assigned to an evaluator handle
// and resolves each to its content. The two-step shape lets callers
// adapt the existing role store, which has no native "by handle" API,
// without forcing the mission package to walk teams itself.
//
// ListRoles returns role names in any order — the hash function sorts
// them lexicographically before serialization. An empty list is a
// valid result (the evaluator may not be on any team); the hash still
// covers personality, writing style, and talents.
type RoleLister interface {
	ListRoles(handle string) ([]EvaluatorRole, error)
}

// HashSources bundles the dependencies of ComputeEvaluatorHash. Two
// fields, both required: the identity loader and the role lister.
// A nil field is a programmer error and produces an explicit error
// rather than a panic — call sites compose this struct once and reuse it.
type HashSources struct {
	Identities IdentityLoader
	Roles      RoleLister
}

// Validate returns an error if either loader is nil. Cheap to call at
// every entry point so a misconfigured caller fails loudly at the
// hash boundary instead of silently producing a sha256 of empty input.
func (h HashSources) Validate() error {
	if h.Identities == nil {
		return errors.New("hash sources: Identities loader is nil")
	}
	if h.Roles == nil {
		return errors.New("hash sources: Roles lister is nil")
	}
	return nil
}

// Field separators for the hash serialization. Bytes 0x1E (Record
// Separator) and 0x1F (Unit Separator) are non-printable ASCII control
// codes that cannot legally appear inside a slug, handle, or markdown
// content body the way ethos uses them — and identity validation
// already rejects control characters in handles. Using them as
// section/field delimiters keeps the format unambiguous: a stray
// newline in a personality file does not collide with a section break.
const (
	fieldSep   = "\x1f" // between (label, value) pairs
	sectionSep = "\x1e" // between sections
)

// Section labels. Stable strings — changing any of these is a hash
// format break that invalidates every existing pinned mission. The
// labels themselves are part of the hash input so a value swap
// between two sections (e.g. moving the personality content into the
// writing style slot) produces a different hash even when the bytes
// are otherwise identical.
const (
	labelHandle       = "handle"
	labelPersonality  = "personality"
	labelWritingStyle = "writing_style"
	labelTalent       = "talent"
	labelRole         = "role"
)

// hashFormatVersion is prepended to the serialized form so a future
// algorithm change can be made backward-compatible if needed: a
// pinned hash from version v1 will not collide with a freshly
// computed hash under v2, and the verifier can refuse v1 hashes
// once v2 ships. Bumped only on intentional format changes.
const hashFormatVersion = "ethos-evaluator-hash-v1"

// ComputeEvaluatorHash returns the deterministic sha256 (hex) of every
// content source that could influence the evaluator's verdict.
//
// Sources hashed (DES-033, in this exact order):
//
//  1. The evaluator handle itself. Two evaluators sharing identical
//     attribute content (e.g. two stub personalities pointing at the
//     same .md file) must still produce different hashes — the handle
//     anchors the contract to a specific identity, not just to a body
//     of text.
//  2. Personality content (markdown body, byte-for-byte).
//  3. Writing style content (markdown body, byte-for-byte).
//  4. Each talent, in identity declaration order, as a (slug, content)
//     pair. Order matters: swapping two talents on the identity
//     yields a different hash even when the union of slug+content
//     is unchanged.
//  5. Each role assignment for this handle, sorted lexicographically
//     by role name, as a (name, content) pair. Sorting is required
//     because RoleLister implementations are free to walk teams in
//     map iteration order — sorting before hashing is what makes the
//     output stable across processes and across operating systems.
//
// Serialization is length-prefixed and separator-delimited so an
// adversary cannot smuggle content from one field into another by
// embedding a separator byte. Each field encodes as
//
//	<label> SEP <decimal length> SEP <value>
//
// and each section encodes as one or more fields followed by a
// section separator. The separators are ASCII control bytes (0x1E,
// 0x1F) that ethos identity validation already rejects from handles
// and slug names; collisions inside markdown bodies are absorbed by
// the explicit length prefix.
//
// Returns the hex-encoded hash on success. Errors come from the
// upstream loaders (identity not found, role load failure) or from
// nil sources — every error is wrapped with the operation context.
func ComputeEvaluatorHash(handle string, sources HashSources) (string, error) {
	if strings.TrimSpace(handle) == "" {
		return "", errors.New("computing evaluator hash: handle is required")
	}
	if err := sources.Validate(); err != nil {
		return "", fmt.Errorf("computing evaluator hash: %w", err)
	}

	id, err := sources.Identities.LoadEvaluator(handle)
	if err != nil {
		return "", fmt.Errorf("computing evaluator hash: loading identity %q: %w", handle, err)
	}
	if id == nil {
		return "", fmt.Errorf("computing evaluator hash: identity %q resolved to nil", handle)
	}
	if len(id.TalentContents) != len(id.Talents) {
		return "", fmt.Errorf(
			"computing evaluator hash: identity %q has %d talents but %d talent contents — refusing to hash partial content",
			handle, len(id.Talents), len(id.TalentContents),
		)
	}

	roles, err := sources.Roles.ListRoles(handle)
	if err != nil {
		return "", fmt.Errorf("computing evaluator hash: listing roles for %q: %w", handle, err)
	}
	// Sort by name so map-iteration order in the lister cannot leak
	// into the output. ListRoles is contractually unordered.
	sort.Slice(roles, func(i, j int) bool { return roles[i].Name < roles[j].Name })

	var buf strings.Builder
	buf.Grow(1024)
	buf.WriteString(hashFormatVersion)
	buf.WriteString(sectionSep)

	writeField(&buf, labelHandle, id.Handle)
	buf.WriteString(sectionSep)

	writeField(&buf, labelPersonality, id.PersonalityContent)
	buf.WriteString(sectionSep)

	writeField(&buf, labelWritingStyle, id.WritingStyleContent)
	buf.WriteString(sectionSep)

	for i, slug := range id.Talents {
		writeField(&buf, labelTalent+":slug", slug)
		writeField(&buf, labelTalent+":content", id.TalentContents[i])
		buf.WriteString(sectionSep)
	}

	for _, r := range roles {
		writeField(&buf, labelRole+":name", r.Name)
		writeField(&buf, labelRole+":content", r.Content)
		buf.WriteString(sectionSep)
	}

	sum := sha256.Sum256([]byte(buf.String()))
	return hex.EncodeToString(sum[:]), nil
}

// writeField appends a length-prefixed (label, value) pair to the
// hash buffer. The decimal length is the byte count of value, not the
// rune count — the hash operates on bytes throughout. The trailing
// fieldSep terminates the field and gives the next writeField call
// (or the section break) a clean attachment point.
func writeField(buf *strings.Builder, label, value string) {
	buf.WriteString(label)
	buf.WriteString(fieldSep)
	buf.WriteString(fmt.Sprintf("%d", len(value)))
	buf.WriteString(fieldSep)
	buf.WriteString(value)
	buf.WriteString(fieldSep)
}

// NewLiveHashSources adapts the live identity, role, and team stores
// into a HashSources value. CLI, MCP, and the verifier hook all build
// their HashSources here so the resolution rules stay in lockstep.
//
// The adapter does two jobs:
//
//  1. Loads the evaluator identity in resolved-content mode (no
//     Reference flag), so personality, writing style, and talent
//     contents are populated for the hash to read.
//  2. Walks every team to find every role assignment for the
//     evaluator's handle. Roles are returned unsorted;
//     ComputeEvaluatorHash sorts before serialization.
//
// A nil role or team store is treated as "no role assignments" so a
// stripped-down install (or a test fixture) can still hash the
// personality + writing style + talents portion of an identity.
func NewLiveHashSources(
	identities identity.IdentityStore,
	roles *role.LayeredStore,
	teams *team.LayeredStore,
) HashSources {
	return HashSources{
		Identities: &liveIdentityLoader{store: identities},
		Roles:      &liveRoleLister{teams: teams, roles: roles},
	}
}

// liveIdentityLoader wraps an identity.IdentityStore and exposes the
// LoadEvaluator method ComputeEvaluatorHash needs. The adapter is the
// only place that knows how to translate the identity package's
// in-memory shape into the mission package's narrower view.
type liveIdentityLoader struct {
	store identity.IdentityStore
}

// LoadEvaluator resolves the handle to an EvaluatorIdentity. The
// underlying store is called WITHOUT Reference(true) so attribute
// content is populated — the hash needs the bytes, not the slugs.
//
// Identity resolution warnings (a missing personality .md file is the
// usual cause) are surfaced as a fatal error rather than silently
// dropped. A partially resolved evaluator is exactly the silent-bypass
// case the contract forbids.
func (a *liveIdentityLoader) LoadEvaluator(handle string) (*EvaluatorIdentity, error) {
	if a.store == nil {
		return nil, errors.New("identity store is nil")
	}
	id, err := a.store.Load(handle)
	if err != nil {
		return nil, err
	}
	if len(id.Warnings) > 0 {
		return nil, fmt.Errorf(
			"identity %q has %d unresolved attribute warnings: %v",
			handle, len(id.Warnings), id.Warnings,
		)
	}
	return &EvaluatorIdentity{
		Handle:              id.Handle,
		PersonalityContent:  id.PersonalityContent,
		WritingStyleContent: id.WritingStyleContent,
		Talents:             append([]string(nil), id.Talents...),
		TalentContents:      append([]string(nil), id.TalentContents...),
	}, nil
}

// liveRoleLister walks the team store to discover role assignments
// for an evaluator handle and loads each role's content from the
// role store. Two-step lookup is necessary because the role store
// has no "by identity" index — role-to-identity binding lives in
// team membership.
type liveRoleLister struct {
	teams *team.LayeredStore
	roles *role.LayeredStore
}

// ListRoles returns every (team, role) assignment for the given
// handle, with content. A nil team or role store yields a nil slice.
//
// Failures from the role store on a role that IS referenced by a team
// membership are fatal — a missing role .yaml when the team file
// claims the binding is filesystem corruption that ComputeEvaluatorHash
// must not paper over.
//
// Each role name is prefixed with the team name (`team/role`) so two
// identical role names on different teams are distinguishable in the
// hash. Without the team prefix, an evaluator on two teams could lose
// one assignment to map de-duplication after the sort step.
func (a *liveRoleLister) ListRoles(handle string) ([]EvaluatorRole, error) {
	if a.teams == nil || a.roles == nil {
		return nil, nil
	}
	teamNames, err := a.teams.List()
	if err != nil {
		return nil, fmt.Errorf("listing teams: %w", err)
	}
	var out []EvaluatorRole
	for _, name := range teamNames {
		t, err := a.teams.Load(name)
		if err != nil {
			return nil, fmt.Errorf("loading team %q: %w", name, err)
		}
		for _, m := range t.Members {
			if m.Identity != handle {
				continue
			}
			r, err := a.roles.Load(m.Role)
			if err != nil {
				return nil, fmt.Errorf("loading role %q for team %q: %w", m.Role, t.Name, err)
			}
			out = append(out, EvaluatorRole{
				Name:    t.Name + "/" + m.Role,
				Content: canonicalRoleContent(r),
			})
		}
	}
	return out, nil
}

// canonicalRoleContent renders a role into a stable byte representation
// for hashing. Marshaling via yaml.Marshal would be nondeterministic
// across releases (key ordering, indent style); a hand-rolled format
// keeps the bytes stable and the failure modes obvious.
//
// Format: one `key=value\n` line per scalar field, then one line per
// element of the responsibilities, permissions, and tools slices.
// Each slice is rendered in declaration order — reordering a
// responsibility on the role file is a content change the hash must
// reflect.
func canonicalRoleContent(r *role.Role) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("name=")
	b.WriteString(r.Name)
	b.WriteByte('\n')
	b.WriteString("model=")
	b.WriteString(r.Model)
	b.WriteByte('\n')
	for _, resp := range r.Responsibilities {
		b.WriteString("responsibility=")
		b.WriteString(resp)
		b.WriteByte('\n')
	}
	for _, perm := range r.Permissions {
		b.WriteString("permission=")
		b.WriteString(perm)
		b.WriteByte('\n')
	}
	for _, tool := range r.Tools {
		b.WriteString("tool=")
		b.WriteString(tool)
		b.WriteByte('\n')
	}
	return b.String()
}
