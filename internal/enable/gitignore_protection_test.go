package enable

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

const extensionOnlyIgnore = "*.jsonl\n*.json\n*.ethos-tmp\n*.env\n"

const composedLocalIgnore = ".punt-labs/local\n!.punt-labs/local/\n.punt-labs/local/*\n!.punt-labs/local/unforeseen\n"

func TestLocalIgnoredRejectsSameRuleGlobstar(t *testing.T) {
	for _, rule := range []string{".punt-labs/**", ".punt-labs/**/*", ".punt-labs/**/", ".punt-labs/**/*/"} {
		for _, existing := range []bool{false, true} {
			name := "absent"
			if existing {
				name = "existing"
			}
			t.Run(rule+"/"+name, func(t *testing.T) {
				root := gitRepo(t)
				initial := rule + "\n!.punt-labs/local/\n!.punt-labs/local/unforeseen\n"
				path := filepath.Join(root, ".gitignore")
				require.NoError(t, os.WriteFile(path, []byte(initial), 0o644))
				local := filepath.Join(root, ".punt-labs/local")
				if existing {
					require.NoError(t, os.MkdirAll(local, 0o700))
				}
				matches, err := gitIgnoreMatches(root, []string{".punt-labs/local", LocalIgnoreRule})
				require.NoError(t, err)
				t.Logf("real Git candidate identities: %#v", matches)
				if existing {
					assert.Equal(t, gitIgnoreMatch{source: ".gitignore", line: 2, pattern: "!.punt-labs/local/"}, matches[".punt-labs/local"])
					assert.Equal(t, gitIgnoreMatch{source: ".gitignore", line: 1, pattern: rule}, matches[LocalIgnoreRule])
				} else if !strings.HasSuffix(rule, "/") {
					candidate := gitIgnoreMatch{source: ".gitignore", line: 1, pattern: rule}
					assert.Equal(t, candidate, matches[".punt-labs/local"])
					assert.Equal(t, candidate, matches[LocalIgnoreRule], "identical positive rules still do not prove directory exclusion")
				} else {
					assert.Empty(t, matches)
				}
				covered, err := LocalIgnored(root)
				require.NoError(t, err)
				assert.False(t, covered)
				assert.Equal(t, initial, readFile(t, path), "verification must preserve operator rules")
				if !existing {
					assert.NoDirExists(t, local, "the read predicate must not materialize its subject")
				}
				require.NoError(t, os.MkdirAll(local, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(local, "unforeseen"), []byte("synthetic private data\n"), 0o600))
				gitRun(t, root, "add", "-A")
				assert.Contains(t, gitOutput(t, root, "diff", "--cached", "--name-only"), LocalIgnoreRule+"unforeseen",
					"create after verification, then demonstrate the actual staging hole")
			})
		}
	}
}

func TestGitignoreDirectoryProofRejectsWildcardTerminals(t *testing.T) {
	for _, component := range []string{"*", "**", "***", "**/*", "**/**", "loc*", "l?cal", "[l]ocal", `lo\cal`} {
		for _, suffix := range []string{"", "/"} {
			t.Run(component+suffix, func(t *testing.T) {
				candidate := gitIgnoreMatch{source: ".gitignore", line: 1, pattern: ".punt-labs/" + component + suffix}
				assert.False(t, gitDirectoryMatchesExclude(candidate, candidate), "apply the conservative criterion before same-rule identity")
				assert.False(t, gitDirectoryMatchesExclude(gitIgnoreMatch{}, candidate), "directory-only syntax cannot bypass the criterion either")
			})
		}
	}
}

func TestGitignoreEstablishedDirectoryRulesRemainNoWrite(t *testing.T) {
	for _, tc := range []struct{ source, rule string }{
		{".gitignore", ".punt-labs/"},
		{".gitignore", "/.punt-labs/"},
		{".gitignore", ".punt-labs"},
		{".gitignore", ".punt-labs/local"},
		{".gitignore", "/.punt-labs/local/"},
		{".gitignore", "local/"},
		{".gitignore", ".punt-labs/**/local/"},
		{".gitignore", ".punt-labs/**/local"},
		{".punt-labs/.gitignore", "local/"},
	} {
		t.Run(tc.source+"/"+tc.rule, func(t *testing.T) {
			root := gitRepo(t)
			path := filepath.Join(root, filepath.FromSlash(tc.source))
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			initial := tc.rule + "\n"
			require.NoError(t, os.WriteFile(path, []byte(initial), 0o644))
			before, err := os.Stat(path)
			require.NoError(t, err)
			p, err := layout.NewRepository(root, root, layout.RecordBlame)
			require.NoError(t, err)
			for _, existing := range []bool{false, true} {
				if existing {
					require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs/local"), 0o700))
				}
				covered, err := LocalIgnored(root)
				require.NoError(t, err)
				assert.True(t, covered)
				for range 2 {
					added, err := EnsureLocalIgnored(root, p)
					require.NoError(t, err)
					assert.False(t, added)
				}
				if !existing {
					assert.NoDirExists(t, filepath.Join(root, ".punt-labs/local"))
				}
			}
			after, err := os.Stat(path)
			require.NoError(t, err)
			assert.True(t, os.SameFile(before, after), "established alternate exclusions must not be rewritten")
			assert.Equal(t, initial, readFile(t, path))
			if tc.source != ".gitignore" {
				assert.NoFileExists(t, filepath.Join(root, ".gitignore"))
			}
			require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs/local/unforeseen"), []byte("synthetic private data\n"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs/local/.gitignore"), []byte("!unforeseen\n"), 0o644))
			gitRun(t, root, "add", "-A")
			assert.NotContains(t, gitOutput(t, root, "diff", "--cached", "--name-only"), LocalIgnoreRule)
		})
	}
}

func TestEnableRepairsSameRuleGlobstarAtLinkedCheckout(t *testing.T) {
	for _, rule := range []string{".punt-labs/**", ".punt-labs/**/*", ".punt-labs/**/", ".punt-labs/**/*/"} {
		for _, maintainer := range []string{"enable", "setup-vendor"} {
			t.Run(rule+"/"+maintainer, func(t *testing.T) {
				store := gitRepo(t)
				gitRun(t, store, "commit", "--allow-empty", "-qm", "initial")
				checkout := filepath.Join(physicalTempDir(t), "linked")
				gitRun(t, store, "worktree", "add", "-qb", "ignore-globstar", checkout)
				initial := rule + "\n!.punt-labs/local/\n!.punt-labs/local/unforeseen\n"
				for _, root := range []string{store, checkout} {
					require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(initial), 0o644))
					assert.NoDirExists(t, filepath.Join(root, ".punt-labs/local"))
				}
				p, err := layout.NewRepository(store, checkout, layout.RecordBlame)
				require.NoError(t, err)
				first := make(map[string]string)
				for attempt := range 2 {
					if maintainer == "enable" {
						_, err = EnableTo(checkout, store)
						require.NoError(t, err)
						assert.FileExists(t, filepath.Join(checkout, markerRel))
					} else {
						for _, root := range []string{store, checkout} {
							_, err = EnsureLocalIgnored(root, p)
							require.NoError(t, err)
						}
						assert.NoFileExists(t, filepath.Join(checkout, markerRel))
					}
					assert.NoDirExists(t, filepath.Join(checkout, ".punt-labs/local"), "C stays absent while staging belongs at S")
					for _, root := range []string{store, checkout} {
						body := readFile(t, filepath.Join(root, ".gitignore"))
						assert.True(t, strings.HasPrefix(body, initial), "all operator rule bytes survive")
						assert.Equal(t, 1, strings.Count("\n"+body, "\n"+LocalIgnoreRule+"\n"))
						if attempt == 0 {
							first[root] = body
						} else {
							assert.Equal(t, first[root], body)
						}
					}
				}
				for _, root := range []string{store, checkout} {
					local := filepath.Join(root, ".punt-labs/local")
					require.NoError(t, os.MkdirAll(local, 0o700))
					require.NoError(t, os.WriteFile(filepath.Join(local, "unforeseen"), []byte("synthetic private data\n"), 0o600))
					require.NoError(t, os.WriteFile(filepath.Join(root, "public.txt"), []byte("public\n"), 0o644))
					gitRun(t, root, "add", "-A")
					staged := gitOutput(t, root, "diff", "--cached", "--name-only")
					assert.Contains(t, staged, "public.txt")
					assert.NotContains(t, staged, LocalIgnoreRule)
				}
			})
		}
	}
}

func TestLocalIgnoredRejectsComposedRules(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "absent"
		if existing {
			name = "existing"
		}
		t.Run(name, func(t *testing.T) {
			root := gitRepo(t)
			require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(composedLocalIgnore), 0o644))
			local := filepath.Join(root, ".punt-labs/local")
			if existing {
				require.NoError(t, os.MkdirAll(local, 0o700))
			}
			t.Log(gitOutput(t, root, "check-ignore", "-v", "--no-index", ".punt-labs/local", ".punt-labs/local/"))
			covered, err := LocalIgnored(root)
			require.NoError(t, err)
			assert.False(t, covered, "different positive rules cannot establish directory exclusion")
			if !existing {
				assert.NoDirExists(t, local, "the coverage reader must not materialize the directory")
			}
			require.NoError(t, os.MkdirAll(local, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(local, "unforeseen"), []byte("synthetic private data\n"), 0o600))
			gitRun(t, root, "add", "-A")
			assert.Contains(t, gitOutput(t, root, "diff", "--cached", "--name-only"), ".punt-labs/local/unforeseen",
				"real Git must demonstrate that the composed rules leave private data stageable")
		})
	}
}

func TestEnableRepairsComposedRulesAtAbsentLinkedCheckout(t *testing.T) {
	store := gitRepo(t)
	gitRun(t, store, "commit", "--allow-empty", "-qm", "initial")
	checkout := filepath.Join(physicalTempDir(t), "linked")
	gitRun(t, store, "worktree", "add", "-qb", "ignore-composition", checkout)
	for _, root := range []string{store, checkout} {
		require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(composedLocalIgnore), 0o644))
		assert.NoDirExists(t, filepath.Join(root, ".punt-labs/local"))
	}
	_, err := EnableTo(checkout, store)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(checkout, markerRel))
	assert.DirExists(t, filepath.Join(store, ".punt-labs/local/ethos/tmp"), "writer staging belongs at S")
	assert.NoDirExists(t, filepath.Join(checkout, ".punt-labs/local"), "writing C must not stage at C")
	for _, root := range []string{store, checkout} {
		body := readFile(t, filepath.Join(root, ".gitignore"))
		assert.True(t, strings.HasPrefix(body, composedLocalIgnore))
		assert.True(t, hasExactLine(body, LocalIgnoreRule), "insufficient proof must install the canonical directory rule")
		local := filepath.Join(root, ".punt-labs/local")
		require.NoError(t, os.MkdirAll(local, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(local, "unforeseen"), []byte("synthetic private data\n"), 0o600))
		gitRun(t, root, "add", "-A")
		assert.NotContains(t, gitOutput(t, root, "diff", "--cached", "--name-only"), ".punt-labs/local/")
	}
}

func TestEnableRepairsDescendantOnlyRule(t *testing.T) {
	root := gitRepo(t)
	const initial = ".punt-labs/**/local/**\n.punt-labs/**/*.lock\n.punt-labs/**/*.ethos-tmp*\n.punt-labs/**/*.local.*\n"
	path := filepath.Join(root, ".gitignore")
	require.NoError(t, os.WriteFile(path, []byte(initial), 0o644))
	rep, err := Enable(root)
	require.NoError(t, err)
	var action string
	for _, step := range rep.Steps {
		if step.Step == "gitignore" {
			action = step.Status
		}
	}
	assert.Equal(t, "added", action, "contents-only coverage cannot substitute for directory exclusion")
	body := readFile(t, path)
	assert.True(t, strings.HasPrefix(body, initial), "all original bytes must survive")
	assert.True(t, hasExactLine(body, LocalIgnoreRule))
	_, err = Enable(root)
	require.NoError(t, err)
	assert.Equal(t, body, readFile(t, path), "repair must be idempotent")
	local := filepath.Join(root, ".punt-labs/local/unforeseen")
	require.NoError(t, os.WriteFile(local, []byte("synthetic private data\n"), 0o600))
	gitRun(t, root, "add", "-A")
	assert.NotContains(t, gitOutput(t, root, "diff", "--cached", "--name-only"), ".punt-labs/local/")
}

func TestGitignoreMalformedResponsesFailClosed(t *testing.T) {
	for _, output := range []string{
		"not a NUL-delimited response",
		".gitignore\x001\x00.punt-labs/local/\x00.punt-labs/local/",
		".gitignore\x001\x00.punt-labs/local/\x00",
		".gitignore\x001\x00.punt-labs/local/\x00.punt-labs/local/\x00extra\x00",
		".gitignore\x00not-a-number\x00.punt-labs/local/\x00.punt-labs/local/\x00",
		".gitignore\x000\x00.punt-labs/local/\x00.punt-labs/local/\x00",
		".gitignore\x00-1\x00.punt-labs/local/\x00.punt-labs/local/\x00",
		"\x001\x00.punt-labs/local/\x00.punt-labs/local/\x00",
		".gitignore\x001\x00\x00.punt-labs/local/\x00",
		".gitignore\x001\x00.punt-labs/local/\x00\x00",
		strings.Repeat(".gitignore\x001\x00.punt-labs/local/\x00.punt-labs/local/\x00", 2),
	} {
		matches, err := parseGitIgnoreMatches(output)
		require.Error(t, err)
		assert.Empty(t, matches, "a partial response cannot establish protection")
	}
	matches, err := parseGitIgnoreMatches(".gitignore\x001\x00.punt-labs/local/\x00.punt-labs/local/\x00")
	require.NoError(t, err)
	assert.Equal(t, gitIgnoreMatch{source: ".gitignore", line: 1, pattern: LocalIgnoreRule}, matches[LocalIgnoreRule])
}

func TestGitignoreMatchesRetainRuleIdentity(t *testing.T) {
	matches, err := parseGitIgnoreMatches(".gitignore\x001\x00.punt-labs/local\x00.punt-labs/local\x00" +
		".gitignore\x003\x00.punt-labs/local/*\x00.punt-labs/local/\x00" +
		".punt-labs/.gitignore\x002\x00!local/\x00.punt-labs/local/negated\x00" +
		".git/info/exclude\x004\x00local/\x00.punt-labs/local/machine-only\x00")
	require.NoError(t, err)
	assert.Equal(t, gitIgnoreMatch{source: ".gitignore", line: 1, pattern: ".punt-labs/local"}, matches[".punt-labs/local"])
	assert.Equal(t, gitIgnoreMatch{source: ".gitignore", line: 3, pattern: ".punt-labs/local/*"}, matches[LocalIgnoreRule])
	assert.Equal(t, gitIgnoreMatch{source: ".punt-labs/.gitignore", line: 2, pattern: "!local/"}, matches[LocalIgnoreRule+"negated"])
	assert.Equal(t, gitIgnoreMatch{source: ".git/info/exclude", line: 4, pattern: "local/"}, matches[LocalIgnoreRule+"machine-only"])
	assert.True(t, matches[".punt-labs/local"].excludes())
	assert.False(t, matches[LocalIgnoreRule+"negated"].excludes())
	assert.False(t, matches[LocalIgnoreRule+"machine-only"].excludes())
	assert.False(t, matches[LocalIgnoreRule+"unmatched"].excludes())
}

func TestGitignoreDirectoryProofRequiresSameRule(t *testing.T) {
	positive := gitIgnoreMatch{source: ".gitignore", line: 1, pattern: ".punt-labs/local"}
	for _, tc := range []struct {
		name            string
		bare, directory gitIgnoreMatch
		want            bool
	}{
		{"same rule", positive, positive, true},
		{"different source", positive, gitIgnoreMatch{source: ".punt-labs/.gitignore", line: 1, pattern: positive.pattern}, false},
		{"different line", positive, gitIgnoreMatch{source: positive.source, line: 3, pattern: positive.pattern}, false},
		{"different pattern", positive, gitIgnoreMatch{source: positive.source, line: 1, pattern: ".punt-labs/local/*"}, false},
		{"negative directory match", positive, gitIgnoreMatch{source: positive.source, line: 2, pattern: "!.punt-labs/local/"}, false},
		{"machine-only directory match", positive, gitIgnoreMatch{source: ".git/info/exclude", line: 1, pattern: LocalIgnoreRule}, false},
		{"absent bare directory", gitIgnoreMatch{}, gitIgnoreMatch{source: positive.source, line: 1, pattern: LocalIgnoreRule}, true},
		{"empty wildcard directory component", gitIgnoreMatch{}, gitIgnoreMatch{source: positive.source, line: 1, pattern: ".punt-labs/local/*/"}, false},
		{"empty globstar directory component", gitIgnoreMatch{}, gitIgnoreMatch{source: positive.source, line: 1, pattern: ".punt-labs/local/**/"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, gitDirectoryMatchesExclude(tc.bare, tc.directory))
		})
	}
}

func TestLocalIgnoredRequiresDirectoryExclusion(t *testing.T) {
	for _, tc := range []struct {
		name, rules string
		want        bool
	}{
		{"combined extensions", extensionOnlyIgnore, false},
		{"descendants only", ".punt-labs/local/**\n", false},
		{"direct children only", ".punt-labs/local/*\n", false},
		{"child directories only", ".punt-labs/local/*/\n", false},
		{"descendant directories only", ".punt-labs/local/**/\n", false},
		{"descendants with arbitrary negation", ".punt-labs/local/**\n!.punt-labs/local/unforeseen\n", false},
		{"canonical directory", LocalIgnoreRule + "\n", true},
		{"wildcard directory", ".punt-labs/**/local/\n", true},
		{"wildcard directory basename needs conservative repair", ".punt-labs/**/loc*/\n", false},
		{"single-character directory wildcard needs conservative repair", ".punt-labs/**/l?cal/\n", false},
		{"ancestor directory", ".punt-labs/\n", true},
		{"ancestor children need conservative repair", ".punt-labs/*\n", false},
		{"directory without slash", ".punt-labs/local\n", true},
		{"directory cannot reinclude child", LocalIgnoreRule + "\n!.punt-labs/local/unforeseen\n", true},
		{"directory negation", LocalIgnoreRule + "\n!.punt-labs/local/\n", false},
		{"directory-only negation defeats untyped match", ".punt-labs/local\n!.punt-labs/local/\n", false},
		{"directory-only negation defeats wildcard match", ".punt-labs/*\n!.punt-labs/local/\n", false},
		{"composed positives", composedLocalIgnore, false},
		{"composed directory-only positives", ".punt-labs/local\n!.punt-labs/local/\n.punt-labs/local/*/\n", false},
		{"directory wildcard with effective negation", ".punt-labs/**/local/\n!.punt-labs/local/\n", false},
		{"directory wildcard blocks child negation", ".punt-labs/**/local/\n!.punt-labs/local/unforeseen\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := gitRepo(t)
			require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(tc.rules), 0o644))
			for _, exists := range []bool{false, true} {
				if exists {
					require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs/local"), 0o700))
				}
				got, err := LocalIgnored(root)
				require.NoError(t, err)
				assert.Equal(t, tc.want, got, "directory exists: %v", exists)
			}
		})
	}
}

func TestGitignoreRepairsDirectoryOnlyContentsRules(t *testing.T) {
	for _, rule := range []string{".punt-labs/local/*/", ".punt-labs/local/**/"} {
		for _, maintainer := range []string{"enable", "setup-vendor"} {
			t.Run(rule+"/"+maintainer, func(t *testing.T) {
				root := gitRepo(t)
				p, err := layout.NewRepository(root, root, layout.RecordBlame)
				require.NoError(t, err)
				local := filepath.Join(root, ".punt-labs/local")
				require.NoError(t, os.MkdirAll(local, 0o700))
				path := filepath.Join(root, ".gitignore")
				require.NoError(t, os.WriteFile(path, []byte(rule+"\n"), 0o644))
				var first string
				for attempt := range 2 {
					if maintainer == "enable" {
						_, err = Enable(root)
					} else {
						_, err = EnsureLocalIgnored(root, p)
					}
					require.NoError(t, err)
					body := readFile(t, path)
					assert.True(t, strings.HasPrefix(body, rule+"\n"))
					assert.True(t, hasExactLine(body, LocalIgnoreRule))
					if attempt == 0 {
						first = body
					} else {
						assert.Equal(t, first, body)
					}
				}
				require.NoError(t, os.WriteFile(filepath.Join(local, "unforeseen"), []byte("synthetic private data\n"), 0o600))
				gitRun(t, root, "add", "-A")
				assert.NotContains(t, gitOutput(t, root, "diff", "--cached", "--name-only"), ".punt-labs/local/")
			})
		}
	}
}

func TestLocalIgnoredMissingGitFailsClosed(t *testing.T) {
	root := gitRepo(t)
	path := filepath.Join(root, ".gitignore")
	initial := LocalIgnoreRule + "\n!.punt-labs/local/\n"
	require.NoError(t, os.WriteFile(path, []byte(initial), 0o644))
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	t.Setenv("PATH", filepath.Join(t.TempDir(), "no-git"))
	covered, err := LocalIgnored(root)
	assert.False(t, covered)
	require.ErrorIs(t, err, exec.ErrNotFound)
	added, err := EnsureLocalIgnored(root, p)
	assert.False(t, added)
	assert.ErrorIs(t, err, exec.ErrNotFound)
	assert.Equal(t, initial, readFile(t, path))
}

func TestLocalIgnoredUsesRequestedRoot(t *testing.T) {
	root, other := gitRepo(t), gitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(other, ".gitignore"), []byte(LocalIgnoreRule+"\n"), 0o644))
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	covered, err := LocalIgnored(root)
	require.NoError(t, err)
	assert.False(t, covered, "another checkout's rules cannot certify this root")
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	added, err := EnsureLocalIgnored(root, p)
	require.NoError(t, err)
	assert.True(t, added)
	assert.Contains(t, readFile(t, filepath.Join(root, ".gitignore")), LocalIgnoreRule)
}

func TestEnableRejectsDefeatedProtectionWithExistingMarker(t *testing.T) {
	root := gitRepo(t)
	_, err := Enable(root)
	require.NoError(t, err)
	marker := filepath.Join(root, markerRel)
	before, err := os.Stat(marker)
	require.NoError(t, err)
	nested := filepath.Join(root, ".punt-labs/.gitignore")
	require.NoError(t, os.WriteFile(nested, []byte("!local/\n"), 0o644))
	_, err = Enable(root)
	require.Error(t, err, "an existing marker must not turn defeated protection into success")
	assert.Contains(t, err.Error(), "still stageable")
	after, err := os.Stat(marker)
	require.NoError(t, err)
	assert.True(t, os.SameFile(before, after), "a failure does not roll back earlier enablement")
}

func TestEnableLegacyRetirementRefusesDefeatedProtection(t *testing.T) {
	root := gitRepo(t)
	const initial = LocalIgnoreRule + "\n!.punt-labs/local/\n" + legacyGitignoreMarker + "\n.punt-labs/**/local/**\n"
	path := filepath.Join(root, ".gitignore")
	require.NoError(t, os.WriteFile(path, []byte(initial), 0o644))
	var first string
	for attempt := range 2 {
		rep, err := Enable(root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "still stageable")
		var step StepResult
		for _, s := range rep.Steps {
			if s.Step == "gitignore" {
				step = s
			}
		}
		assert.Equal(t, "attention", step.Status)
		assert.Contains(t, step.Detail, "still stageable")
		assert.NoFileExists(t, filepath.Join(root, markerRel))
		body := readFile(t, path)
		assert.Equal(t, LocalIgnoreRule+"\n!.punt-labs/local/\n"+gitignoreMarker+"\n", body)
		if attempt == 0 {
			first = body
		} else {
			assert.Equal(t, first, body, "failed retry cannot append a duplicate or undo the prior retirement")
		}
	}
}

func TestEnableRepairsCombinedExtensionFalsifier(t *testing.T) {
	root := gitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(extensionOnlyIgnore), 0o644))
	_, err := Enable(root)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(readFile(t, filepath.Join(root, ".gitignore")), extensionOnlyIgnore))
	for _, rel := range []string{
		".punt-labs/local/ethos/trace/missions/m-test/prompt.md",
		".punt-labs/local/ethos/identities/user.ext/tool.local.yaml",
		".punt-labs/local/arbitrary-new-filename",
	} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte("synthetic private data\n"), 0o600))
	}
	gitRun(t, root, "add", "-A")
	assert.NotContains(t, gitOutput(t, root, "diff", "--cached", "--name-only"), ".punt-labs/local/")
}

func TestEnableRejectsNestedNegationAtEitherRoot(t *testing.T) {
	for _, host := range []string{"checkout", "store"} {
		for _, canonical := range []bool{false, true} {
			name := "absent"
			if canonical {
				name = "present"
			}
			t.Run(host+"/canonical="+name, func(t *testing.T) {
				store := gitRepo(t)
				gitRun(t, store, "commit", "--allow-empty", "-qm", "initial")
				checkout := filepath.Join(t.TempDir(), "linked")
				gitRun(t, store, "worktree", "add", "-qb", "ignore-linked", checkout)
				root := checkout
				if host == "store" {
					root = store
				}
				const userRules = "# operator rules\n*.log\n"
				initial := userRules
				if canonical {
					initial += LocalIgnoreRule + "\n"
				}
				require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(initial), 0o644))
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs"), 0o755))
				const nested = "# operator override\n!local/\nother-pattern\n"
				nestedPath := filepath.Join(root, ".punt-labs/.gitignore")
				require.NoError(t, os.WriteFile(nestedPath, []byte(nested), 0o644))
				var first string
				for attempt := range 2 {
					_, err := EnableTo(checkout, store)
					require.Error(t, err, "attempt %d must not enable an unprotected checkout", attempt)
					assert.Contains(t, err.Error(), root)
					assert.Contains(t, err.Error(), "still stageable")
					assert.NoFileExists(t, filepath.Join(checkout, markerRel))
					body := readFile(t, filepath.Join(root, ".gitignore"))
					assert.True(t, strings.HasPrefix(body, initial))
					assert.Equal(t, 1, strings.Count(body, LocalIgnoreRule))
					assert.Equal(t, nested, readFile(t, nestedPath))
					if attempt == 0 {
						first = body
					} else {
						assert.Equal(t, first, body, "failed retries must preserve user rules")
					}
				}
				require.NoError(t, os.WriteFile(nestedPath, []byte("# operator override\nother-pattern\n"), 0o644))
				_, err := EnableTo(checkout, store)
				require.NoError(t, err)
				assert.FileExists(t, filepath.Join(checkout, markerRel))
				for _, hostRoot := range []string{store, checkout} {
					got, err := LocalIgnored(hostRoot)
					require.NoError(t, err)
					assert.True(t, got, hostRoot)
				}
			})
		}
	}
}
