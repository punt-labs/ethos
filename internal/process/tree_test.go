//go:build linux || darwin

package process

import (
	"os"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

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
	claudePIDOnce = syncOnceZero()
	defer func() { claudePIDOnce = syncOnceZero() }()

	result := walkToClaudeAncestor(os.Getpid())
	// Must return a valid PID string — either a claude ancestor
	// (when running inside Claude Code) or os.Getppid() (fallback).
	pid, err := strconv.Atoi(result)
	assert.NoError(t, err)
	assert.Greater(t, pid, 0)
}

// syncOnceZero returns a zero-value sync.Once for test isolation.
func syncOnceZero() syncOnce { return syncOnce{} }

type syncOnce = sync.Once
