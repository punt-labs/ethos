package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupIdentityWithAttributes creates an identity and its attribute .md files
// in a temp directory. Returns the identity store and session store.
func setupIdentityWithAttributes(t *testing.T, id *identity.Identity, personality, writingStyle string) (*identity.Store, *session.Store) {
	t.Helper()
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	if personality != "" {
		ps := attribute.NewStore(dir, attribute.Personalities)
		require.NoError(t, ps.Save(&attribute.Attribute{
			Slug:    id.Personality,
			Content: personality,
		}))
	}
	if writingStyle != "" {
		ws := attribute.NewStore(dir, attribute.WritingStyles)
		require.NoError(t, ws.Save(&attribute.Attribute{
			Slug:    id.WritingStyle,
			Content: writingStyle,
		}))
	}

	// Create talent .md files so Save's ValidateRefs passes.
	if len(id.Talents) > 0 {
		ts := attribute.NewStore(dir, attribute.Talents)
		for _, slug := range id.Talents {
			require.NoError(t, ts.Save(&attribute.Attribute{
				Slug:    slug,
				Content: "# " + slug,
			}))
		}
	}

	require.NoError(t, s.Save(id))
	return s, ss
}

// isolateGitConfig prevents git config from interfering with resolve.
func isolateGitConfig(t *testing.T, user string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(tmp, "empty.gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("USER", user)
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "empty.gitconfig"), []byte(""), 0o644))
}

// captureSessionStartOutput runs HandleSessionStart and captures stdout.
func captureSessionStartOutput(t *testing.T, input string, s *identity.Store, ss *session.Store) string {
	t.Helper()

	// Capture stdout.
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	in := bytes.NewReader([]byte(input))
	require.NoError(t, HandleSessionStart(in, s, ss))

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)
	return buf.String()
}

func TestHandleSessionStart_PersonaBlock(t *testing.T) {
	id := &identity.Identity{
		Name:         "Alice",
		Handle:       "alice",
		Kind:         "human",
		Personality:  "calm-engineer",
		WritingStyle: "concise-quant",
		Talents:      []string{"engineering"},
	}
	personality := "# Calm Engineer\n\nA calm and methodical engineer.\n\n- Stay focused\n- Data over adjectives"
	writingStyle := "# Concise Quantified\n\nShort and data-driven.\n\n- Under 30 words\n- Lead with numbers"

	s, ss := setupIdentityWithAttributes(t, id, personality, writingStyle)
	isolateGitConfig(t, "alice")

	out := captureSessionStartOutput(t, `{"session_id": "s1"}`, s, ss)

	var result SessionStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	ctx := result.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "You are Alice (alice)")
	assert.Contains(t, ctx, "A calm and methodical engineer.")
	assert.Contains(t, ctx, "## Personality")
	assert.Contains(t, ctx, "Stay focused")
	assert.Contains(t, ctx, "## Writing Style")
	assert.Contains(t, ctx, "Under 30 words")
	assert.Contains(t, ctx, "## Talents")
	assert.Contains(t, ctx, "engineering")
}

func TestHandleSessionStart_NoPersonality_FallsBack(t *testing.T) {
	id := &identity.Identity{
		Name:   "Bob",
		Handle: "bob",
		Kind:   "human",
	}

	s, ss := setupIdentityWithAttributes(t, id, "", "")
	isolateGitConfig(t, "bob")

	out := captureSessionStartOutput(t, `{"session_id": "s2"}`, s, ss)

	var result SessionStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	ctx := result.HookSpecificOutput.AdditionalContext
	// Should fall back to one-line format.
	assert.Contains(t, ctx, "Active identity: Bob (bob)")
	assert.NotContains(t, ctx, "## Personality")
}

func TestHandleSessionStart_NoIdentity_NoOutput(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	isolateGitConfig(t, "nobody")

	out := captureSessionStartOutput(t, `{"session_id": "s3"}`, s, ss)

	// No identity resolved -- should produce no output.
	assert.Equal(t, "", out)
}

func TestHandleSessionStart_PersonalityOnly(t *testing.T) {
	id := &identity.Identity{
		Name:        "Carol",
		Handle:      "carol",
		Kind:        "agent",
		Personality: "methodical",
	}
	personality := "# Methodical\n\nQuiet and patient.\n\n- Think before acting\n- Simplicity first"

	s, ss := setupIdentityWithAttributes(t, id, personality, "")
	isolateGitConfig(t, "carol")

	out := captureSessionStartOutput(t, `{"session_id": "s4"}`, s, ss)

	var result SessionStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	ctx := result.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "You are Carol (carol)")
	assert.Contains(t, ctx, "## Personality")
	assert.Contains(t, ctx, "Think before acting")
	assert.NotContains(t, ctx, "## Writing Style")
}
