//go:build linux || darwin

package main

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCLI_Session_NonexistentRoster pins behavior when ETHOS_SESSION names
// a well-formed but nonexistent session. The hard-chain consumers (iam,
// session show) fail visibly; whoami — the soft standalone path — falls
// back to the git/OS identity rather than crashing or resolving a wrong
// global persona.
func TestCLI_Session_NonexistentRoster(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := seededSessionEnv(t)
	// 32-hex shape, but no such roster exists.
	sh := withSession(se, "deadbeefdeadbeefdeadbeefdeadbeef", "")

	// session show resolves the env id and fails visibly.
	_, stderr, code := runCLI(t, sh, "session", "show")
	require.NotEqual(t, 0, code, "show of a nonexistent session must fail")
	assert.Contains(t, stderr, "not found")

	// iam takes the hard chain: the env id resolves, then the roster load
	// fails — an actionable error, and no roster is created.
	_, stderr, code = runCLI(t, sh, "iam", "claude")
	require.NotEqual(t, 0, code, "iam against a nonexistent session must fail")
	assert.Contains(t, stderr, "not found")
	assert.NoFileExists(t,
		filepath.Join(se.home, ".punt-labs", "ethos", "sessions", "deadbeefdeadbeefdeadbeefdeadbeef.yaml"),
		"a failed iam must not create the roster")

	// whoami is soft: a bogus session is treated as "no resolvable
	// session", falling to the USER identity — exit 0, not a crash, not a
	// wrong global persona.
	stdout, _, code := runCLI(t, sh, "whoami")
	require.Equal(t, 0, code, "whoami must not fail on a bogus session")
	assert.Contains(t, stdout, "tester", "whoami falls back to the git/OS identity")
}

// TestCLI_Session_MalformedID_NoTraversal proves the roster path is built
// with filepath.Base (store.go rosterPath/lockPath), so a session id
// shaped as a path traversal cannot read or write outside the sessions
// dir. Every on-disk artifact a malformed id can produce lands under
// sessions/, keyed by the id's basename.
func TestCLI_Session_MalformedID_NoTraversal(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := seededSessionEnv(t)
	sessionsDir := filepath.Join(se.home, ".punt-labs", "ethos", "sessions")

	cases := []struct {
		name    string
		id      string // ETHOS_SESSION value; base carries the token
		outside string // an absolute path that must never be created
	}{
		{"dotdot", "../PWN_%s", ""},
		{"absolute", "/tmp/PWN_%s", "/tmp/PWN_%s.yaml"},
		{"embedded-slash", "a/b/PWN_%s", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := fmt.Sprintf("PWN_%d", time.Now().UnixNano())
			id := strings.Replace(tc.id, "PWN_%s", token, 1)
			sh := withSession(se, id, "")

			// Exercise both the read path (session show → Load) and the
			// write path (iam → withLock + Load). Both must fail safely.
			_, _, showCode := runCLI(t, sh, "session", "show")
			assert.NotEqual(t, 0, showCode, "show of %q must fail, not read outside", id)
			_, _, iamCode := runCLI(t, sh, "iam", "claude")
			assert.NotEqual(t, 0, iamCode, "iam on %q must fail, not write outside", id)

			// No artifact carrying the token may exist outside sessionsDir.
			if tc.outside != "" {
				assert.NoFileExists(t, strings.Replace(tc.outside, "PWN_%s", token, 1),
					"traversal target must not be created")
			}
			var stray []string
			_ = filepath.WalkDir(se.home, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				if strings.Contains(d.Name(), token) && filepath.Dir(path) != sessionsDir {
					stray = append(stray, path)
				}
				return nil
			})
			assert.Empty(t, stray, "all id-derived files must be contained under %s", sessionsDir)
		})
	}
}

// TestCLI_Session_EmptyEnv_TreatedAsUnset pins that an empty ETHOS_SESSION
// is treated as unset (SessionID checks the value is non-empty), so it
// falls through to the walk rather than being a hard error. In a scratch
// HOME the walk resolves nothing, so session show reports no session.
func TestCLI_Session_EmptyEnv_TreatedAsUnset(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := seededSessionEnv(t)
	sh := withSession(se, "", "") // ETHOS_SESSION="" explicitly.

	stdout, _, code := runCLI(t, sh, "session", "show")
	require.Equal(t, 0, code, "empty ETHOS_SESSION must not be a hard error")
	assert.Contains(t, stdout, "No active session.",
		"empty is treated as unset and falls through to the (empty) walk")
}
