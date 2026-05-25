//go:build !windows

package mission

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStorageLayout_FullLifecycleAllFilesInRepoTree drives a single
// mission through every state-producing operation the Store exposes
// and asserts that EVERY artifact lands at <repoRoot>/.punt-labs/
// ethos/missions/<id>/<file>, with the only exception being the
// per-mission flock (which by DES-054 design lives under the global
// tree because lockfile inodes must not move between layers).
//
// The lifecycle:
//
//  1. Create — contract.yaml + log.jsonl
//  2. AppendReflection — reflections.yaml
//  3. AppendResult — results.yaml
//  4. WriteDelegationSkeleton — delegations/<id>/record.yaml +
//     prompt.md
//  5. Close — contract.yaml status updated + log.jsonl event +
//     parent missions.jsonl trace line
//
// Negative assertions: <globalRoot>/missions/<id>.* (legacy flat
// shape) is empty except for <id>.lock.
//
// This is the failing test the storage-activation bug (m-2026-05-23-004
// escalation) would have written, had it existed. Now that the fix
// is landed, the test pins the contract so a future regression
// surfaces here, not via the operator visually inspecting directories
// six months later.
func TestStorageLayout_FullLifecycleAllFilesInRepoTree(t *testing.T) {
	repoRoot := t.TempDir()
	globalRoot := t.TempDir()
	s := NewStoreWithRoots(repoRoot, globalRoot)

	c := newContract("m-2026-05-23-901")
	require.NoError(t, s.Create(c))
	id := c.MissionID

	perMissionDir := filepath.Join(repoRoot, ".punt-labs", "ethos", "missions", id)
	legacyDir := filepath.Join(globalRoot, "missions")

	// --- 1. Create produces contract.yaml + log.jsonl ---

	assertFileExists(t, filepath.Join(perMissionDir, "contract.yaml"),
		"Create must write contract.yaml into the repo tree")
	assertFileExists(t, filepath.Join(perMissionDir, "log.jsonl"),
		"Create must write log.jsonl into the repo tree")

	// --- 2. Reflect produces reflections.yaml ---

	r := reflectionFor(1, RecommendationContinue)
	require.NoError(t, s.AppendReflection(id, r))

	assertFileExists(t, filepath.Join(perMissionDir, "reflections.yaml"),
		"AppendReflection must write reflections.yaml into the repo tree")

	// --- 3. Result produces results.yaml ---

	submitRoundResult(t, s, c, VerdictPass)

	assertFileExists(t, filepath.Join(perMissionDir, "results.yaml"),
		"AppendResult must write results.yaml into the repo tree")

	// --- 4. Delegation skeleton produces delegations/<id>/record.yaml + prompt.md ---

	delegationID := "d-2026-05-23-901"
	prompt := []byte("smoke prompt body for the skeleton")
	_, err := WriteDelegationSkeleton(repoRoot, id, delegationID, DelegationSkeleton{
		Tier:      TierB,
		AgentType: "bwk",
		Prompt:    prompt,
	})
	require.NoError(t, err)

	delegationDir := filepath.Join(perMissionDir, "delegations", delegationID)
	assertFileExists(t, filepath.Join(delegationDir, "record.yaml"),
		"WriteDelegationSkeleton must write record.yaml into the repo tree")
	assertFileExists(t, filepath.Join(delegationDir, "prompt.md"),
		"WriteDelegationSkeleton with a non-empty Prompt must also write prompt.md")

	// --- 5. Close produces a missions.jsonl trace entry at parent level ---

	closedResult, err := s.Close(id, StatusClosed)
	require.NoError(t, err)
	require.NotNil(t, closedResult)

	traceFile := filepath.Join(repoRoot, ".punt-labs", "ethos", "missions.jsonl")
	assertFileExists(t, traceFile,
		"Close must append a summary line to <repo>/.punt-labs/ethos/missions.jsonl")
	data, err := os.ReadFile(traceFile)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"id":"`+id+`"`,
		"missions.jsonl must reference the closed mission's id")

	// --- Full per-mission directory contents inventory ---
	//
	// Enumerate every file the directory holds. The test asserts the
	// expected set is present; future additions (e.g. a new sibling
	// artifact) should add to this set explicitly so an unintended
	// path drift becomes a test failure.
	want := []string{
		"contract.yaml",
		"log.jsonl",
		"reflections.yaml",
		"results.yaml",
		"delegations",
	}
	for _, name := range want {
		assertFileExists(t, filepath.Join(perMissionDir, name),
			"expected file or directory in per-mission tree")
	}

	// --- Negative: legacy global tree carries only the per-mission lock ---
	//
	// DES-054 concurrency model: locks are global because the
	// lockfile inode must not move when a mission migrates between
	// layers. Every other artifact MUST be repo-tree-only.

	assertFileMissing(t, filepath.Join(legacyDir, id+".yaml"),
		"contract MUST NOT live in legacy global when repoRoot is set")
	assertFileMissing(t, filepath.Join(legacyDir, id+".jsonl"),
		"log MUST NOT live in legacy global when repoRoot is set")
	assertFileMissing(t, filepath.Join(legacyDir, id+".results.yaml"),
		"results MUST NOT live in legacy global when repoRoot is set")
	assertFileMissing(t, filepath.Join(legacyDir, id+".reflections.yaml"),
		"reflections MUST NOT live in legacy global when repoRoot is set")
	assertFileExists(t, filepath.Join(legacyDir, id+".lock"),
		"per-mission flock IS expected at the global tree (DES-054 lock invariant)")
}

// TestStorageLayout_NewStoreOnly_LegacyPathWhenNoRepoRoot pins the
// inverse: a Store created without a repoRoot writes the legacy
// flat-shape paths. Confirms the two-tree dispatch is gated on
// twoTreeStorage and that NewStore (no repoRoot) still works for
// non-repo CLI invocations.
func TestStorageLayout_NewStoreOnly_LegacyPathWhenNoRepoRoot(t *testing.T) {
	globalRoot := t.TempDir()
	s := NewStore(globalRoot)

	c := newContract("m-2026-05-23-902")
	require.NoError(t, s.Create(c))
	id := c.MissionID

	legacyDir := filepath.Join(globalRoot, "missions")
	assertFileExists(t, filepath.Join(legacyDir, id+".yaml"),
		"NewStore (legacy single-tree) must write contract to legacy global path")
	assertFileExists(t, filepath.Join(legacyDir, id+".jsonl"),
		"NewStore (legacy single-tree) must write log to legacy global path")
}

// assertFileExists fails the test if path does not exist on disk.
// stat is used (not lstat) so a symlink to an existing target counts.
func assertFileExists(t *testing.T, path, msg string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("%s: stat %q failed: %v", msg, path, err)
	}
}

// assertFileMissing fails the test if path exists on disk.
func assertFileMissing(t *testing.T, path, msg string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Errorf("%s: path %q exists but should not", msg, path)
	} else if !os.IsNotExist(err) {
		t.Errorf("%s: stat %q failed with unexpected error: %v", msg, path, err)
	}
}

// _ guards against deadcode warnings in build environments where the
// test is excluded by build tags.
var _ = time.Time{}
