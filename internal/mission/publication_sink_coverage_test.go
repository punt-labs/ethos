package mission

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Exercise the real registry and checker, not a parallel list of sinks. These
// helpers install payloads through function-valued primitives or Git stdin, so
// a new caller must be discovered even without a direct os write primitive.
func TestPublicationHelperCallersRequireGuardedPayload(t *testing.T) {
	for _, call := range []string{
		"writeContractBytes(path, PAYLOAD)",
		"writeContractBytesWithOutcome(path, PAYLOAD, out)",
		"restoreAtomicFile(dir, pattern, path, PAYLOAD)",
		"installAtomicFile(dir, pattern, path, PAYLOAD)",
		"audit.WriteLocalAtomic(p, path, PAYLOAD)",
		"publisher.installFile(path, PAYLOAD)",
		"repository.stageBytes(ctx, path, PAYLOAD)",
	} {
		for _, mode := range []string{"raw", "guarded", "decoy"} {
			t.Run(call+"/"+mode, func(t *testing.T) {
				sources := loadSources(t, filepath.Join("..", ".."))
				payload, prefix := "raw", ""
				switch mode {
				case "guarded":
					payload, prefix = "guarded", "guarded := RedactRecord(raw); "
				case "decoy":
					prefix = "RedactRecord(nil); "
				}
				sources["internal/hook/coverage_mutant.go"] = "package hook\nfunc stray(raw []byte) error { " + prefix + "return " + strings.ReplaceAll(call, "PAYLOAD", payload) + " }\n"
				bad := checkWriters(scanFuncs(t, sources), writerRegistry, outOfScopePackages, chokePoints)
				var mutantFindings []string
				for _, finding := range bad {
					if strings.Contains(finding, "internal/hook.stray") {
						mutantFindings = append(mutantFindings, finding)
					}
				}
				if mode == "guarded" {
					require.Empty(t, mutantFindings, "guarded data must remain admissible")
				} else {
					require.NotEmpty(t, mutantFindings, "a raw helper wrapper must fail the actual registry checker")
				}
			})
		}
	}
}
