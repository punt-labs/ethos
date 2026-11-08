package adr

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestRepositoryStore_RejectsMalformedMetadataAtEverySink(t *testing.T) {
	t.Setenv("HOME", canonicalTempDir(t))
	fields := []struct {
		name string
		set  func(*ADR, string)
	}{
		{"author", func(a *ADR, value string) { a.Author = value }},
		{"mission_id", func(a *ADR, value string) { a.MissionID = value }},
		{"bead_id", func(a *ADR, value string) { a.BeadID = value }},
	}
	values := []struct{ name, value string }{
		{"assignment", "password=synthetic-only"},
		{"address", "person@example.org"},
		{"handle-shaped-secret", "sk-" + strings.Repeat("a", 32)},
		{"gitlab-secret", "glpat-" + strings.Repeat("a", 24)},
		{"path", "/home/private/person"},
		{"invalid-utf8", string([]byte{0xff})},
		{"whitespace", " bwk "},
	}
	for _, field := range fields {
		for _, value := range values {
			for _, sink := range []string{"create", "update", "copy-on-write", "migrate-dry-run", "migrate-apply"} {
				t.Run(field.name+"/"+value.name+"/"+sink, func(t *testing.T) {
					s, legacy, p := repositoryStore(t, layout.RecordBlame)
					a := sampleADR()
					var prior []byte
					var source []byte
					var err error
					switch sink {
					case "create":
						field.set(a, value.value)
						before := clone(a)
						err = s.Create(a)
						assert.Equal(t, before, a)
					case "update":
						require.NoError(t, s.Create(a))
						prior, err = os.ReadFile(s.path(a.ID))
						require.NoError(t, err)
						err = s.Update(a.ID, func(a *ADR) { field.set(a, value.value) })
					default:
						// Global compatibility deliberately accepts historical values.
						// Preflight must refuse all selected copies, even if the valid
						// source precedes an invalid email-bearing legacy author.
						good := sampleADR()
						require.NoError(t, legacy.Create(good))
						field.set(a, value.value)
						require.NoError(t, legacy.Create(a))
						source, err = os.ReadFile(legacy.path(a.ID))
						require.NoError(t, err)
						if sink == "copy-on-write" {
							err = s.Update(a.ID, func(a *ADR) { a.Context = "must not be copied" })
						} else {
							_, err = s.Migrate([]string{good.ID, a.ID}, sink == "migrate-apply")
						}
						assert.NoFileExists(t, s.path(good.ID))
					}
					var invalid *ValidationError
					require.ErrorAs(t, err, &invalid)
					assert.Equal(t, field.name, invalid.Field)
					assert.NotContains(t, err.Error(), value.value)
					if sink == "update" {
						data, err := os.ReadFile(s.path(a.ID))
						require.NoError(t, err)
						assert.Equal(t, prior, data)
					} else {
						assert.NoFileExists(t, s.path("DES-001"))
						assert.NoFileExists(t, s.path("DES-002"))
						assert.NoDirExists(t, p.RecordRoot(), "refusal must not create a record tree")
					}
					if source != nil {
						data, err := os.ReadFile(legacy.path(a.ID))
						require.NoError(t, err)
						assert.Equal(t, source, data)
					}
				})
			}
		}
	}
}

func TestRepositoryMetadata_IdentifierGrammars(t *testing.T) {
	cases := []struct {
		name string
		a    ADR
		bad  string
	}{
		{"optional", ADR{}, ""},
		{"valid", ADR{Author: "bwk", MissionID: "m-2026-11-04-001", BeadID: "ethos-07m.5"}, ""},
		{"hyphenated-project", ADR{Author: "agent-2", BeadID: "punt-kit-abc.1.2"}, ""},
		{"mission-short", ADR{MissionID: "m-1"}, "mission_id"},
		{"mission-suffix", ADR{MissionID: "m-2026-11-04-001-extra"}, "mission_id"},
		{"mission-extra-digit", ADR{MissionID: "m-2026-11-04-0001"}, "mission_id"},
		{"bead-no-project", ADR{BeadID: "abc"}, "bead_id"},
		{"bead-empty-id", ADR{BeadID: "ethos-"}, "bead_id"},
		{"bead-double-dash", ADR{BeadID: "ethos--abc"}, "bead_id"},
		{"bead-uppercase", ADR{BeadID: "ethos-ABC"}, "bead_id"},
		{"bead-nonnumeric-child", ADR{BeadID: "ethos-abc.def"}, "bead_id"},
		{"bead-empty-child", ADR{BeadID: "ethos-abc."}, "bead_id"},
		{"bead-limit", ADR{BeadID: "ethos-" + strings.Repeat("a", 122)}, ""},
		{"bead-too-long", ADR{BeadID: "ethos-" + strings.Repeat("a", 123)}, "bead_id"},
		{"author-assignment", ADR{Author: "password=synthetic-only"}, "author"},
		{"author-address", ADR{Author: "person@example.invalid"}, "author"},
		{"mission-assignment", ADR{MissionID: "password=synthetic-only"}, "mission_id"},
		{"bead-assignment", ADR{BeadID: "password=synthetic-only"}, "bead_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRepositoryMetadata(&tc.a)
			if tc.bad == "" {
				require.NoError(t, err)
			} else {
				var invalid *ValidationError
				require.ErrorAs(t, err, &invalid)
				assert.Equal(t, tc.bad, invalid.Field)
			}
		})
	}
}

func TestValidateRepositoryMetadataNil(t *testing.T) {
	var invalid *ValidationError
	require.ErrorAs(t, ValidateRepositoryMetadata(nil), &invalid)
	assert.Equal(t, "adr", invalid.Field)
}

func TestRepositoryStore_PreservesValidMetadataAtEverySink(t *testing.T) {
	t.Setenv("HOME", canonicalTempDir(t))
	for _, sink := range []string{"create", "update", "copy-on-write", "migrate"} {
		t.Run(sink, func(t *testing.T) {
			s, legacy, _ := repositoryStore(t, layout.RecordFull)
			a := sampleADR()
			a.Author, a.MissionID, a.BeadID = "agent-2", "m-2026-11-04-001", "ethos-07m.5"
			switch sink {
			case "create":
				require.NoError(t, s.Create(a))
			case "update":
				require.NoError(t, s.Create(a))
				require.NoError(t, s.Update(a.ID, func(a *ADR) { a.Status = StatusSettled }))
			case "copy-on-write":
				require.NoError(t, legacy.Create(a))
				require.NoError(t, s.Update(a.ID, func(a *ADR) { a.Status = StatusSettled }))
			case "migrate":
				require.NoError(t, legacy.Create(a))
				_, err := s.Migrate([]string{a.ID}, true)
				require.NoError(t, err)
			}
			stored, err := s.loadOwn(a.ID)
			require.NoError(t, err)
			assert.Equal(t, a.Author, stored.Author)
			assert.Equal(t, a.MissionID, stored.MissionID)
			assert.Equal(t, a.BeadID, stored.BeadID)
		})
	}
}
