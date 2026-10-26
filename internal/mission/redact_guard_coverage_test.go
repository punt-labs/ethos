package mission

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// guardedSinks are the functions that put bytes into the tracked mission and
// audit tree, each with the guard it must call on the final bytes before
// they reach the file. The redaction passes upstream are best effort; this
// is the check that holds whatever they missed.
var guardedSinks = map[string]string{
	"internal/mission.writeContractFile":                "guardWrite",
	"internal/mission.writeAtomicFile":                  "guardWrite",
	"internal/mission.writeSyncedFile":                  "guardWrite",
	"internal/mission.WriteTrackedFiles":                "guardWrite",
	"internal/mission.Store.writeResultsLockedNote":     "guardWrite",
	"internal/mission.Store.writeReflectionsLockedNote": "guardWrite",
	"internal/mission.Store.appendEventLocked":          "guardWrite",
	"internal/mission.Store.appendLiveEventLocked":      "guardWrite",
	"internal/mission.Store.appendTraceLine":            "guardWrite",
	"internal/mission.Store.prepareTraceSummary":        "guardWrite",
	"internal/hook.sealDirLocked":                       "guardTail",
	"internal/audit.Quarantine":                         "guardLine",
	"internal/hook.appendLiveAuditReport":               "guardAuditLineFull",
	"internal/hook.writeAuditEntryReport":               "guardAuditLineFull",
	"internal/hook.appendRawAuditLines":                 "guardAuditLineNote",
	"internal/hook.emitAuditSentinel":                   "guardAuditLineNote",
	"internal/hook.emitLegacySentinel":                  "guardAuditLineNote",
	"cmd/ethos.runMissionExport":                        "WriteTrackedFiles",
}

// guardAllowed are the registered sinks that do not call a guard themselves,
// each with why every byte they receive is already guarded. A sink is listed
// only if all its callers are in guardedSinks.
var guardAllowed = map[string]string{
	"internal/audit.AppendMonotonic":   "appends the line its callback returns; every caller's callback returns guardAuditLine output (hook) or is guardWrite-checked (mission live log)",
	"internal/audit.WriteChunkAtomic":  "its two callers, hook.sealDirLocked (guardTail) and audit.Quarantine (guardLine), pass every line through the audit guard first",
	"internal/audit.writeMarkerAtomic": "quarantine marker whose reason went through redactReason, which replaces a reason that still leaks",
}

// guardScopes are the packages whose writers reach the tracked tree.
var guardScopes = []string{"internal/mission.", "internal/hook.", "internal/audit.", "cmd/ethos."}

// TestTrackedSinksCallTheGuard is the source scan for the sink guard: every
// function in a tracked-tree package that creates or renames a file is either
// registered exempt (it writes nothing a record carries) or a guarded sink
// that calls its guard, and every guarded sink still exists and calls it. A
// writer added later with neither fails here.
func TestTrackedSinksCallTheGuard(t *testing.T) {
	funcs := scanFuncs(t, loadSources(t, filepath.Join("..", "..")))

	var bad []string
	for key, g := range guardedSinks {
		fi, ok := funcs[key]
		if !ok {
			bad = append(bad, key+": registered as a guarded sink but not found")
			continue
		}
		if !fi.callees[g] {
			bad = append(bad, key+": does not call "+g)
		}
	}
	for key, fi := range funcs {
		if !fi.prims || !inGuardScope(key) {
			continue
		}
		if _, ok := guardedSinks[key]; ok {
			continue
		}
		if e, ok := writerRegistry[key]; ok && e.class == classExempt {
			continue
		}
		if _, ok := guardAllowed[key]; ok {
			continue
		}
		if e, ok := writerRegistry[key]; ok && e.class == classSink {
			bad = append(bad, key+": a sink in the writer registry with no entry in guardedSinks")
			continue
		}
		bad = append(bad, key+": creates a file and is neither exempt nor a guarded sink")
	}
	sort.Strings(bad)
	assert.Empty(t, bad)
}

func inGuardScope(key string) bool {
	for _, p := range guardScopes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// TestTrackedSinksCallTheGuard_Mutant proves the scan fails on an unguarded
// writer.
func TestTrackedSinksCallTheGuard_Mutant(t *testing.T) {
	funcs := scanFuncs(t, map[string]string{"internal/mission/x.go": "package mission\nimport \"os\"\nfunc stray() { os.WriteFile(\"a\", nil, 0) }\n"})
	fi, ok := funcs["internal/mission.stray"]
	require.True(t, ok)
	assert.True(t, fi.prims)
	assert.False(t, fi.callees["guardWrite"])
}
