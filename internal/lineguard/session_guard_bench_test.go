package lineguard

import (
	"fmt"
	"testing"
)

// benchLines is 200 audit lines in the shapes the corpus carries: a Bash
// command, a Read of a path, an Edit with prose, and a tool with no input.
func benchLines() [][]byte {
	var out [][]byte
	for i := 0; i < 200; i++ {
		var l string
		switch i % 4 {
		case 0:
			l = fmt.Sprintf(`{"ts":"2026-11-09T04:17:%02d.525854Z","session":"s","tool":"Bash","tool_input":{"command":"git status && go test ./... -run T%d"},"tool_input_hash":"h","tool_input_preview":"{}"}`, i%60, i)
		case 1:
			l = fmt.Sprintf(`{"ts":"2026-11-09T04:18:%02d.525854Z","session":"s","tool":"Read","tool_input":{"file_path":"/work/repo/internal/pkg%d/file.go"},"path_format":"abs"}`, i%60, i)
		case 2:
			l = fmt.Sprintf(`{"ts":"2026-11-09T04:19:%02d.525854Z","session":"s","tool":"Edit","tool_input":{"file_path":"~/repo/a.go","old_string":"func f%d() {}","new_string":"func f%d() int { return 1 }"}}`, i%60, i, i)
		default:
			l = fmt.Sprintf(`{"ts":"2026-11-09T04:20:%02d.525854Z","session":"s","tool":"TodoWrite"}`, i%60)
		}
		out = append(out, []byte(l))
	}
	return out
}

func BenchmarkSessionLineGuard(b *testing.B) {
	b.Setenv("HOME", "/home/bench")
	lines := benchLines()
	guard := SessionLineGuardFor("/work/repo")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, l := range lines {
			guard(l)
		}
	}
}
