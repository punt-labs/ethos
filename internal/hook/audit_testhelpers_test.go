package hook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/internal/audit"
)

// writeChunkFile creates a JSONL file (a sealed chunk or a frozen legacy
// file) holding one line per timestamp. Shared by the seal and read tests.
func writeChunkFile(t *testing.T, dir, name string, tss ...int64) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var body []byte
	for _, ts := range tss {
		line := `{"ts":"` + audit.FormatLineTS(ts) + `","session":"s","tool":"Bash"}` + "\n"
		body = append(body, line...)
	}
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
		t.Fatal(err)
	}
}
