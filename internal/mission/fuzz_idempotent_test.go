package mission

import (
	"testing"

	"github.com/punt-labs/ethos/v4/internal/redact"
)

// FuzzPassesIdempotent: every pass satisfies f(f(x)) == f(x): Path, Text,
// Content, and the redact package's HomePaths and Prose.
func FuzzPassesIdempotent(f *testing.F) {
	for i := range fuzzHomes {
		for _, s := range leakSeeds {
			f.Add(s, uint8(i))
		}
		for _, s := range []string{
			"file:///home/me/x", "//home/me/x", "///home/zed/x", "[redacted-secret]-home-bob-x",
			"GITHUB_TOKEN=abc-home-bob-x", "~//home/bob", "~/x ~/y", "-~-[redacted-slug]",
			"x -home-zed/home/bob", "-home-zed/home/zed", "-home-zed//w/repo", "-home-zed//home/zed",
		} {
			f.Add(s, uint8(i))
		}
	}
	f.Fuzz(func(t *testing.T, s string, h uint8) {
		home := fuzzHomes[int(h)%len(fuzzHomes)]
		r := PathRedactor{Home: home, Repo: "/work/repo"}
		for name, pass := range map[string]func(string) string{
			"Path": r.Path, "Text": r.Text, "Content": r.Content,
			"HomePaths": redact.HomePaths, "Prose": redact.Prose,
		} {
			once := pass(s)
			if twice := pass(once); twice != once {
				t.Fatalf("%s is not idempotent (home %s): %q -> %q -> %q", name, home, s, once, twice)
			}
		}
	})
}
