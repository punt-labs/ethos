//go:build linux || darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func bundleDiagnosticEnv(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "xdg"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Setenv("GIT_SSH_COMMAND", "")
	t.Setenv("GIT_SSH", "")
	require.NoError(t, os.Unsetenv("GIT_SSH_COMMAND"))
	require.NoError(t, os.Unsetenv("GIT_SSH"))
	// Restrict the actual Git transport, including the failed clone, to local
	// paths. A fixture/configuration mistake must not reach a live endpoint.
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	return base
}

func bundleDiagnosticGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := testhelpers.GitCommand(append([]string{"-C", dir, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// bundleDiagnosticMkdirPaths observes attempted mkdir/mkdirat destinations,
// including a temporary subsequently removed. It is deliberately NOT a general
// write/open/metadata observer. The trace contains only mkdir, mkdirat and
// execve, with -f -yy -s 65535 and numeric tids. Relative mkdir operands are refused;
// relative mkdirat requires an absolute annotated dirfd. Split calls are joined
// by tid, and unsupported/truncated records fail rather than disappearing.
func bundleDiagnosticMkdirPaths(trace string) ([]string, error) {
	pending := map[string]string{}
	var paths []string
	call := regexp.MustCompile(`^(mkdir|mkdirat)\((.*)\) += (.+)$`)
	operand := `("(?:[^"\\]|\\.)*")`
	mkdir := regexp.MustCompile(`^` + operand + `, 0[0-7]+$`)
	mkdirat := regexp.MustCompile(`^([^,]+), ` + operand + `, 0[0-7]+$`)
	fd := regexp.MustCompile(`^(?:AT_FDCWD|[0-9]+)<([^<>]+)>$`)
	for _, line := range strings.Split(trace, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		tid, text, ok := strings.Cut(strings.TrimSpace(line), " ")
		if _, err := strconv.Atoi(tid); !ok || err != nil {
			return nil, fmt.Errorf("missing numeric tid: %q", line)
		}
		text = strings.TrimSpace(text)
		if strings.HasPrefix(text, "+++") || strings.HasPrefix(text, "---") || strings.HasPrefix(text, "execve(") || strings.HasPrefix(text, "<... execve resumed>") {
			continue
		}
		if start, split, found := strings.Cut(text, "<unfinished ...>"); found {
			if split != "" || pending[tid] != "" || (!strings.HasPrefix(start, "mkdir(") && !strings.HasPrefix(start, "mkdirat(")) {
				return nil, fmt.Errorf("unsupported split record: %q", line)
			}
			pending[tid] = start
			continue
		}
		if strings.HasPrefix(text, "<... ") {
			start := pending[tid]
			name, _, _ := strings.Cut(start, "(")
			marker := "<... " + name + " resumed>"
			if start == "" || !strings.HasPrefix(text, marker) {
				return nil, fmt.Errorf("unsupported resume: %q", line)
			}
			text = start + strings.TrimPrefix(text, marker)
			delete(pending, tid)
		}
		m := call.FindStringSubmatch(text)
		if m == nil {
			return nil, fmt.Errorf("unsupported mkdir trace record: %q", line)
		}
		var path, dirfd string
		if m[1] == "mkdir" {
			a := mkdir.FindStringSubmatch(strings.TrimSpace(m[2]))
			if a == nil {
				return nil, fmt.Errorf("unsupported mkdir operands: %q", line)
			}
			path = a[1]
		} else {
			a := mkdirat.FindStringSubmatch(strings.TrimSpace(m[2]))
			if a == nil {
				return nil, fmt.Errorf("unsupported mkdirat operands: %q", line)
			}
			dirfd, path = a[1], a[2]
		}
		p, err := strconv.Unquote(path)
		if err != nil || p == "" {
			return nil, fmt.Errorf("unsupported pathname: %q", line)
		}
		if !filepath.IsAbs(p) {
			a := fd.FindStringSubmatch(dirfd)
			if a == nil {
				return nil, fmt.Errorf("relative mkdir without annotated dirfd: %q", line)
			}
			base, err := strconv.Unquote(`"` + a[1] + `"`)
			if err != nil || !filepath.IsAbs(base) {
				return nil, fmt.Errorf("nonabsolute dirfd: %q", line)
			}
			p = filepath.Join(base, p)
		}
		paths = append(paths, filepath.Clean(p))
	}
	if len(pending) != 0 {
		return nil, fmt.Errorf("unfinished mkdir calls: %v", pending)
	}
	return paths, nil
}

func TestBundleDiagnosticMkdirObserver(t *testing.T) {
	paths, err := bundleDiagnosticMkdirPaths("12 mkdirat(AT_FDCWD</fixture>, \"record/transient\", 0700 <unfinished ...>\n12 <... mkdirat resumed>) = 0\n13 mkdir(\"/removed/transient\", 0700) = 0\n")
	require.NoError(t, err)
	assert.Equal(t, []string{"/fixture/record/transient", "/removed/transient"}, paths)
	for _, input := range []string{
		"12 mkdir(\"relative\", 0700) = 0\n",
		"12 mkdirat(AT_FDCWD, \"relative\", 0700) = 0\n",
		"12 mkdirat(GARBAGE</fixture>, \"relative\", 0700) = 0\n",
		"12 mkdir(\"/truncated\"..., 0700) = 0\n",
		"12 mkdir(\"/path\", GARBAGE) = 0\n",
		"12 mkdir(\"/path\", 0700 <unfinished ...>\n",
		"12 unknown(\"/path\") = 0\n",
	} {
		_, err := bundleDiagnosticMkdirPaths(input)
		assert.Error(t, err, input)
	}
}

func TestTeamBundleDiagnosticPhysicalPlacement(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("physical mkdir observation requires Linux strace")
	}
	require.NotEmpty(t, ethosBinary, "actual CLI build must succeed")
	strace, err := exec.LookPath("strace")
	require.NoError(t, err, "physical placement evidence requires strace")
	for _, linked := range []bool{false, true} {
		for _, mode := range []string{"blame", "full"} {
			t.Run(fmt.Sprintf("linked=%t/%s", linked, mode), func(t *testing.T) {
				base := bundleDiagnosticEnv(t)
				store := filepath.Join(base, "store")
				require.NoError(t, os.Mkdir(store, 0o700))
				bundleDiagnosticGit(t, store, "init", "-q")
				bundleDiagnosticGit(t, store, "commit", "--allow-empty", "-qm", "fixture")
				checkout := store
				if linked {
					checkout = filepath.Join(base, "checkout")
					bundleDiagnosticGit(t, store, "worktree", "add", "-qb", "linked", checkout)
					require.NotEqual(t, store, checkout)
				}
				for _, root := range []string{store, checkout} {
					require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs", "ethos"), 0o700))
				}
				require.NoError(t, os.WriteFile(filepath.Join(store, ".punt-labs", "ethos.yaml"), []byte("record: "+mode+"\n"), 0o600))
				record := filepath.Join(checkout, ".punt-labs", "ethos")
				url := filepath.Join(base, "nonexistent.git")
				require.NoFileExists(t, url)
				t.Setenv("TMPDIR", record)
				t.Setenv("TMP", record)
				t.Setenv("TEMP", record)
				trace := filepath.Join(base, "syscalls")
				cmd := exec.Command(strace, "-f", "-yy", "-s", "65535", "-o", trace, "-e", "trace=mkdir,mkdirat,execve", ethosBinary, "team", "add-bundle", "--apply", "--name", "missing", url)
				cmd.Dir, cmd.Env = checkout, os.Environ()
				out, err := cmd.CombinedOutput()
				var ee *exec.ExitError
				require.ErrorAs(t, err, &ee, "%s", out)
				assert.Equal(t, 1, ee.ExitCode(), "%s", out)
				assert.Contains(t, string(out), "running git submodule add "+url)
				assert.Contains(t, string(out), "exit status 128")
				assert.Contains(t, string(out), "does not exist")
				assert.NotContains(t, string(out), "ssh-add")
				assert.NotContains(t, string(out), "added bundle")
				calls, err := os.ReadFile(trace)
				require.NoError(t, err)
				assert.Contains(t, string(calls), `"ls-remote", "--exit-code", `+strconv.Quote(url)+`, "HEAD"`, "actual diagnostic must execute")
				t.Logf("S=%s C=%s record=%s mode=%s; raw mkdir/exec trace:\n%s", store, checkout, record, mode, calls)
				paths, err := bundleDiagnosticMkdirPaths(string(calls))
				require.NoError(t, err)
				neutralCount := 0
				for _, p := range paths {
					// Only the intended submodule target and its Git module object
					// directories may be made. TMPDIR is NOT an allowed destination.
					target := filepath.Join(store, ".punt-labs", "ethos-bundles", "missing")
					module := filepath.Join(store, ".git", "modules", ".punt-labs", "ethos-bundles", "missing")
					allowed := p == filepath.Join(store, ".punt-labs") || p == filepath.Dir(target) || p == target || strings.HasPrefix(p, target+string(filepath.Separator))
					for ancestor := module; ancestor != filepath.Join(store, ".git"); ancestor = filepath.Dir(ancestor) {
						allowed = allowed || p == ancestor
					}
					allowed = allowed || strings.HasPrefix(p, module+string(filepath.Separator))
					local := filepath.Join(store, ".punt-labs", "local", "ethos")
					allowed = allowed || p == filepath.Dir(local) || p == local || p == filepath.Join(local, "tmp")
					neutral := filepath.Dir(p) == filepath.Join(local, "tmp") && strings.HasPrefix(filepath.Base(p), "ethos-git-") && strings.HasSuffix(p, ".ethos-tmp")
					if neutral {
						neutralCount++
					}
					allowed = allowed || neutral
					assert.True(t, allowed, "unexpected attempted mkdir destination %s (S=%s C=%s)", p, store, checkout)
				}
				assert.Equal(t, 1, neutralCount, "exactly one fresh diagnostic cwd must be placed under captured S, including when S != C")
			})
		}
	}
}

// The SSH executable is a local fixture, not ssh: no socket/remote is opened.
// Actual Git must resolve includes, insteadOf and sshCommand itself, with only
// system/global/command config, never the invoking checkout's repository config.
func TestTeamBundleDiagnosticUserConfig(t *testing.T) {
	for _, source := range []string{"system", "include", "global-after-include", "count", "parameters"} {
		t.Run(source, func(t *testing.T) {
			base := bundleDiagnosticEnv(t)
			t.Setenv("GIT_ALLOW_PROTOCOL", "ssh")
			t.Setenv("GIT_SSH_VARIANT", "ssh")
			repo := filepath.Join(base, "repo")
			require.NoError(t, os.Mkdir(repo, 0o700))
			bundleDiagnosticGit(t, repo, "init", "-q")
			log := filepath.Join(base, "ssh.log")
			ssh := func(label string) string {
				p := filepath.Join(base, label+"-ssh")
				script := "#!/bin/sh\n" +
					"[ \"$GIT_TERMINAL_PROMPT\" = 0 ] && [ -z \"$GIT_ASKPASS\" ] && [ -z \"$SSH_ASKPASS\" ] && [ \"$SSH_ASKPASS_REQUIRE\" = never ] || exit 90\n" +
					"printf '%s\\n' '" + label + "' \"$*\" >> '" + log + "'\n" +
					"echo 'Permission denied (publickey).' >&2\nexit 128\n"
				require.NoError(t, os.WriteFile(p, []byte(script), 0o700))
				return p
			}
			systemSSH, includeSSH, globalSSH := ssh("system"), ssh("include"), ssh("global-after-include")
			countSSH, parametersSSH, repoSSH := ssh("count"), ssh("parameters"), ssh("repository-must-not-run")
			system := filepath.Join(base, "system")
			included := filepath.Join(base, "included")
			global := filepath.Join(base, "global")
			sys := "[core]\n sshCommand = " + systemSSH + "\n[url \"ssh://git@fixture.invalid/system/\"]\n insteadOf = diagnostic:\n"
			inc := "[core]\n sshCommand = " + includeSSH + "\n[url \"ssh://git@fixture.invalid/included/\"]\n insteadOf = diagnostic:team/\n"
			require.NoError(t, os.WriteFile(system, []byte(sys), 0o600))
			require.NoError(t, os.WriteFile(included, []byte(inc), 0o600))
			cfg := ""
			if source != "system" {
				cfg = "[core]\n sshCommand = " + globalSSH + "\n[include]\n path = included\n"
			}
			if source == "global-after-include" {
				cfg += "[core]\n sshCommand = " + globalSSH + "\n"
			}
			require.NoError(t, os.WriteFile(global, []byte(cfg), 0o600))
			bundleDiagnosticGit(t, repo, "config", "core.sshCommand", repoSSH)
			bundleDiagnosticGit(t, repo, "config", "url.ssh://git@fixture.invalid/repository/.insteadOf", "diagnostic:team/b")
			t.Setenv("GIT_CONFIG_SYSTEM", system)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
			t.Setenv("GIT_CONFIG_GLOBAL", global)
			if source == "count" || source == "parameters" {
				t.Setenv("GIT_CONFIG_COUNT", "1")
				t.Setenv("GIT_CONFIG_KEY_0", "core.sshCommand")
				t.Setenv("GIT_CONFIG_VALUE_0", countSSH)
			}
			if source == "parameters" {
				t.Setenv("GIT_CONFIG_PARAMETERS", "'core.sshCommand="+parametersSSH+"'")
			}
			// A non-directory TMPDIR proves this diagnostic uses only its
			// explicitly captured local scratch, never ambient temporary storage.
			blocked := filepath.Join(base, "blocked")
			require.NoError(t, os.WriteFile(blocked, []byte("fixture"), 0o600))
			t.Setenv("TMPDIR", blocked)
			t.Chdir(repo)
			beforeEnv, beforeCWD := os.Environ(), repo
			roots := resolve.Located{Store: repo, Checkout: repo}
			require.True(t, sshHint("diagnostic:team/b.git", &roots), "actual Git must reach the fixture SSH")
			got, err := os.ReadFile(log)
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(string(got), source+"\n"), "%s", got)
			remote := "/included/b.git"
			if source == "system" {
				remote = "/system/team/b.git"
			}
			assert.Contains(t, string(got), remote)
			assert.NotContains(t, string(got), "repository-must-not-run")
			assert.Equal(t, beforeEnv, os.Environ())
			cwd, err := os.Getwd()
			require.NoError(t, err)
			assert.Equal(t, beforeCWD, cwd)
		})
	}
}

func TestTeamBundleDiagnosticNoBogusHint(t *testing.T) {
	base := bundleDiagnosticEnv(t)
	url := filepath.Join(base, "missing.git")
	for _, shape := range []string{"outside", "repository", "broken-gitfile", "dangling-dotgit", "malformed-config", "missing-git"} {
		t.Run(shape, func(t *testing.T) {
			cwd := t.TempDir()
			switch shape {
			case "repository":
				bundleDiagnosticGit(t, cwd, "init", "-q")
			case "broken-gitfile":
				require.NoError(t, os.WriteFile(filepath.Join(cwd, ".git"), []byte("gitdir: missing\n"), 0o600))
			case "dangling-dotgit":
				require.NoError(t, os.Symlink(filepath.Join(cwd, "missing"), filepath.Join(cwd, ".git")))
			case "malformed-config":
				cfg := filepath.Join(cwd, "invalid-config")
				require.NoError(t, os.WriteFile(cfg, []byte("[invalid\n"), 0o600))
				t.Setenv("GIT_CONFIG_GLOBAL", cfg)
			case "missing-git":
				t.Setenv("PATH", cwd)
			}
			t.Chdir(cwd)
			assert.False(t, sshHint(url, nil))
		})
	}
}

func TestTeamBundleDiagnosticFailurePreservesCloneError(t *testing.T) {
	for _, scenario := range []string{"failure", "success", "timeout", "missing-git", "relative-dot", "relative-parent"} {
		t.Run(scenario, func(t *testing.T) {
			base := bundleDiagnosticEnv(t)
			bundleFlags(t, true)
			t.Chdir(base)
			log := filepath.Join(base, "diagnostic.log")
			git := filepath.Join(base, "git")
			clone := "echo 'original clone failure' >&2; exit 42"
			probe := "exit 99"
			switch scenario {
			case "success":
				probe = "exit 0"
			case "timeout":
				// Even SSH-looking text emitted before a timeout is not a
				// completed Git diagnostic and must not produce advice.
				probe = "echo 'Permission denied (publickey).' >&2; exec /bin/sleep 30"
				old := hintLimit
				hintLimit = 100 * time.Millisecond
				t.Cleanup(func() { hintLimit = old })
			case "missing-git":
				clone = "/bin/rm -- '" + git + "'; " + clone
			}
			script := "#!/bin/sh\nwhile [ \"$1\" = -c ]; do shift 2; done\ncase \"$1\" in\nclone) " + clone + ";;\nls-remote) echo called >> '" + log + "'; " + probe + ";;\nesac\nexit 98\n"
			require.NoError(t, os.WriteFile(git, []byte(script), 0o700))
			t.Setenv("PATH", base)
			url := filepath.Join(base, "missing.git")
			if scenario == "relative-dot" {
				url = "./missing.git"
			}
			if scenario == "relative-parent" {
				url = "../missing.git"
			}
			start := time.Now()
			stderr, err := runAddBundle(t, url)
			var ee *exec.ExitError
			require.ErrorAs(t, err, &ee)
			assert.Equal(t, 42, ee.ExitCode(), "retain the original clone error, not the probe's result")
			assert.Contains(t, stderr, "original clone failure")
			assert.NotContains(t, err.Error(), "ssh-add")
			assert.Less(t, time.Since(start), 5*time.Second, "hintLimit must bound the diagnostic")
			if scenario == "missing-git" || strings.HasPrefix(scenario, "relative-") {
				assert.NoFileExists(t, log)
			} else {
				assert.FileExists(t, log, "diagnostic actually executed")
			}
		})
	}
}

func TestTeamBundleDiagnosticGlobalStandalone(t *testing.T) {
	base := bundleDiagnosticEnv(t)
	require.NotEmpty(t, ethosBinary)
	url := filepath.Join(base, "missing.git")
	cmd := exec.Command(ethosBinary, "team", "add-bundle", "--global", "--apply", url)
	cmd.Dir, cmd.Env = base, os.Environ()
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee, "%s", out)
	assert.Equal(t, 1, ee.ExitCode())
	assert.Contains(t, string(out), "running git clone "+url)
	assert.Contains(t, string(out), "exit status 128")
	assert.NotContains(t, string(out), "ssh-add")
	assert.NotContains(t, string(out), "added bundle")
}

func TestTeamBundleDiagnosticConcurrent(t *testing.T) {
	base := bundleDiagnosticEnv(t)
	t.Chdir(base)
	url := filepath.Join(base, "missing.git")
	before := os.Environ()
	var wg sync.WaitGroup
	results := make(chan bool, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			roots := resolve.Located{Store: base, Checkout: base}
			results <- sshHint(url, &roots)
		}()
	}
	wg.Wait()
	close(results)
	for got := range results {
		assert.False(t, got)
	}
	assert.Equal(t, before, os.Environ())
}

func TestTeamBundleDiagnosticRelativeSSHStaysNeutral(t *testing.T) {
	base := bundleDiagnosticEnv(t)
	t.Setenv("GIT_ALLOW_PROTOCOL", "ssh")
	t.Setenv("GIT_SSH_VARIANT", "ssh")
	marker := filepath.Join(base, "must-not-execute")
	require.NoError(t, os.WriteFile(filepath.Join(base, "relative-ssh"), []byte("#!/bin/sh\necho called > '"+marker+"'\necho 'Permission denied (publickey).' >&2\nexit 128\n"), 0o700))
	cfg := filepath.Join(base, "global")
	require.NoError(t, os.WriteFile(cfg, []byte("[core]\n sshCommand = ./relative-ssh\n"), 0o600))
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Chdir(base)
	roots := resolve.Located{Store: base, Checkout: base}
	assert.False(t, sshHint("ssh://git@fixture.invalid/b.git", &roots))
	assert.NoFileExists(t, marker, "the diagnostic must not run relative SSH executables from C")
}

func TestTeamBundleDiagnosticUnavailableLocal(t *testing.T) {
	for _, shape := range []string{"obstructed", "symlink"} {
		t.Run(shape, func(t *testing.T) {
			base := bundleDiagnosticEnv(t)
			store := filepath.Join(base, "store")
			require.NoError(t, os.Mkdir(store, 0o700))
			bundleDiagnosticGit(t, store, "init", "-q")
			bundleDiagnosticGit(t, store, "commit", "--allow-empty", "-qm", "fixture")
			record := filepath.Join(store, ".punt-labs", "ethos")
			require.NoError(t, os.MkdirAll(record, 0o700))
			local := filepath.Join(store, ".punt-labs", "local")
			if shape == "symlink" {
				require.NoError(t, os.Symlink(record, local))
			} else {
				require.NoError(t, os.WriteFile(local, []byte("obstruction"), 0o600))
			}
			t.Setenv("TMPDIR", record)
			url := filepath.Join(base, "missing.git")
			cmd := exec.Command(ethosBinary, "team", "add-bundle", "--apply", url)
			trace := filepath.Join(base, "syscalls")
			if runtime.GOOS == "linux" {
				strace, err := exec.LookPath("strace")
				require.NoError(t, err)
				cmd = exec.Command(strace, "-f", "-yy", "-s", "65535", "-o", trace, "-e", "trace=mkdir,mkdirat,execve", ethosBinary, "team", "add-bundle", "--apply", url)
			}
			cmd.Dir, cmd.Env = store, os.Environ()
			out, err := cmd.CombinedOutput()
			var ee *exec.ExitError
			require.ErrorAs(t, err, &ee, "%s", out)
			assert.Equal(t, 1, ee.ExitCode())
			assert.Contains(t, string(out), "running git submodule add "+url)
			assert.Contains(t, string(out), "exit status 128")
			assert.NotContains(t, string(out), "ssh-add")
			assert.NotContains(t, string(out), "added bundle")
			entries, err := os.ReadDir(record)
			require.NoError(t, err)
			assert.Empty(t, entries, "never fall back to ambient TMPDIR on a refused local directory")
			if runtime.GOOS == "linux" {
				calls, err := os.ReadFile(trace)
				require.NoError(t, err)
				t.Logf("refused %s local path at %s; raw mkdir/exec trace:\n%s", shape, local, calls)
				assert.NotContains(t, string(calls), `"ls-remote"`, "a refused local context cannot fall back to a global diagnostic")
				paths, err := bundleDiagnosticMkdirPaths(string(calls))
				require.NoError(t, err)
				for _, p := range paths {
					assert.False(t, p == record || strings.HasPrefix(p, record+string(filepath.Separator)), p)
					assert.False(t, p == local || strings.HasPrefix(p, local+string(filepath.Separator)), p)
				}
			}
		})
	}
	base := bundleDiagnosticEnv(t)
	roots := resolve.Located{Store: base, Checkout: base, Config: &resolve.RepoConfig{Record: "invalid"}}
	assert.False(t, sshHint(filepath.Join(base, "missing.git"), &roots), "invalid captured policy refuses only the optional hint")
}

func bundleDiagnosticSeparatorFixture(t *testing.T, storeSeparator, linked, checkoutSeparator bool) (base, store, checkout string) {
	t.Helper()
	base = bundleDiagnosticEnv(t)
	storeName := "store"
	if storeSeparator {
		storeName += string(os.PathListSeparator) + "separator"
	}
	store = filepath.Join(base, storeName)
	require.NoError(t, os.Mkdir(store, 0o700))
	bundleDiagnosticGit(t, store, "init", "-q")
	bundleDiagnosticGit(t, store, "commit", "--allow-empty", "-qm", "fixture")
	checkout = store
	if linked {
		checkoutName := "checkout"
		if checkoutSeparator {
			checkoutName += string(os.PathListSeparator) + "separator"
		}
		checkout = filepath.Join(base, checkoutName)
		bundleDiagnosticGit(t, store, "worktree", "add", "-qb", "linked", checkout)
		require.NotEqual(t, store, checkout)
	}
	for _, root := range []string{store, checkout} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs", "ethos"), 0o700))
	}
	return base, store, checkout
}

// A ceiling is a path-list, not an opaque pathname. Separator-bearing S must
// decline before allocation/probing. A separator in C alone must not disable a
// probe whose S-local ceiling is representable. All SSH programs are offline
// scripts: repository-selected execution is observable without using a socket.
func TestTeamBundleDiagnosticCeilingSeparator(t *testing.T) {
	for _, shape := range []struct {
		name                                      string
		storeSeparator, linked, checkoutSeparator bool
	}{
		{name: "ordinary-safe"},
		{name: "ordinary-store-separator", storeSeparator: true},
		{name: "linked-store-separator", storeSeparator: true, linked: true},
		{name: "linked-checkout-only-separator", linked: true, checkoutSeparator: true},
	} {
		for _, mode := range []string{"blame", "full"} {
			t.Run(shape.name+"/"+mode, func(t *testing.T) {
				base, store, checkout := bundleDiagnosticSeparatorFixture(t, shape.storeSeparator, shape.linked, shape.checkoutSeparator)
				t.Setenv("GIT_ALLOW_PROTOCOL", "ssh")
				t.Setenv("GIT_SSH_VARIANT", "ssh")
				log := filepath.Join(base, "ssh.log")
				ssh := func(label string) string {
					path := filepath.Join(base, label)
					script := "#!/bin/sh\n" +
						"[ \"$GIT_TERMINAL_PROMPT\" = 0 ] && [ -z \"$GIT_ASKPASS\" ] && [ -z \"$SSH_ASKPASS\" ] && [ \"$SSH_ASKPASS_REQUIRE\" = never ] || exit 90\n" +
						"printf '%s\\n' '" + label + "' \"$*\" >> '" + log + "'\n" +
						"echo 'Permission denied (publickey).' >&2\nexit 128\n"
					require.NoError(t, os.WriteFile(path, []byte(script), 0o700))
					return path
				}
				globalSSH, repositorySSH := ssh("global-ssh"), ssh("repository-must-not-run")
				global := filepath.Join(base, "global")
				require.NoError(t, os.WriteFile(global, []byte("[core]\n sshCommand = "+globalSSH+"\n[url \"ssh://git@fixture.invalid/global/\"]\n insteadOf = ceiling:\n"), 0o600))
				bundleDiagnosticGit(t, store, "config", "core.sshCommand", repositorySSH)
				bundleDiagnosticGit(t, store, "config", "url.ssh://git@fixture.invalid/repository/.insteadOf", "ceiling:team/")
				t.Setenv("GIT_CONFIG_GLOBAL", global)
				config := filepath.Join(store, ".punt-labs", "ethos.yaml")
				payload := filepath.Join(store, ".punt-labs", "ethos", "payload.yaml")
				require.NoError(t, os.WriteFile(config, []byte("record: "+mode+"\n"), 0o600))
				require.NoError(t, os.WriteFile(payload, []byte("fixture: unchanged\n"), 0o600))
				before := map[string][]byte{}
				for _, path := range []string{global, config, payload, filepath.Join(store, ".git", "config")} {
					data, err := os.ReadFile(path)
					require.NoError(t, err)
					before[path] = data
				}
				t.Setenv("TMPDIR", filepath.Join(checkout, ".punt-labs", "ethos"))
				t.Chdir(checkout)
				roots := resolve.Located{Store: store, Checkout: checkout, Config: &resolve.RepoConfig{Record: mode}}
				policy, err := roots.Layout()
				require.NoError(t, err)
				require.Equal(t, shape.storeSeparator, strings.ContainsRune(policy.TempDir(), os.PathListSeparator))
				beforeEnv := os.Environ()
				hint := sshHint("ceiling:team/b.git", &roots)
				got, readErr := os.ReadFile(log)
				t.Logf("S=%s C=%s mode=%s separator=%q hint=%t helper=%q readErr=%v", store, checkout, mode, os.PathListSeparator, hint, got, readErr)
				assert.NotContains(t, string(got), "repository-must-not-run")
				assert.NotContains(t, string(got), "/repository/")
				if shape.storeSeparator {
					assert.False(t, hint, "an unrepresentable ceiling declines the optional diagnostic")
					assert.ErrorIs(t, readErr, os.ErrNotExist, "no SSH helper may execute")
					assert.NoDirExists(t, filepath.Join(store, ".punt-labs", "local"), "refuse before local allocation")
				} else {
					require.NoError(t, readErr)
					assert.True(t, hint, "safe S must still run the user-config probe, including separator-bearing C")
					assert.True(t, strings.HasPrefix(string(got), "global-ssh\n"), "%s", got)
					assert.Contains(t, string(got), "/global/team/b.git")
				}
				for path, data := range before {
					after, err := os.ReadFile(path)
					require.NoError(t, err)
					assert.Equal(t, data, after, "diagnostics must not modify %s", path)
				}
				assert.Equal(t, beforeEnv, os.Environ())
				cwd, err := os.Getwd()
				require.NoError(t, err)
				assert.Equal(t, checkout, cwd)
			})
		}
	}
}

func TestTeamBundleDiagnosticSeparatorFailure(t *testing.T) {
	for _, linked := range []bool{false, true} {
		for _, mode := range []string{"blame", "full"} {
			t.Run(fmt.Sprintf("linked=%t/%s", linked, mode), func(t *testing.T) {
				base, store, checkout := bundleDiagnosticSeparatorFixture(t, true, linked, false)
				require.NoError(t, os.WriteFile(filepath.Join(store, ".punt-labs", "ethos.yaml"), []byte("record: "+mode+"\n"), 0o600))
				record := filepath.Join(checkout, ".punt-labs", "ethos")
				t.Setenv("TMPDIR", record)
				t.Setenv("TMP", record)
				t.Setenv("TEMP", record)
				url := filepath.Join(base, "nonexistent.git")
				require.NoFileExists(t, url)
				require.NotEmpty(t, ethosBinary)
				cmd := exec.Command(ethosBinary, "team", "add-bundle", "--apply", "--name", "missing", url)
				trace := filepath.Join(base, "syscalls")
				if runtime.GOOS == "linux" {
					strace, err := exec.LookPath("strace")
					require.NoError(t, err)
					cmd = exec.Command(strace, "-f", "-yy", "-s", "65535", "-o", trace, "-e", "trace=mkdir,mkdirat,execve", ethosBinary, "team", "add-bundle", "--apply", "--name", "missing", url)
				}
				cmd.Dir, cmd.Env = checkout, os.Environ()
				out, err := cmd.CombinedOutput()
				var ee *exec.ExitError
				require.ErrorAs(t, err, &ee, "%s", out)
				assert.Equal(t, 1, ee.ExitCode())
				assert.Contains(t, string(out), "running git submodule add "+url)
				assert.Contains(t, string(out), "exit status 128")
				assert.Contains(t, string(out), "does not exist")
				assert.NotContains(t, string(out), "ssh-add")
				assert.NotContains(t, string(out), "added bundle")
				local := filepath.Join(store, ".punt-labs", "local")
				assert.NoDirExists(t, local)
				entries, err := os.ReadDir(record)
				require.NoError(t, err)
				assert.Empty(t, entries)
				if runtime.GOOS == "linux" {
					calls, err := os.ReadFile(trace)
					require.NoError(t, err)
					t.Logf("S=%s C=%s mode=%s; separator refusal raw mkdir/exec trace:\n%s", store, checkout, mode, calls)
					assert.NotContains(t, string(calls), `"ls-remote"`, "decline before diagnostic execution, not just before SSH")
					paths, err := bundleDiagnosticMkdirPaths(string(calls))
					require.NoError(t, err)
					for _, path := range paths {
						assert.False(t, path == local || strings.HasPrefix(path, local+string(filepath.Separator)), path)
						assert.False(t, path == record || strings.HasPrefix(path, record+string(filepath.Separator)), path)
					}
				}
			})
		}
	}
}
