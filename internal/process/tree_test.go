//go:build !windows

package process

import (
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePS_BasicOutput(t *testing.T) {
	output := `  100     1 launchd
  200   100 bash
  300   200 claude
  400   300 go
  500   400 ethos
`
	tree, err := parsePS(output)
	require.NoError(t, err)
	assert.Len(t, tree, 5)
	assert.Equal(t, proc{ppid: 200, comm: "claude"}, tree[300])
}

func TestParsePS_EmptyOutput(t *testing.T) {
	_, err := parsePS("")
	require.Error(t, err)
}

func TestParsePS_MalformedLines(t *testing.T) {
	output := `  abc   100 bash
  200   def claude
  300   200 go
`
	tree, err := parsePS(output)
	require.NoError(t, err)
	assert.Len(t, tree, 1)
	assert.Equal(t, proc{ppid: 200, comm: "go"}, tree[300])
}

func TestWalkToClaudeAncestor_FindsTopmost(t *testing.T) {
	tree := map[int]proc{
		1:   {ppid: 0, comm: "launchd"},
		100: {ppid: 1, comm: "claude"},
		200: {ppid: 100, comm: "claude"},
		300: {ppid: 200, comm: "go"},
		400: {ppid: 300, comm: "ethos"},
	}
	// Walk from ethos (400) → go (300) → claude (200) → claude (100) → launchd (1)
	// Topmost claude is 100.
	result := walkToClaudeAncestor(400, tree)
	assert.Equal(t, "100", result)
}

func TestWalkToClaudeAncestor_SingleClaude(t *testing.T) {
	tree := map[int]proc{
		1:   {ppid: 0, comm: "launchd"},
		100: {ppid: 1, comm: "bash"},
		200: {ppid: 100, comm: "claude"},
		300: {ppid: 200, comm: "ethos"},
	}
	result := walkToClaudeAncestor(300, tree)
	assert.Equal(t, "200", result)
}

func TestWalkToClaudeAncestor_NoClaude(t *testing.T) {
	tree := map[int]proc{
		1:   {ppid: 0, comm: "launchd"},
		100: {ppid: 1, comm: "bash"},
		200: {ppid: 100, comm: "node"},
		300: {ppid: 200, comm: "ethos"},
	}
	result := walkToClaudeAncestor(300, tree)
	assert.Equal(t, strconv.Itoa(os.Getppid()), result)
}

func TestWalkToClaudeAncestor_MaxDepth(t *testing.T) {
	// Build a chain deeper than maxWalkDepth.
	tree := make(map[int]proc)
	for i := 1; i <= 15; i++ {
		tree[i] = proc{ppid: i - 1, comm: "proc"}
	}
	tree[1] = proc{ppid: 0, comm: "claude"}
	// Start at PID 15 — should not reach PID 1 (claude) beyond 10 levels.
	result := walkToClaudeAncestor(15, tree)
	// PID 15 is 14 hops from PID 1, but we cap at 10, so we walk
	// 15→14→…→5 (10 iterations). PID 5's parent is 4, but we stop.
	assert.Equal(t, strconv.Itoa(os.Getppid()), result)
}

func TestWalkToClaudeAncestor_PathContainingClaude(t *testing.T) {
	tree := map[int]proc{
		1:   {ppid: 0, comm: "launchd"},
		100: {ppid: 1, comm: "/usr/local/bin/claude"},
		200: {ppid: 100, comm: "ethos"},
	}
	result := walkToClaudeAncestor(200, tree)
	assert.Equal(t, "100", result)
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
	}
	for _, tt := range tests {
		t.Run(tt.comm, func(t *testing.T) {
			assert.Equal(t, tt.want, isClaudeComm(tt.comm))
		})
	}
}
