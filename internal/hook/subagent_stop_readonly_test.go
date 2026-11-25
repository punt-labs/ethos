//go:build linux || darwin

package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/stretchr/testify/require"
)

// TestHandoffReadOnlyStop proves a subagent stop skips handoff checks only
// when the agent made no Write/Edit call and the checkout is unchanged since
// SubagentStart; any Bash-style change still runs the `files: all` gate.
func TestHandoffReadOnlyStop(t *testing.T) {
	for _, tc := range []struct {
		name    string
		act     func(t *testing.T, root string)
		audit   []auditEntry
		noStart bool
		global  bool
		before  bool
		hide    string
		runs    bool
	}{
		{name: "read-only", runs: false},
		{name: "read-only beside another agent's write", audit: []auditEntry{auditWrite("other", "dirty.go")}, runs: false},
		{name: "write with unchanged checkout", audit: []auditEntry{auditWrite("sub", "dirty.go")}, runs: true},
		{name: "edit with unchanged checkout", audit: []auditEntry{{Tool: "Edit", AgentID: "sub", ToolInput: map[string]any{"file_path": "<repo>/dirty.go"}, PathFormat: auditPathFormat}}, runs: true},
		{name: "bash edits a clean file", act: func(t *testing.T, root string) { writeFile(t, filepath.Join(root, "clean.go"), "package changed\n") }, runs: true},
		{name: "bash edits a dirty file", act: func(t *testing.T, root string) { writeFile(t, filepath.Join(root, "dirty.go"), "package b\n") }, runs: true},
		{name: "bash edits a renamed file", act: func(t *testing.T, root string) { writeFile(t, filepath.Join(root, "renamed file.go"), "package c\n") }, runs: true},
		{name: "bash edits an untracked file", act: func(t *testing.T, root string) { writeFile(t, filepath.Join(root, "untracked.go"), "package d\n") }, runs: true},
		{name: "bash creates a file", act: func(t *testing.T, root string) { writeFile(t, filepath.Join(root, "new.go"), "") }, runs: true},
		{name: "read-only with an assume-unchanged file", hide: "--assume-unchanged", runs: false},
		{name: "bash edits an assume-unchanged file", hide: "--assume-unchanged", act: func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "clean.go"), "package hidden\n")
		}, runs: true},
		{name: "bash edits a skip-worktree file", hide: "--skip-worktree", act: func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "clean.go"), "package hidden\n")
		}, runs: true},
		{name: "bash removes a file", act: func(t *testing.T, root string) { require.NoError(t, os.Remove(filepath.Join(root, "clean.go"))) }, runs: true},
		{name: "bash stages a file", act: func(t *testing.T, root string) { stageGit(t, root, "add", "dirty.go") }, runs: true},
		{name: "bash commits", act: func(t *testing.T, root string) {
			stageGit(t, root, "-c", "user.name=T", "-c", "user.email=t@t", "commit", "-qam", "bash")
		}, runs: true},
		{name: "bash swaps a rename's source", act: func(t *testing.T, root string) {
			stageGit(t, root, "mv", "twin.txt", "twin1.txt")
			stageGit(t, root, "mv", "twin2.txt", "twin.txt")
		}, runs: true},
		{name: "read-only with global policy", global: true, runs: false},
		{name: "global policy changes", global: true, act: func(t *testing.T, _ string) {
			checkPolicy(t, os.Getenv("HOME"), "project", handoffProbe+"  - name: added\n    stage: handoff\n    files: all\n    run: [probe]\n")
		}, runs: true},
		{name: "unattributed write after start", audit: []auditEntry{auditWrite("", "dirty.go")}, runs: true},
		{name: "unattributed write before start", audit: []auditEntry{auditWrite("", "dirty.go")}, before: true, runs: false},
		{name: "bash edits a dirty nested repo", act: func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "nested/other.txt"), "changed\n")
		}, runs: true},
		{name: "no start fingerprint", noStart: true, runs: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, bin := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			policyRoot := root
			if tc.global {
				policyRoot = os.Getenv("HOME")
			}
			checkPolicy(t, policyRoot, "project", handoffProbe)
			writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
			stageGit(t, root, "add", ".punt-labs")
			stageGit(t, root, "-c", "user.name=T", "-c", "user.email=t@t", "commit", "-qm", "policy")
			marker := filepath.Join(t.TempDir(), "ran")
			t.Setenv("HANDOFF_MARKER", marker)
			postEditTool(t, bin, "probe", "printf ran > \"$HANDOFF_MARKER\"; exit 1\n")
			writeFile(t, filepath.Join(root, "dirty.go"), "package a\n")
			writeFile(t, filepath.Join(root, "clean.go"), "package clean\n")
			writeFile(t, filepath.Join(root, "twin1.txt"), "same\n")
			writeFile(t, filepath.Join(root, "twin2.txt"), "same\n")
			stageGit(t, root, "add", "dirty.go", "clean.go", "twin1.txt", "twin2.txt")
			stageGit(t, root, "-c", "user.name=T", "-c", "user.email=t@t", "commit", "-qm", "dirty")
			writeFile(t, filepath.Join(root, "dirty.go"), "package a // dirty\n")
			stageGit(t, root, "mv", "README", "renamed file.go")
			stageGit(t, root, "mv", "twin1.txt", "twin.txt")
			writeFile(t, filepath.Join(root, "untracked.go"), "package u\n")
			nested := filepath.Join(root, "nested")
			writeFile(t, filepath.Join(nested, "other.txt"), "base\n")
			stageGit(t, nested, "init", "-q")
			stageGit(t, nested, "add", "other.txt")
			stageGit(t, nested, "-c", "user.name=T", "-c", "user.email=t@t", "commit", "-qm", "nested")
			writeFile(t, filepath.Join(nested, "dirty.txt"), "dirty\n")
			if tc.hide != "" {
				stageGit(t, root, "update-index", tc.hide, "clean.go")
			}

			ss := session.NewStore(t.TempDir())
			require.NoError(t, ss.Create("ro", session.Participant{AgentID: "human"}, session.Participant{AgentID: "main"}, "", ""))
			start, err := json.Marshal(map[string]any{"session_id": "ro", "agent_id": "sub", "agent_type": "worker"})
			require.NoError(t, err)
			if tc.noStart {
				require.NoError(t, ss.Join("ro", session.Participant{AgentID: "sub", AgentType: "worker"}))
			} else {
				_, err = captureStdout(t, func() error {
					return HandleSubagentStartWithDeps(bytes.NewReader(start), SubagentStartDeps{Identities: identity.NewStore(t.TempDir()), Sessions: ss})
				})
				require.NoError(t, err)
			}
			roster, err := ss.Load("ro")
			require.NoError(t, err)
			require.Equal(t, tc.noStart, roster.FindParticipant("sub").CheckoutState == "")
			if tc.act != nil {
				tc.act(t, root)
			}
			for i := range tc.audit {
				ts := time.Now().UTC().Add(time.Second)
				if tc.before {
					ts = ts.Add(-time.Hour)
				}
				tc.audit[i].Ts, tc.audit[i].Session = ts.Format(time.RFC3339Nano), "ro"
			}
			if len(tc.audit) > 0 {
				writeRepoTreeSession(t, root, "2026-11-22", "ro", tc.audit)
			}

			var out, stderr bytes.Buffer
			require.NoError(t, HandleSubagentStopWithIO(context.Background(), bytes.NewReader(start), ss, &out, &stderr))
			roster, err = ss.Load("ro")
			require.NoError(t, err)
			require.Nil(t, roster.FindParticipant("sub"))
			require.NoFileExists(t, marker)
			if tc.runs {
				require.Contains(t, out.String(), `"decision":"block"`)
				require.Contains(t, out.String(), "handoff checks deferred")
				return
			}
			require.Empty(t, out.String())
			require.Empty(t, stderr.String())
		})
	}
}

const handoffProbe = "  - name: full-gate\n    stage: handoff\n    files: all\n    run: [probe]\n"

func auditWrite(agent, file string) auditEntry {
	return auditEntry{Tool: "Write", AgentID: agent, ToolInput: map[string]any{"file_path": "<repo>/" + file}, PathFormat: auditPathFormat}
}
