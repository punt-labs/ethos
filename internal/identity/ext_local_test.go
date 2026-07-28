package identity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// writeExtFile writes raw YAML to <handle>.ext/<name>, creating the dir.
// Tests use it to lay down a .local companion no writer API produced, so
// the read path is exercised against the file layout itself.
func writeExtFile(t *testing.T, s *Store, handle, name, body string) {
	t.Helper()
	dir := s.ExtDir(handle)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
}

func TestReadNamespaceMerge(t *testing.T) {
	// base and local are nil when the file is absent, so an empty file
	// (exists, no keys) stays distinguishable from no file at all.
	body := func(s string) *string { return &s }
	tests := []struct {
		name    string
		base    *string
		local   *string
		want    map[string]string
		wantErr bool
	}{
		{
			name: "base only is byte-identical to today",
			base: body("provider: elevenlabs\nvoice_id: abc\n"),
			want: map[string]string{"provider": "elevenlabs", "voice_id": "abc"},
		},
		{
			name:  "local overlays base per key",
			base:  body("provider: elevenlabs\nvoice_id: abc\n"),
			local: body("voice_id: secret\n"),
			want:  map[string]string{"provider": "elevenlabs", "voice_id": "secret"},
		},
		{
			name:  "local adds keys absent from base",
			base:  body("provider: elevenlabs\n"),
			local: body("api_token: t0ken\n"),
			want:  map[string]string{"provider": "elevenlabs", "api_token": "t0ken"},
		},
		{
			name:  "local alone is a namespace",
			local: body("api_token: t0ken\n"),
			want:  map[string]string{"api_token": "t0ken"},
		},
		{
			name: "empty base file exists with no keys",
			base: body(""),
			want: map[string]string{},
		},
		{
			name:    "neither file is not found",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := setupExtTest(t)
			if tt.base != nil {
				writeExtFile(t, s, "test", "vox.yaml", *tt.base)
			}
			if tt.local != nil {
				writeExtFile(t, s, "test", "vox"+ExtLocalSuffix, *tt.local)
			}

			m, err := s.readNamespace("test", "vox")
			if tt.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, os.ErrNotExist), "want an os.ErrNotExist, got %v", err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, m)
		})
	}
}

func TestExtGetMergesLocal(t *testing.T) {
	s := setupExtTest(t)
	require.NoError(t, s.ExtSet("test", "vox", "provider", "elevenlabs"))
	writeExtFile(t, s, "test", "vox"+ExtLocalSuffix, "provider: local-only\n")

	m, err := s.ExtGet("test", "vox", "provider")
	require.NoError(t, err)
	assert.Equal(t, "local-only", m["provider"])
}

func TestLoadMergesLocalIntoOneFlatMap(t *testing.T) {
	s := setupExtTest(t)
	require.NoError(t, s.ExtSet("test", "quarry", "memory_collection", "test-mem"))
	writeExtFile(t, s, "test", "quarry"+ExtLocalSuffix, "api_token: t0ken\n")

	id, err := s.Load("test")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"memory_collection": "test-mem",
		"api_token":         "t0ken",
	}, id.Ext["quarry"])
}

// A base write must read, mutate, and rewrite the BASE file only. If it
// went through the merged view, a .local secret would be folded into the
// git-tracked half — the exact leak the layout boundary exists to prevent.
func TestExtSetBaseNeverFoldsLocal(t *testing.T) {
	s := setupExtTest(t)
	require.NoError(t, s.ExtSet("test", "quarry", "memory_collection", "test-mem"))
	writeExtFile(t, s, "test", "quarry"+ExtLocalSuffix, "api_token: t0ken\n")

	require.NoError(t, s.ExtSet("test", "quarry", "session_context", "on"))

	base := readYAMLMap(t, s.extPath("test", "quarry"))
	assert.Equal(t, map[string]string{
		"memory_collection": "test-mem",
		"session_context":   "on",
	}, base)
	assert.NotContains(t, base, "api_token")
}

func TestExtSetLocalTargetsCompanion(t *testing.T) {
	s := setupExtTest(t)
	require.NoError(t, s.ExtSet("test", "quarry", "memory_collection", "test-mem"))
	require.NoError(t, s.ExtSet("test", "quarry", "api_token", "t0ken", Local(true)))

	assert.Equal(t, map[string]string{"memory_collection": "test-mem"},
		readYAMLMap(t, s.extPath("test", "quarry")))
	assert.Equal(t, map[string]string{"api_token": "t0ken"},
		readYAMLMap(t, s.extLocalPath("test", "quarry")))

	m, err := s.ExtGet("test", "quarry", "")
	require.NoError(t, err)
	assert.Len(t, m, 2)
}

func TestExtDelTargetsOneFile(t *testing.T) {
	s := setupExtTest(t)
	require.NoError(t, s.ExtSet("test", "vox", "provider", "base"))
	require.NoError(t, s.ExtSet("test", "vox", "provider", "local", Local(true)))

	// Deleting the base key leaves the .local value standing.
	require.NoError(t, s.ExtDel("test", "vox", "provider"))
	m, err := s.ExtGet("test", "vox", "provider")
	require.NoError(t, err)
	assert.Equal(t, "local", m["provider"])

	// Deleting the .local key empties the namespace.
	require.NoError(t, s.ExtDel("test", "vox", "provider", Local(true)))
	_, err = s.ExtGet("test", "vox", "")
	require.Error(t, err)
}

func TestExtDelNamespaceLocalOnly(t *testing.T) {
	s := setupExtTest(t)
	require.NoError(t, s.ExtSet("test", "vox", "provider", "base"))
	require.NoError(t, s.ExtSet("test", "vox", "api_token", "t0ken", Local(true)))

	require.NoError(t, s.ExtDel("test", "vox", "", Local(true)))

	m, err := s.ExtGet("test", "vox", "")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"provider": "base"}, m)
}

// ExtList strips ".local.yaml" as a unit before the ".yaml" case, so a
// .local-only namespace lists under its real name and never as the
// phantom "<ns>.local".
func TestExtListNoPhantomLocalNamespace(t *testing.T) {
	s := setupExtTest(t)
	require.NoError(t, s.ExtSet("test", "vox", "provider", "elevenlabs"))
	require.NoError(t, s.ExtSet("test", "vox", "api_token", "t0ken", Local(true)))
	writeExtFile(t, s, "test", "beadle"+ExtLocalSuffix, "password: hunter2\n")

	ns, err := s.ExtList("test")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"beadle", "vox"}, ns)
	assert.NotContains(t, ns, "vox.local")
	assert.NotContains(t, ns, "beadle.local")
}

// readYAMLMap decodes a namespace file straight from disk, bypassing every
// store read path — the only way to assert what was actually persisted.
func readYAMLMap(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var m map[string]string
	require.NoError(t, yaml.Unmarshal(data, &m))
	return m
}
