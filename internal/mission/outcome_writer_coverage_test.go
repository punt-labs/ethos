package mission

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Exercise the moved checkpoints against actual production sources. The
// compatibility wrappers must keep delegating, and outcome-aware forward
// writers must retain their final-payload guards and redaction boundaries.
func TestOutcomeWriterCheckpointsDetectRemovedCalls(t *testing.T) {
	for _, tc := range []struct {
		file, target, callee string
	}{
		{"internal/mission/repository.go", "internal/mission.Store.writeBytes", "writeBytesWithOutcome"},
		{"internal/mission/repository.go", "internal/mission.Store.writeBytesWithOutcome", "guardWrite"},
		{"internal/mission/store.go", "internal/mission.writeContractBytes", "writeContractBytesWithOutcome"},
		{"internal/mission/store.go", "internal/mission.Store.writeContractNoteWithOutcome", "writeContractNoteAt"},
		{"internal/mission/store.go", "internal/mission.Store.writeContractNoteAt", "RedactedYAMLNotify"},
		{"internal/mission/store.go", "internal/mission.Store.writeContractNoteAt", "writeBytesWithOutcome"},
		{"internal/mission/store.go", "internal/mission.Store.writeResultsLockedNoteWithOutcome", "guardWrite"},
		{"internal/mission/store.go", "internal/mission.Store.withMissionLock", "withMissionLockWithOutcome"},
		{"internal/mission/store.go", "internal/mission.Store.withMissionLockWithOutcome", "withAuthorityLock"},
		{"internal/mission/store.go", "internal/mission.Store.withAuthorityLock", "withStateFileLock"},
		{"internal/mission/delegation.go", "internal/mission.restoreAtomicFile", "installAtomicFile"},
		{"internal/mission/delegation.go", "internal/mission.writeAtomicFile", "writeAtomicFileWithOutcome"},
		{"internal/mission/delegation.go", "internal/mission.writeAtomicFileWithOutcome", "writeAtomicFileWithWarning"},
		{"internal/mission/delegation.go", "internal/mission.writeAtomicFileWithWarning", "guardWrite"},
		{"internal/mission/delegation.go", "internal/mission.writeAtomicFileWithWarning", "installAtomicFile"},
		{"internal/mission/store.go", "internal/mission.Store.AppendReflection", "writeReflectionsLockedNote"},
		{"internal/mission/store.go", "internal/mission.Store.DisclaimDelegation", "DisclaimDelegationRecord"},
	} {
		t.Run(tc.target+"/"+tc.callee, func(t *testing.T) {
			sources := loadSources(t, filepath.Join("..", ".."))
			funcs := scanFuncs(t, sources)
			require.Empty(t, checkWriters(funcs, writerRegistry, outOfScopePackages, chokePoints))
			require.True(t, funcs[tc.target].callees[tc.callee], "mutation must remove a real production call")
			original := sources[tc.file]
			mutant := strings.ReplaceAll(original, tc.callee+"(", "removed"+tc.callee+"(")
			require.NotEqual(t, original, mutant)
			sources[tc.file] = mutant
			bad := checkWriters(scanFuncs(t, sources), writerRegistry, outOfScopePackages, chokePoints)
			require.Contains(t, strings.Join(bad, "\n"), tc.target+" is registered as redacting but does not call "+tc.callee)
		})
	}
}
