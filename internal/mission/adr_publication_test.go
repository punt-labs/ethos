package mission_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/adr"
	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/provenance"
)

func TestADRPublicationValidatesRepositoryMetadata(t *testing.T) {
	fields := []struct {
		name string
		set  func(*adr.ADR, string)
	}{
		{"author", func(a *adr.ADR, value string) { a.Author = value }},
		{"mission_id", func(a *adr.ADR, value string) { a.MissionID = value }},
		{"bead_id", func(a *adr.ADR, value string) { a.BeadID = value }},
	}
	for _, field := range fields {
		for _, value := range []string{"password=synthetic-only", "person@example.invalid"} {
			for _, selection := range []string{"explicit", "linked"} {
				if field.name == "mission_id" && selection == "linked" {
					continue // A malformed mission reference cannot link to this mission.
				}
				t.Run(field.name+"/"+value+"/"+selection, func(t *testing.T) {
					s, publisher, git := adrPublicationFixture(t)
					const id = "m-2026-11-04-001"
					a := adr.ADR{ID: "DES-001", Title: "decision", Status: adr.StatusSettled,
						CreatedAt: "2026-11-04T10:00:00Z", UpdatedAt: "2026-11-04T10:00:00Z",
						Author: "bwk", Decision: "use the repository validator", BeadID: "ethos-07m.5"}
					var explicit []string
					if selection == "linked" {
						a.MissionID = id
					} else {
						explicit = []string{a.ID}
					}
					path := filepath.Join(publisher.Policy.RecordRoot(), "adrs", a.ID+".yaml")
					writeADRPublicationYAML(t, path, a)
					valid, err := s.SnapshotSelection(t.Context(), []string{id}, explicit)
					require.NoError(t, err, "valid metadata remains publishable")

					// Simulate an external edit, bypassing the repository ADR writer.
					field.set(&a, value)
					require.NoError(t, a.Validate(), "ordinary ADR validation does not check these semantic fields")
					bad := writeADRPublicationYAML(t, path, a)
					snapshot, err := s.SnapshotSelection(t.Context(), []string{id}, explicit)
					assertADRPublicationMetadataError(t, err, field.name, value)
					assert.Empty(t, snapshot.Records)

					// Also drive the real publisher with a caller-owned snapshot. The
					// sink must not rely on the snapshot producer having validated it.
					for i := range valid.Records {
						if valid.Records[i].Path == ".punt-labs/ethos/adrs/DES-001.yaml" {
							valid.Records[i].Data = bad
							break
						}
					}
					index := filepath.Join(publisher.Repository.Root, ".git", "index")
					before, err := os.ReadFile(index)
					require.NoError(t, err)
					receipt, err := publisher.Publish(t.Context(), valid, "HEAD")
					assertADRPublicationMetadataError(t, err, field.name, value)
					assert.False(t, receipt.IndexInstalled)
					assert.Empty(t, receipt.Copied)
					after, err := os.ReadFile(index)
					require.NoError(t, err)
					assert.Equal(t, before, after, "the actual index must remain byte-identical")
					assert.Equal(t, "staged\n", git("show", ":unrelated.txt"))
					assert.NoDirExists(t, publisher.Policy.CheckoutRecordRoot(), "refusal precedes any publication destination creation")
					source, err := os.ReadFile(path)
					require.NoError(t, err)
					assert.Equal(t, bad, source, "refusal must not rewrite source authority")
				})
			}
		}
	}
}

func assertADRPublicationMetadataError(t *testing.T, err error, field, value string) {
	t.Helper()
	var invalid *adr.ValidationError
	if assert.ErrorAs(t, err, &invalid) {
		assert.Equal(t, field, invalid.Field)
		assert.NotContains(t, err.Error(), value, "diagnostics must not repeat unsafe metadata")
	}
}

func writeADRPublicationYAML[T any](t *testing.T, path string, value T) []byte {
	t.Helper()
	data, err := yaml.Marshal(value)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return data
}

func TestADRPublicationPreservesValidMetadata(t *testing.T) {
	for _, optional := range []bool{false, true} {
		t.Run(map[bool]string{false: "all-fields", true: "optional-empty"}[optional], func(t *testing.T) {
			s, publisher, git := adrPublicationFixture(t)
			a := adr.ADR{ID: "DES-001", Title: "decision", Status: adr.StatusSettled,
				CreatedAt: "2026-11-04T10:00:00Z", UpdatedAt: "2026-11-04T10:00:00Z",
				Author: "agent-2", Decision: "use the repository validator",
				MissionID: "m-2026-11-04-001", BeadID: "punt-kit-abc.1.2"}
			if optional {
				a.Author, a.MissionID, a.BeadID = "", "", ""
			}
			const name = ".punt-labs/ethos/adrs/DES-001.yaml"
			writeADRPublicationYAML(t, filepath.Join(publisher.Policy.RecordRoot(), "adrs", a.ID+".yaml"), a)
			snapshot, err := s.SnapshotSelection(t.Context(), []string{"m-2026-11-04-001"}, []string{a.ID})
			require.NoError(t, err)
			receipt, err := publisher.Publish(t.Context(), snapshot, "HEAD")
			require.NoError(t, err)
			assert.True(t, receipt.IndexInstalled)
			data, err := os.ReadFile(filepath.Join(publisher.Repository.Root, filepath.FromSlash(name)))
			require.NoError(t, err)
			assert.Equal(t, string(data), git("show", ":"+name))
			var published adr.ADR
			require.NoError(t, yaml.Unmarshal(data, &published))
			assert.Equal(t, a, published, "valid semantic metadata must remain unchanged")
		})
	}
}

func adrPublicationFixture(t *testing.T) (*mission.Store, provenance.Publisher, func(...string) string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	store, checkout := t.TempDir(), t.TempDir()
	env := gitenv.Parsed()
	git := func(args ...string) string {
		t.Helper()
		data, err := gitenv.OutputEnv(checkout, "git", env, args...)
		require.NoError(t, err, "git %v", args)
		return string(data)
	}
	git("init", "-b", "main")
	git("config", "core.hooksPath", filepath.Join(checkout, "no-hooks"))
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.invalid")
	require.NoError(t, os.WriteFile(filepath.Join(checkout, "unrelated.txt"), []byte("base\n"), 0o600))
	git("add", "unrelated.txt")
	git("commit", "-m", "fixture base")
	require.NoError(t, os.WriteFile(filepath.Join(checkout, "unrelated.txt"), []byte("staged\n"), 0o600))
	git("add", "unrelated.txt")
	p, err := layout.NewRepository(store, checkout, layout.RecordBlame)
	require.NoError(t, err)
	s := mission.NewRepositoryStore(p, t.TempDir())
	c := mission.Contract{MissionID: "m-2026-11-04-001", Status: mission.StatusOpen,
		CreatedAt: "2026-11-04T10:00:00Z", UpdatedAt: "2026-11-04T10:00:00Z",
		Leader: "claude", Worker: "bwk", Evaluator: mission.Evaluator{Handle: "rsc", PinnedAt: "2026-11-04T10:00:00Z"},
		WriteSet: []string{"source.go"}, SuccessCriteria: []string{"checked"}, Budget: mission.Budget{Rounds: 1}, CurrentRound: 1}
	require.NoError(t, s.Create(&c))
	return s, provenance.Publisher{Policy: p, Repository: provenance.Repository{Root: checkout, Env: env}}, git
}
