package mission

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func contractRequiringRead(id string, entry string) *Contract {
	c := newContract(id)
	c.Preconditions = []Precondition{{
		Form: PreconditionFormExplicit, RequireRead: []string{entry}, Message: "read it first",
	}}
	return c
}

// TestCreate_RefusesAnAmbiguousRequireRead: an entry whose placeholder is at a
// path boundary (cache/-~-project/config.json) is stored unchanged and expanded
// at evaluation to a different file, so it is refused when it is written, for
// Create and for an Update that adds it; an entry an Update leaves alone is not
// judged again; an input substituted into an entry is judged too.
func TestCreate_RefusesAnAmbiguousRequireRead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := NewStoreWithRoots(t.TempDir(), t.TempDir())

	err := s.Create(contractRequiringRead("m-2026-08-06-170", "cache/-~-project/config.json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "redaction placeholder")
	assert.Contains(t, err.Error(), "require_read[0]")

	// A name with the token inside it round-trips and is accepted.
	require.NoError(t, s.Create(contractRequiringRead("m-2026-08-06-171", "src/a-~b")))

	// An Update that adds an ambiguous entry is refused.
	c := newContract("m-2026-08-06-172")
	c.WriteSet = []string{"docs/"}
	require.NoError(t, s.Create(c))
	loaded, err := s.Load(c.MissionID)
	require.NoError(t, err)
	loaded.Preconditions = []Precondition{{Form: PreconditionFormExplicit, RequireRead: []string{"cache/-~-project/config.json"}, Message: "m"}}
	err = s.Update(loaded)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "redaction placeholder")

	// An input substituted into an entry is judged as the entry is.
	sub := newContract("m-2026-08-06-173")
	sub.WriteSet = []string{"pkg/"}
	sub.Inputs.Files = []string{"cache/-~-project/config.json"}
	sub.Preconditions = []Precondition{{Form: PreconditionFormExplicit, RequireRead: []string{"${inputs.files.0}"}, Message: "m"}}
	err = s.Create(sub)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "redaction placeholder")
}

// TestCreate_AcceptsAHomeRelativeRequireRead: a leading ~ or ~/ in a require_read
// entry is the evaluator's own home placeholder, not an ambiguity, so it is
// accepted (as an absolute $HOME/... input is); a boundary token that Path would
// produce from a different spelling is still refused.
func TestCreate_AcceptsAHomeRelativeRequireRead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := NewStoreWithRoots(t.TempDir(), t.TempDir())
	n := 0
	id := func() string { n++; return "m-2026-08-06-18" + string(rune('0'+n)) }
	ws := func(c *Contract) *Contract { c.WriteSet = []string{"w" + string(rune('0'+n)) + "/"}; return c }

	for _, entry := range []string{"~/.claude/CLAUDE.md", "~"} {
		assert.NoError(t, s.Create(ws(contractRequiringRead(id(), entry))), entry)
	}
	sub := newContract(id())
	sub.WriteSet = []string{"sub/"}
	sub.Inputs.Files = []string{"~/.claude/CLAUDE.md"}
	sub.Preconditions = []Precondition{{Form: PreconditionFormExplicit, RequireRead: []string{"${inputs.files.0}"}, Message: "m"}}
	assert.NoError(t, s.Create(sub), "an input that substitutes to a home path")

	for _, entry := range []string{"cache/-~-project/config.json", "<repo>/x", "~/x/-~-y"} {
		err := s.Create(ws(contractRequiringRead(id(), entry)))
		if assert.Error(t, err, entry) {
			assert.Contains(t, err.Error(), "redaction placeholder", entry)
		}
	}
}

// TestUpdate_LeavesAnUnchangedAmbiguousEntryAlone: a stored contract that holds
// an entry admission would now refuse (<repo>/cfg/x.yaml, as an older ethos
// wrote it) can still be updated in another field; only new or changed
// entries are judged.
func TestUpdate_LeavesAnUnchangedAmbiguousEntryAlone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := NewStoreWithRoots(t.TempDir(), t.TempDir())
	c := newContract("m-2026-08-06-190")
	require.NoError(t, s.Create(c))
	path, err := s.ContractPath(c.MissionID)
	require.NoError(t, err)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var stored Contract
	require.NoError(t, yaml.Unmarshal(raw, &stored))
	stored.Preconditions = []Precondition{{Form: PreconditionFormExplicit, RequireRead: []string{"<repo>/cfg/x.yaml"}, Message: "m"}}
	raw, err = yaml.Marshal(&stored)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0o600))

	loaded, err := s.Load(c.MissionID)
	require.NoError(t, err)
	loaded.Budget.Rounds = 4
	require.NoError(t, s.Update(loaded), "the unchanged entry is not judged again")

	loaded, err = s.Load(c.MissionID)
	require.NoError(t, err)
	loaded.Preconditions[0].RequireRead = append(loaded.Preconditions[0].RequireRead, "<repo>/cfg/y.yaml")
	require.Error(t, s.Update(loaded), "a new entry of the same kind is")
}

// TestUpdate_JudgesAnUnchangedTemplateWhoseInputChanged: an entry is skipped only
// when what it substitutes to is what it substituted to in the prior contract.
// ${inputs.files.0} is the same text, but if the input it names changes to an
// ambiguous path, the new value is judged.
func TestUpdate_JudgesAnUnchangedTemplateWhoseInputChanged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := NewStoreWithRoots(t.TempDir(), t.TempDir())
	c := newContract("m-2026-08-06-191")
	c.Inputs.Files = []string{"docs/a.md"}
	c.Preconditions = []Precondition{{Form: PreconditionFormExplicit, RequireRead: []string{"${inputs.files.0}"}, Message: "m"}}
	require.NoError(t, s.Create(c))

	loaded, err := s.Load(c.MissionID)
	require.NoError(t, err)
	loaded.Budget.Rounds = 4
	require.NoError(t, s.Update(loaded), "nothing the entry names changed")

	loaded, err = s.Load(c.MissionID)
	require.NoError(t, err)
	loaded.Inputs.Files = []string{"cache/-~-project/config.json"}
	err = s.Update(loaded)
	require.Error(t, err, "the template is unchanged, its value is not")
	assert.Contains(t, err.Error(), "redaction placeholder")
}
