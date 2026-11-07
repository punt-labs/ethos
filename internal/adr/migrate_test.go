package adr

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestMigrate_SelectedRedactedCopiesAndIdempotence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	s, legacy, _ := repositoryStore(t, layout.RecordBlame)
	a := sampleADR()
	a.Context = home + "/secret person@example.com password=synthetic"
	require.NoError(t, legacy.Create(a))
	require.NoError(t, legacy.Create(sampleADR())) // unrelated global record
	source, err := os.ReadFile(legacy.path(a.ID))
	require.NoError(t, err)
	results, err := s.Migrate([]string{a.ID}, false)
	require.NoError(t, err)
	assert.Equal(t, []Migration{{ID: a.ID, Action: "would-copy"}}, results)
	assert.NoFileExists(t, s.path(a.ID))
	results, err = s.Migrate([]string{a.ID}, true)
	require.NoError(t, err)
	assert.Equal(t, []Migration{{ID: a.ID, Action: "copied"}}, results)
	got, err := s.loadOwn(a.ID)
	require.NoError(t, err)
	assert.NotContains(t, got.Context, home)
	assert.NotContains(t, got.Context, "person@example.com")
	assert.NotContains(t, got.Context, "synthetic")
	assert.Equal(t, a.CreatedAt, got.CreatedAt)
	assert.Equal(t, a.UpdatedAt, got.UpdatedAt)
	assert.Equal(t, a.ID, got.ID)
	for _, apply := range []bool{false, true, true} {
		results, err = s.Migrate([]string{a.ID}, apply)
		require.NoError(t, err)
		assert.Equal(t, []Migration{{ID: a.ID, Action: "unchanged"}}, results)
	}
	unchanged, err := os.ReadFile(legacy.path(a.ID))
	require.NoError(t, err)
	assert.Equal(t, source, unchanged)
	assert.NoFileExists(t, s.path("DES-002"))
}

func TestMigrate_PreflightDivergenceAndMalformedInput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, legacy, _ := repositoryStore(t, layout.RecordFull)
	for i := 0; i < 2; i++ {
		require.NoError(t, legacy.Create(sampleADR()))
	}
	require.NoError(t, s.Update("DES-002", func(a *ADR) { a.Context = "diverged" }))
	for _, apply := range []bool{false, true} {
		_, err := s.Migrate([]string{"DES-001", "DES-002"}, apply)
		require.ErrorContains(t, err, "divergent")
		assert.NoFileExists(t, s.path("DES-001"), "preflight all selections before copying any")
	}
	for _, ids := range [][]string{nil, {"../DES-001"}, {"DES-999"}, {"DES-001", "DES-001"}} {
		_, err := s.Migrate(ids, true)
		require.Error(t, err)
	}
	require.NoError(t, os.WriteFile(legacy.path("DES-003"), []byte("title: ["), 0o600))
	_, err := s.Migrate([]string{"DES-003"}, true)
	require.Error(t, err)
	assert.NoFileExists(t, s.path("DES-003"))
	_, err = legacy.Migrate([]string{"DES-001"}, true)
	require.ErrorContains(t, err, "requires a repository")
}

func TestMigrate_InterruptedBatchRetry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, legacy, _ := repositoryStore(t, layout.RecordBlame)
	for i := 0; i < 2; i++ {
		require.NoError(t, legacy.Create(sampleADR()))
	}
	s.link = func(src, dest string) error {
		if strings.HasSuffix(dest, "DES-002.yaml") {
			return syscall.EIO
		}
		return os.Link(src, dest)
	}
	_, err := s.Migrate([]string{"DES-001", "DES-002"}, true)
	require.ErrorIs(t, err, syscall.EIO)
	assert.FileExists(t, s.path("DES-001"))
	assert.NoFileExists(t, s.path("DES-002"))
	s.link = nil
	results, err := s.Migrate([]string{"DES-001", "DES-002"}, true)
	require.NoError(t, err)
	assert.Equal(t, []Migration{{ID: "DES-001", Action: "unchanged"}, {ID: "DES-002", Action: "copied"}}, results)
	entries, err := os.ReadDir(s.dir)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.Equal(t, ".yaml", filepath.Ext(entry.Name()))
	}
}

func TestMigrate_RedactorFailureCannotPublish(t *testing.T) {
	s, legacy, _ := repositoryStore(t, layout.RecordBlame)
	a := sampleADR()
	require.NoError(t, legacy.Create(a))
	source, err := os.ReadFile(legacy.path(a.ID))
	require.NoError(t, err)
	t.Setenv("HOME", "/")
	for _, apply := range []bool{false, true} {
		_, err := s.Migrate([]string{a.ID}, apply)
		require.ErrorContains(t, err, "unusable")
		assert.NoFileExists(t, s.path(a.ID))
	}
	unchanged, err := os.ReadFile(legacy.path(a.ID))
	require.NoError(t, err)
	assert.Equal(t, source, unchanged)
}
