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
	"internal/mission.writeContractFile":                       "guardWrite",
	"internal/mission.ReplaceRecordFile":                       "guardWrite",
	"internal/mission.writeAtomicFileWithWarning":              "guardWrite",
	"internal/mission.writeSyncedFile":                         "guardWrite",
	"internal/mission.WriteTrackedFiles":                       "writeTrackedFiles",
	"internal/mission.Store.WriteExportFiles":                  "writeTrackedFiles",
	"internal/mission.writeTrackedFiles":                       "guardWrite",
	"internal/mission.Store.writeBytesWithOutcome":             "guardWrite",
	"internal/mission.Store.persistIntent":                     "guardWrite",
	"internal/mission.Store.persistCorrectionIntent":           "guardWrite",
	"internal/mission.Store.ApplyReflection":                   "guardWrite",
	"internal/mission.Store.finishReflection":                  "guardWrite",
	"internal/mission.Store.applySharedCorrection":             "guardWrite",
	"internal/mission.WriteDelegationSkeletonWithPolicy":       "guardWrite",
	"internal/mission.Store.writeResultsLockedNoteWithOutcome": "guardWrite",
	"internal/mission.Store.writeReflectionsLockedNote":        "guardWrite",
	"internal/mission.Store.appendEventLocked":                 "guardWrite",
	"internal/mission.Store.appendRepositoryEvent":             "guardWrite",
	"internal/mission.Store.deliverOperation":                  "guardWrite",
	"internal/mission.Store.appendTraceLine":                   "guardWrite",
	"internal/mission.Store.prepareTraceSummary":               "guardWrite",
	"internal/hook.sealPolicyStream":                           "guard",
	"internal/hook.MigrateAuditWithPolicy":                     "guard",
	"internal/audit.QuarantineLocal":                           "guard",
	"internal/audit.Quarantine":                                "guardLine",
	"internal/hook.appendPolicyAudit":                          "guardAuditLineFull",
	"internal/hook.appendLiveAuditReport":                      "guardAuditLineFull",
	"internal/hook.writeAuditEntryReport":                      "guardAuditLineFull",
	"internal/hook.appendRawAuditLines":                        "guardAuditLineNote",
	"internal/hook.emitAuditSentinel":                          "guardAuditLineNote",
	"internal/hook.emitLegacySentinel":                         "guardAuditLineNote",
	"cmd/ethos.runMissionExport":                               "WriteExportFiles",
	"internal/adr.Store.write":                                 "prepare",
	"internal/adr.Store.prepare":                               "LeakRule",
	"internal/provenance.Publisher.Publish":                    "RedactPublicationSnapshot",
	"internal/provenance.Publisher.publishLocked":              "GuardPublicationSummary",
	"internal/provenance.validate":                             "LeakRule",
	"internal/mission.RedactPublicationSnapshot":               "guardWrite",
	"internal/mission.publicationRedact":                       "guardWrite",
	"internal/mission.GuardPublicationSummary":                 "guardValue",
}

// guardAllowed are the registered sinks that do not call a guard themselves,
// each with why every byte they receive is already guarded. A sink is listed
// only with an exact source-chain rationale. The raw damaged-byte exception is
// limited to QuarantineLocal's canonical-local quarantine destination.
var guardAllowed = map[string]string{
	"internal/audit.AppendMonotonic":                 "appends the line its callback returns; every caller's callback returns guardAuditLine output (hook) or is guardWrite-checked (mission live log)",
	"internal/audit.WriteChunkAtomic":                "the production caller audit.Quarantine checks recovered lines with guardLine before sealing; the old hook.sealDirLocked now exists only in a test fixture",
	"internal/audit.writeMarkerAtomic":               "quarantine marker whose reason went through redactReason, which replaces a reason that still leaks",
	"internal/mission.localReplace":                  "forward callers writeBytesWithOutcome, persistIntent, persistCorrectionIntent, ApplyReflection, finishReflection, applySharedCorrection and WriteDelegationSkeletonWithPolicy call guardWrite; writeBytesWithOutcome(false) via restoreBytes and restoreAtomicFile restore saved destination bytes only",
	"internal/mission.writeContractBytesWithOutcome": "writeContractFile guards bytes before writeContractBytes delegates; writeBytesWithOutcome guards forward bytes before installation, or restores saved destination bytes through restoreBytes with guarding disabled",
	"internal/mission.installAtomicFile":             "writeAtomicFileWithWarning guards forward bytes; restoreAtomicFile restores saved delegation/reflection destination bytes without re-redaction; the writer registry discovers every caller of installAtomicFile and restoreAtomicFile",
	"internal/mission.installPromptArtifact":         "retainDelegationPrompt receives prompt bytes already guarded by WriteDelegationSkeletonWithPolicy, and guardWrite-checks marshaled consent; publishPromptLocked guardWrite-checks and compares exact bytes to captured consent before installation; the writer registry discovers every payload caller of this sink and retainDelegationPrompt",
	"internal/mission.writeTrackedTemp":              "staging callback passed by WriteTrackedFiles to writeTrackedFiles, which guardWrite-checks every new payload; restoreTrackedWith uses the same callback only for saved destination bytes",
	"internal/audit.WriteLocalAtomic":                "sealPolicyStream requires canonical equality with its namespace guard before local archival or publication; QuarantineLocal guards recovered lines, receives a quarantineReason-checked marker, and stores raw damaged bytes ONLY at p.LocalRoot()/quarantine/<stream-hash>/<digest>.corrupt (retirement records contain a constant, digest, size and timestamp)",
	"internal/audit.replaceAuditFile":                "ReplaceLocalFile forwards only sealPolicyStream canonical-guard-equal immutable payloads, MigrateAuditWithPolicy final sessionLineGuard output (refusing unsafe existing identities), or QuarantineLocal generated diagnostics; QuarantineLocal also calls directly for the explicitly selected published marker with a quarantineReason-checked reason and numeric availability bounds; raw originals go only through WriteLocalAtomic into S quarantine",
	"internal/adr.Store.publish":                     "Store.write is its sole caller and marshals Store.prepare output; prepare applies shared redact.Record/redact.Prose and rejects every field with redact.LeakRule for repository ADRs; standalone ADRs remain global",
}

// guardScopes are the packages whose writers reach the tracked tree.
var guardScopes = []string{"internal/mission.", "internal/hook.", "internal/audit.", "internal/adr.", "internal/provenance.", "cmd/ethos."}

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
