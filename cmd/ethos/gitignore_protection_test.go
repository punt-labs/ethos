//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/enable"
)

func TestCLI_LocalIgnoreComposedRules(t *testing.T) {
	for _, operation := range []string{"enable", "setup", "solo", "vendor"} {
		t.Run(operation, func(t *testing.T) {
			se, store := linkedIgnoreEnv(t)
			const initial = ".punt-labs/local\n!.punt-labs/local/\n.punt-labs/local/*\n!.punt-labs/local/unforeseen\n"
			for _, root := range []string{store, se.repo} {
				writeRepoFile(t, root, ".gitignore", initial)
				assert.NoDirExists(t, filepath.Join(root, ".punt-labs/local"))
			}
			if operation == "setup" || operation == "solo" {
				_, stderr, code := runCLI(t, se, "seed")
				require.Equal(t, 0, code, stderr)
			}
			for attempt := range 2 {
				var stderr string
				var code int
				if operation == "enable" {
					_, stderr, code = runCLI(t, se, "enable")
				} else {
					stderr, code = runLinkedIgnoreCLI(t, se, operation)
				}
				require.Equal(t, 0, code, stderr)
				assert.DirExists(t, filepath.Join(store, ".punt-labs/local/ethos/tmp"), "staging must belong to S")
				assert.NoDirExists(t, filepath.Join(se.repo, ".punt-labs/local"), "C must remain absent until a private writer creates it")
				for _, root := range []string{store, se.repo} {
					body := readGitignore(t, filepath.Join(root, ".gitignore"))
					assert.True(t, strings.HasPrefix(body, initial))
					assert.Equal(t, 1, countIgnoreLine(body, enable.LocalIgnoreRule), "canonical repair must be idempotent, attempt %d", attempt)
				}
			}
			if operation == "enable" {
				assert.FileExists(t, filepath.Join(se.repo, ".punt-labs/ethos/enabled"))
			} else {
				assert.NoFileExists(t, filepath.Join(se.repo, ".punt-labs/ethos/enabled"))
			}
			for _, root := range []string{store, se.repo} {
				writeRepoFile(t, root, ".punt-labs/local/unforeseen", "synthetic private sentinel\n")
				writeRepoFile(t, root, ".punt-labs/local/another-arbitrary-filename", "synthetic private sentinel\n")
				const public = ".punt-labs/ethos/adrs/public.yaml"
				writeRepoFile(t, root, public, "title: Public\n")
				staged := gitAddAll(t, root)
				assert.Contains(t, staged, public)
				for _, path := range staged {
					assert.NotContains(t, path, ".punt-labs/local/", "private path staged at %s", root)
				}
			}
		})
	}
}

func countIgnoreLine(body, want string) int {
	count := 0
	for _, line := range strings.Split(body, "\n") {
		if line == want {
			count++
		}
	}
	return count
}

func TestCLI_LocalIgnoreSameRuleGlobstar(t *testing.T) {
	for _, rule := range []string{".punt-labs/**", ".punt-labs/**/*", ".punt-labs/**/", ".punt-labs/**/*/"} {
		for _, operation := range []string{"enable", "setup", "solo", "vendor"} {
			t.Run(rule+"/"+operation, func(t *testing.T) {
				se, store := linkedIgnoreEnv(t)
				initial := rule + "\n!.punt-labs/local/\n!.punt-labs/local/unforeseen\n"
				for _, root := range []string{store, se.repo} {
					writeRepoFile(t, root, ".gitignore", initial)
					assert.NoDirExists(t, filepath.Join(root, ".punt-labs/local"))
				}
				if operation == "setup" || operation == "solo" {
					_, stderr, code := runCLI(t, se, "seed")
					require.Equal(t, 0, code, stderr)
				}
				first := make(map[string]string)
				for attempt := range 2 {
					var stderr string
					var code int
					if operation == "enable" {
						_, stderr, code = runCLI(t, se, "enable")
					} else {
						stderr, code = runLinkedIgnoreCLI(t, se, operation)
					}
					require.Equal(t, 0, code, stderr)
					assert.DirExists(t, filepath.Join(store, ".punt-labs/local/ethos/tmp"))
					assert.NoDirExists(t, filepath.Join(se.repo, ".punt-labs/local"), "verification and C writes must not create C/local")
					for _, root := range []string{store, se.repo} {
						body := readGitignore(t, filepath.Join(root, ".gitignore"))
						assert.True(t, strings.HasPrefix(body, initial), "preserve effective negations for operator control")
						assert.Equal(t, 1, countIgnoreLine(body, enable.LocalIgnoreRule))
						if attempt == 0 {
							first[root] = body
						} else {
							assert.Equal(t, first[root], body, "repair must be byte-stable on retry")
						}
					}
				}
				if operation == "enable" {
					assert.FileExists(t, filepath.Join(se.repo, ".punt-labs/ethos/enabled"))
				} else {
					assert.NoFileExists(t, filepath.Join(se.repo, ".punt-labs/ethos/enabled"))
				}
				for _, root := range []string{store, se.repo} {
					writeRepoFile(t, root, ".punt-labs/local/unforeseen", "synthetic private sentinel\n")
					writeRepoFile(t, root, ".punt-labs/local/another-arbitrary-filename", "synthetic private sentinel\n")
					// The operator's broad rule also ignores records, so use a
					// public path outside that rule as the ordinary-add control.
					writeRepoFile(t, root, "public.txt", "public\n")
					staged := gitAddAll(t, root)
					assert.Contains(t, staged, "public.txt")
					for _, path := range staged {
						assert.NotContains(t, path, enable.LocalIgnoreRule, "future private path staged at %s", root)
					}
				}
			})
		}
	}
}

func TestCLI_LocalIgnoreCombinedExtensions(t *testing.T) {
	for _, operation := range []string{"enable", "setup", "solo", "vendor"} {
		t.Run(operation, func(t *testing.T) {
			se, store := linkedIgnoreEnv(t)
			const initial = "# user rules\n*.jsonl\n*.json\n*.ethos-tmp\n*.env\n"
			before := make(map[string]os.FileInfo)
			for _, root := range []string{store, se.repo} {
				writeRepoFile(t, root, ".gitignore", initial)
				var err error
				before[root], err = os.Stat(filepath.Join(root, ".gitignore"))
				require.NoError(t, err)
				covered, err := enable.LocalIgnored(root)
				require.NoError(t, err)
				assert.False(t, covered, "extension coverage cannot prove directory protection")
			}
			if operation == "setup" || operation == "solo" {
				_, stderr, code := runCLI(t, se, "seed")
				require.Equal(t, 0, code, stderr)
			}
			for range 2 {
				var stderr string
				var code int
				if operation == "enable" {
					_, stderr, code = runCLI(t, se, "enable")
				} else {
					stderr, code = runLinkedIgnoreCLI(t, se, operation)
				}
				require.Equal(t, 0, code, stderr)
			}
			for _, root := range []string{store, se.repo} {
				body := readGitignore(t, filepath.Join(root, ".gitignore"))
				after, err := os.Stat(filepath.Join(root, ".gitignore"))
				require.NoError(t, err)
				assert.False(t, os.SameFile(before[root], after), "real command replaces atomically, not in place")
				assert.Equal(t, before[root].Mode().Perm(), after.Mode().Perm())
				assert.True(t, strings.HasPrefix(body, initial))
				assert.Equal(t, 1, strings.Count(body, enable.LocalIgnoreRule))
				for _, rel := range []string{
					".punt-labs/local/ethos/trace/missions/m-test/prompt.md",
					".punt-labs/local/ethos/identities/user.ext/tool.local.yaml",
					".punt-labs/local/arbitrary-new-filename",
				} {
					writeRepoFile(t, root, rel, "synthetic private sentinel\n")
				}
				const public = ".punt-labs/ethos/adrs/public.yaml"
				writeRepoFile(t, root, public, "title: Public\n")
				staged := gitAddAll(t, root)
				assert.Contains(t, staged, public, "ordinary add must still stage source records")
				for _, path := range staged {
					assert.NotContains(t, path, ".punt-labs/local/", "local path staged at %s", root)
				}
				covered, err := enable.LocalIgnored(root)
				require.NoError(t, err)
				assert.True(t, covered)
			}
		})
	}
}

func TestCLI_EnableNestedIgnoreNegation(t *testing.T) {
	for _, host := range []string{"store", "checkout"} {
		for _, canonical := range []bool{false, true} {
			name := "absent"
			if canonical {
				name = "present"
			}
			t.Run(host+"/canonical="+name, func(t *testing.T) {
				se, store := linkedIgnoreEnv(t)
				root := store
				if host == "checkout" {
					root = se.repo
				}
				initial := linkedLegacyIgnore
				if canonical {
					initial += enable.LocalIgnoreRule + "\n"
				}
				writeRepoFile(t, root, ".gitignore", initial)
				writeRepoFile(t, root, ".punt-labs/.gitignore", linkedNestedNegation)
				var first string
				for attempt := range 2 {
					_, stderr, code := runCLI(t, se, "enable", "--json")
					require.NotZero(t, code, "defeated rule must fail on first write and retry")
					assert.Contains(t, stderr, root)
					assert.Contains(t, stderr, "still stageable")
					assert.NoFileExists(t, filepath.Join(se.repo, ".punt-labs/ethos/enabled"))
					body := assertLinkedNestedNegation(t, root)
					if attempt == 0 {
						first = body
					} else {
						assert.Equal(t, first, body)
					}
				}
				writeRepoFile(t, root, ".punt-labs/.gitignore", linkedNestedRepair)
				_, stderr, code := runCLI(t, se, "enable", "--json")
				require.Equal(t, 0, code, stderr)
				assert.FileExists(t, filepath.Join(se.repo, ".punt-labs/ethos/enabled"))
				for _, root := range []string{store, se.repo} {
					covered, err := enable.LocalIgnored(root)
					require.NoError(t, err)
					assert.True(t, covered)
				}
				_, err := os.Stat(filepath.Join(root, ".punt-labs/.gitignore"))
				require.NoError(t, err)
				assert.Equal(t, linkedNestedRepair, readGitignore(t, filepath.Join(root, ".punt-labs/.gitignore")))
			})
		}
	}
}
