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
// a well-formed but nonexistent session. Every consumer — iam, session
// show, and whoami alike — fails visibly. Before DES-074, whoami was the
// one "soft" exception: it silently fell back to the git/OS identity here,
// exactly the wrong-answer-with-exit-0 shape DES-074 was written to close
// ("an ended session" is one of its three measured cases) — an explicit
// ETHOS_SESSION is "a session was expected" regardless of which command
// asked, so a session that does not check out is now loud everywhere, not
// just on the hard chain.
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

	// whoami: an explicitly-declared but bogus session is a wrong-answer
	// risk, not an absence (DES-074) — it fails the same way iam and
	// session show do, not a crash and not a silently substituted global
	// persona.
	_, stderr, code = runCLI(t, sh, "whoami")
	require.NotEqual(t, 0, code, "whoami must fail on a bogus explicit session")
	assert.Contains(t, stderr, "not found")
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
