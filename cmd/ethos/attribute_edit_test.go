//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/attribute"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The scratch file editContent hands the editor lives in a private directory
// whose name carries the temp marker, so it classifies as temporary wherever
// TMPDIR points, and both are gone afterwards.
func TestEditContentScratchIsMarkedAndRemoved(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	seen := filepath.Join(tmp, "seen")
	editor := filepath.Join(t.TempDir(), "editor.sh")
	script := "#!/bin/sh\nprintf %s \"$1\" > " + seen + "\nprintf 'body\\n' >> \"$1\"\n"
	require.NoError(t, os.WriteFile(editor, []byte(script), 0o755))
	t.Setenv("VISUAL", editor)

	got, err := editContent(attribute.Talents, "go")
	require.NoError(t, err)
	assert.Contains(t, got, "body")

	path, err := os.ReadFile(seen)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(path), tmp+string(filepath.Separator)), "scratch is under TMPDIR: %s", path)
	assert.Contains(t, filepath.Dir(string(path)), layout.TmpMarker, "scratch directory carries the temp marker")
	assert.NoDirExists(t, filepath.Dir(string(path)))
	assert.NoFileExists(t, string(path))
}

// A failing editor still leaves nothing behind.
func TestEditContentRemovesScratchOnEditorFailure(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("VISUAL", "false")

	_, err := editContent(attribute.Talents, "go")
	require.Error(t, err)

	entries, readErr := os.ReadDir(tmp)
	require.NoError(t, readErr)
	assert.Empty(t, entries)
}
