package adr

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "adrs"))
}

func TestStore_CreateAndLoad(t *testing.T) {
	s := testStore(t)
	a := &ADR{
		Title:    "Use YAML for ADRs",
		Context:  "Need structured ADRs",
		Decision: "Store as YAML",
		Author:   "bwk",
	}
	require.NoError(t, s.Create(a))
	assert.Equal(t, "DES-001", a.ID)
	assert.Equal(t, StatusProposed, a.Status)
	assert.NotEmpty(t, a.CreatedAt)
	assert.NotEmpty(t, a.UpdatedAt)

	loaded, err := s.Load("DES-001")
	require.NoError(t, err)
	assert.Equal(t, a.ID, loaded.ID)
	assert.Equal(t, a.Title, loaded.Title)
	assert.Equal(t, a.Decision, loaded.Decision)
	assert.Equal(t, a.Author, loaded.Author)
}

func TestStore_AutoIncrement(t *testing.T) {
	s := testStore(t)
	for i := 0; i < 3; i++ {
		a := &ADR{
			Title:    "Decision " + string(rune('A'+i)),
			Context:  "context",
			Decision: "decided",
		}
		require.NoError(t, s.Create(a))
	}
	ids, err := s.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"DES-001", "DES-002", "DES-003"}, ids)
}

func TestStore_List_Empty(t *testing.T) {
	s := testStore(t)
	ids, err := s.List()
	require.NoError(t, err)
	assert.Nil(t, ids)
}

func TestStore_Update(t *testing.T) {
	s := testStore(t)
	a := &ADR{
		Title:    "Original",
		Context:  "context",
		Decision: "decided",
	}
	require.NoError(t, s.Create(a))

	err := s.Update("DES-001", func(a *ADR) {
		a.Status = StatusSettled
	})
	require.NoError(t, err)

	loaded, err := s.Load("DES-001")
	require.NoError(t, err)
	assert.Equal(t, StatusSettled, loaded.Status)
}

func TestStore_LoadNotFound(t *testing.T) {
	s := testStore(t)
	_, err := s.Load("DES-999")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_LoadEmptyID(t *testing.T) {
	s := testStore(t)
	_, err := s.Load("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

func TestValidate_BadStatus(t *testing.T) {
	a := &ADR{
		ID:        "DES-001",
		Title:     "test",
		Status:    "invalid",
		Decision:  "decided",
		CreatedAt: "2026-04-09T00:00:00Z",
		UpdatedAt: "2026-04-09T00:00:00Z",
	}
	err := a.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid status")
}

func TestValidate_MissingTitle(t *testing.T) {
	a := &ADR{
		ID:        "DES-001",
		Title:     "",
		Status:    StatusProposed,
		Decision:  "decided",
		CreatedAt: "2026-04-09T00:00:00Z",
		UpdatedAt: "2026-04-09T00:00:00Z",
	}
	err := a.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "title is required")
}

func TestValidate_MissingDecision(t *testing.T) {
	a := &ADR{
		ID:        "DES-001",
		Title:     "test",
		Status:    StatusProposed,
		Decision:  "",
		CreatedAt: "2026-04-09T00:00:00Z",
		UpdatedAt: "2026-04-09T00:00:00Z",
	}
	err := a.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decision is required")
}

func TestValidate_BadID(t *testing.T) {
	a := &ADR{
		ID:        "BAD-001",
		Title:     "test",
		Status:    StatusProposed,
		Decision:  "decided",
		CreatedAt: "2026-04-09T00:00:00Z",
		UpdatedAt: "2026-04-09T00:00:00Z",
	}
	err := a.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must match DES-NNN")
}

func TestStore_StrictDecode_UnknownFields(t *testing.T) {
	s := testStore(t)
	a := &ADR{
		Title:    "test",
		Context:  "context",
		Decision: "decided",
	}
	require.NoError(t, s.Create(a))

	// Append an unknown field to the file.
	path := s.path("DES-001")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	data = append(data, []byte("unknown_field: bad\n")...)
	require.NoError(t, os.WriteFile(path, data, 0o600))

	_, err = s.Load("DES-001")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown_field")
}

func TestStore_CreateWithAlternatives(t *testing.T) {
	s := testStore(t)
	a := &ADR{
		Title:        "Use YAML",
		Context:      "context",
		Decision:     "YAML",
		Alternatives: []string{"JSON", "TOML"},
		MissionID:    "m-2026-04-09-001",
		BeadID:       "ethos-k28",
		Author:       "bwk",
	}
	require.NoError(t, s.Create(a))

	loaded, err := s.Load("DES-001")
	require.NoError(t, err)
	assert.Equal(t, []string{"JSON", "TOML"}, loaded.Alternatives)
	assert.Equal(t, "m-2026-04-09-001", loaded.MissionID)
	assert.Equal(t, "ethos-k28", loaded.BeadID)
}

func TestStore_UpdateNotFound(t *testing.T) {
	s := testStore(t)
	err := s.Update("DES-999", func(a *ADR) {
		a.Status = StatusSettled
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_CreateMissingDecision(t *testing.T) {
	s := testStore(t)
	a := &ADR{
		Title:   "test",
		Context: "context",
	}
	err := s.Create(a)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decision is required")
}
