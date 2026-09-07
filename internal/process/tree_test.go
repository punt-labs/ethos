//go:build linux || darwin

package process

import (
	"bytes"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureStderr runs fn with os.Stderr redirected to a pipe and returns
// what fn wrote there.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	defer func() { os.Stderr = old }()
	fn()
	w.Close()
	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)
	return buf.String()
}

func TestIsClaudeComm(t *testing.T) {
	tests := []struct {
		comm string
		want bool
	}{
		{"claude", true},
		{"/usr/local/bin/claude", true},
		{"claude-code", false},
		{"not-claude", false},
		{"bash", false},
		{"", false},
		{"2.1.86", false}, // version string alone is not claude (normalization is in readProc)
	}
	for _, tt := range tests {
		t.Run(tt.comm, func(t *testing.T) {
			assert.Equal(t, tt.want, isClaudeComm(tt.comm))
		})
	}
}

func TestNormalizeClaudeComm(t *testing.T) {
	tests := []struct {
		name    string
		comm    string
		exePath string
		want    string
	}{
		{"already claude", "claude", "/usr/local/bin/claude", "claude"},
		{"version-named binary", "2.1.86", "/Users/x/.local/share/claude/versions/2.1.86", "claude"},
		{"version-named other version", "2.1.81", "/home/user/.local/share/claude/versions/2.1.81", "claude"},
		{"non-claude binary", "node", "/usr/local/bin/node", "node"},
		{"non-claude with versions in path", "myapp", "/some/versions/myapp", "myapp"},
		{"empty exe path", "2.1.86", "", "2.1.86"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeClaudeComm(tt.comm, tt.exePath))
		})
	}
}

func TestReadProc_CurrentProcess(t *testing.T) {
	// We can always read our own process info.
	ppid, comm, err := readProc(os.Getpid())
	if err != nil {
		t.Fatalf("readProc(self): %v", err)
	}
	assert.Equal(t, os.Getppid(), ppid)
	assert.NotEmpty(t, comm)
}

func TestReadProc_Init(t *testing.T) {
	// PID 1 should always be readable.
	ppid, comm, err := readProc(1)
	if err != nil {
		t.Skipf("cannot read PID 1: %v", err)
	}
	assert.Equal(t, 0, ppid)
	assert.NotEmpty(t, comm)
}

func TestReadProc_Nonexistent(t *testing.T) {
	_, _, err := readProc(999999999)
	assert.Error(t, err)
}

func TestWalkToClaudeAncestor_ReturnsValidPID(t *testing.T) {
	result := walkToClaudeAncestor(os.Getpid())
	// Must return a valid PID string — either a claude ancestor
	// (when running inside Claude Code) or os.Getppid() (fallback).
	pid, err := strconv.Atoi(result)
	assert.NoError(t, err)
	assert.Greater(t, pid, 0)
}

// --- DES-074 regression tests ---
//
// These strip the real CLAUDE_PID from the test process (this suite itself
// usually runs inside a Claude Code session, so the ambient value is real
// and live — exactly the ambient leak DES-074 warns test authors about)
// before asserting on env-absent behavior, and set a controlled value
// before asserting on env-present behavior.

func TestFindClaudePID_PrefersLiveEnvPID(t *testing.T) {
	// os.Getppid() is a genuinely live ancestor of this test process,
	// standing in for the owning claude process CLAUDE_PID would name in
	// production — corroboration cares only about live ancestry, not the
	// command name (biff DES-058's is_live_ancestor does the same).
	parent := strconv.Itoa(os.Getppid())
	t.Setenv("CLAUDE_PID", parent)

	assert.Equal(t, parent, FindClaudePID())
}

func TestFindClaudePID_FallsBackWhenEnvNotLiveAncestor(t *testing.T) {
	// A PID that cannot be our ancestor (max walk depth is 10 short hops;
	// this value is astronomically unlikely to exist at all). Trusting an
	// env-sourced PID without corroboration is exactly the gap DES-074
	// closes — a dead ancestor's PID could otherwise be recycled by an
	// unrelated but legitimate claude session and resolve to a real, live,
	// WRONG process.
	t.Setenv("CLAUDE_PID", "999999999")

	got := FindClaudePID()
	want := walkToClaudeAncestor(os.Getpid())
	assert.Equal(t, want, got, "corroboration failure must fall back to the walk")
	assert.NotEqual(t, "999999999", got)
}

func TestFindClaudePID_IgnoresBlankOrMalformedEnv(t *testing.T) {
	for _, v := range []string{"", "   ", "not-a-pid", "-7", "0"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("CLAUDE_PID", v)
			got := FindClaudePID()
			want := walkToClaudeAncestor(os.Getpid())
			assert.Equal(t, want, got)
		})
	}
}

func TestFindClaudePID_NoProcessLifetimeCaching(t *testing.T) {
	// Pins the sync.Once removal (tree.go, formerly line 31): a long-lived
	// process (ethos serve) must not keep answering with the FIRST PID it
	// ever resolved. Two calls, each corroborating a DIFFERENT genuinely
	// live ancestor (our parent, then our grandparent — both real,
	// verified PIDs in our own process tree, not the walk's fallback),
	// must each be re-derived, not served from a process-lifetime cache.
	//
	// Round 2 finding: the previous version of this test forced CLAUDE_PID
	// to our parent, then to a bogus PID expecting the walk fallback to
	// differ. In an environment with no real "claude" ancestor (CI, a
	// detached process), the walk's own fallback is os.Getppid() — the
	// SAME value the first call already used — so the two calls
	// coincided by accident of environment, not because caching was
	// absent, and a re-introduced sync.Once around FindClaudePID would
	// have passed this test undetected. Using two REAL, always-distinct
	// ancestors closes that gap in every environment.
	parentPID := os.Getppid()
	grandparentPID, err := ParentPID(parentPID)
	require.NoError(t, err, "need a real grandparent to run this test")
	require.NotEqual(t, 0, grandparentPID)
	require.NotEqual(t, parentPID, grandparentPID)

	t.Setenv("CLAUDE_PID", strconv.Itoa(parentPID))
	first := FindClaudePID()
	require.Equal(t, strconv.Itoa(parentPID), first)

	t.Setenv("CLAUDE_PID", strconv.Itoa(grandparentPID))
	second := FindClaudePID()
	assert.Equal(t, strconv.Itoa(grandparentPID), second,
		"a process-lifetime cache would still return the first PID")
	assert.NotEqual(t, first, second)
}

func TestIsLiveAncestor(t *testing.T) {
	assert.True(t, isLiveAncestor(os.Getppid()), "our real parent must corroborate")
	assert.False(t, isLiveAncestor(999999999), "a PID with no ancestry relation must not corroborate")
}

func TestUnderClaudeCode(t *testing.T) {
	t.Run("CLAUDE_PID present is sufficient", func(t *testing.T) {
		t.Setenv("CLAUDE_PID", "1")
		t.Setenv("CLAUDECODE", "")
		assert.True(t, UnderClaudeCode())
	})

	t.Run("CLAUDECODE present is sufficient", func(t *testing.T) {
		t.Setenv("CLAUDE_PID", "")
		t.Setenv("CLAUDECODE", "1")
		assert.True(t, UnderClaudeCode())
	})

	t.Run("neither env var, no claude ancestor", func(t *testing.T) {
		// This suite normally runs inside a real Claude Code process, so a
		// genuine "not under Claude Code at all" state can only be
		// asserted once both indicator env vars are stripped; whether a
		// claude ancestor is found depends on this suite's real process
		// tree, which we do not control, so this only exercises the
		// env-absent path deterministically.
		t.Setenv("CLAUDE_PID", "")
		t.Setenv("CLAUDECODE", "")
		_, foundAncestor := findClaudeAncestor(os.Getpid())
		assert.Equal(t, foundAncestor, UnderClaudeCode())
	})
}

// TestUnderClaudeCode_ForceHatchLogsToStderr pins mission 005 finding F:
// ForceNotUnderClaudeCodeEnv suppresses the loud-failure branch this
// whole decision exists to make loud, so honoring it must never be
// silent. Before this fix, setting the variable in a real environment
// produced ErrNotUnderClaudeCode -- and the git/OS fallback that follows
// it -- with zero logging anywhere.
func TestUnderClaudeCode_ForceHatchLogsToStderr(t *testing.T) {
	t.Setenv(ForceNotUnderClaudeCodeEnv, "1")

	var result bool
	stderr := captureStderr(t, func() { result = UnderClaudeCode() })

	assert.False(t, result, "the hatch is negative-only: it must still force false")
	assert.Contains(t, stderr, ForceNotUnderClaudeCodeEnv, "honoring the hatch must never be silent")
}
