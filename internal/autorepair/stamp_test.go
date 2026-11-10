package autorepair

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// physicalTemp is a temp directory with symlinks resolved: the layout package
// refuses a symlinked ancestor, and macOS temp dirs sit under /var.
func physicalTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return d
}

func TestStampRoundTrip(t *testing.T) {
	dir := physicalTemp(t)
	path := filepath.Join(dir, "sub", StampName)
	want := Stamp{
		Version:    LayoutVersion,
		Ethos:      "v5.0.0",
		RepairedAt: "2026-11-09T10:00:00Z",
		Items:      7,
		Residual:   []Residual{{Step: "round-decisions", ID: "m-1", Reason: "incomplete history"}},
	}
	require.NoError(t, WriteStamp(path, want, func(d string) error { return os.MkdirAll(d, 0o700) }))

	got, err := ReadStamp(path)
	require.NoError(t, err)
	assert.Equal(t, want, got)

	// No staging file is left behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestReadStamp(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name    string
		content string // "" with missing=true means no file
		missing bool
		current bool
		wantErr bool
		absent  bool
	}{
		{name: "missing", missing: true, wantErr: true, absent: true},
		{name: "current", content: "version: 5\nethos: v5.0.0\nrepaired_at: 2026-11-09T10:00:00Z\n", current: true},
		{name: "older", content: "version: 4\n"},
		{name: "higher is current", content: "version: 6\n", current: true},
		{name: "corrupt", content: "version: [", wantErr: true},
		{name: "empty", content: "", wantErr: false},
		{name: "zero version", content: "version: 0\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "_")+".yaml")
			if !tt.missing {
				require.NoError(t, os.WriteFile(path, []byte(tt.content), 0o600))
			}
			s, err := ReadStamp(path)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.absent, errors.Is(err, fs.ErrNotExist))
			assert.Equal(t, tt.current, s.Current())
		})
	}
}

func TestWriteStampInRepositoryLocalZone(t *testing.T) {
	store := physicalTemp(t)
	p, err := layout.NewRepository(store, store, layout.RecordBlame)
	require.NoError(t, err)
	path := RepositoryStampPath(p)
	assert.Equal(t, filepath.Join(store, ".punt-labs", "local", "ethos", "layout.yaml"), path)

	ensure := func(d string) error { return layout.EnsureLocalDir(p, d) }
	require.NoError(t, WriteStamp(path, Stamp{Version: LayoutVersion}, ensure))
	s, err := ReadStamp(path)
	require.NoError(t, err)
	assert.True(t, s.Current())

	// The stamp classifies as never tracked.
	rel, err := filepath.Rel(store, path)
	require.NoError(t, err)
	c, _, ok := layout.Classify(rel)
	assert.True(t, ok)
	assert.Equal(t, layout.Never, c)
}

func TestGlobalStampPath(t *testing.T) {
	assert.Equal(t, filepath.Join("/g", "layout.yaml"), GlobalStampPath("/g"))
}
