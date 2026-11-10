//go:build linux || darwin

package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

// Seed through the real PostToolUse CLI entry point, not a fabricated archive
// or a legacy line without prospective eligibility. The installed hook must
// subsequently archive these exact retained bytes.
func seedInstalledAudit(t *testing.T, store, checkout string, env []string) []byte {
	t.Helper()
	cmd := exec.Command(filepath.Join(ethosBinDir(t), "ethos"), "hook", "audit-log")
	cmd.Dir, cmd.Env = checkout, env
	cmd.Stdin = strings.NewReader(`{"session_id":"` + sessionID + `","tool_name":"Read","delegation_id":"d-2026-11-04-043","contract_id":"m-2026-11-04-043","tool_input":{"file_path":"example.go"}}`)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "production append: %s", out)
	live, err := os.ReadFile(filepath.Join(store, ".punt-labs", "local", "ethos", "sessions", sessionID+".audit.jsonl"))
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(live), "\n"), "one actual production entry")
	require.Contains(t, string(live), `"tool":"Read"`)
	return live
}

func assertInstalledArchive(t *testing.T, store string, live []byte) string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(store, ".punt-labs", "local", "ethos", "trace", "sessions", "*", "audit-*.jsonl"))
	require.NoError(t, err)
	require.Len(t, paths, 1, "must have actually archived the pending entry")
	data, err := os.ReadFile(paths[0])
	require.NoError(t, err)
	assert.Equal(t, live, data, "local archival retains exact guarded/stamped bytes")
	return paths[0]
}

func archivalGitOutput(t *testing.T, checkout string, env []string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(string(archivalGitBytes(t, checkout, env, args...)))
}

func archivalGitBytes(t *testing.T, checkout string, env []string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = checkout, env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "fixture git %v: %s", args, stderr.String())
	return out
}

type archivalIndexEntry struct {
	mode, oid, stage, path string
}

func parseArchivalIndex(data []byte) (map[string]archivalIndexEntry, error) {
	entries := map[string]archivalIndexEntry{}
	if len(data) == 0 {
		return entries, nil
	}
	if data[len(data)-1] != 0 {
		return nil, fmt.Errorf("unterminated ls-files -z record")
	}
	for _, record := range bytes.Split(data[:len(data)-1], []byte{0}) {
		header, path, ok := bytes.Cut(record, []byte{'\t'})
		fields := strings.Fields(string(header))
		if !ok || len(path) == 0 || len(fields) != 3 {
			return nil, fmt.Errorf("malformed ls-files --stage -z record %q", record)
		}
		if _, exists := entries[string(path)]; exists {
			return nil, fmt.Errorf("multiple stages/entries for %q", path)
		}
		entries[string(path)] = archivalIndexEntry{mode: fields[0], oid: fields[1], stage: fields[2], path: string(path)}
	}
	return entries, nil
}

func archivalIndexEntries(t *testing.T, checkout string, env []string, paths ...string) map[string]archivalIndexEntry {
	t.Helper()
	args := append([]string{"--literal-pathspecs", "ls-files", "--stage", "-z", "--"}, paths...)
	entries, err := parseArchivalIndex(archivalGitBytes(t, checkout, env, args...))
	require.NoError(t, err)
	return entries
}

func installedAuditPaths(t *testing.T, store, checkout string, live []byte) (string, string) {
	t.Helper()
	var entry struct {
		TS string `json:"ts"`
	}
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(live), &entry))
	ts, err := time.Parse(time.RFC3339Nano, entry.TS)
	require.NoError(t, err)
	// Independently compute the one-entry immutable identity. Do not discover
	// allowed destinations by globbing whatever a potentially faulty writer left.
	rel := filepath.Join("sessions", ts.UTC().Format("2006-01-02")+"-"+sessionID, fmt.Sprintf("audit-%019d-%019d.jsonl", ts.UnixNano(), ts.UnixNano()))
	return filepath.Join(store, ".punt-labs/local/ethos/trace", rel), filepath.Join(checkout, ".punt-labs/ethos", rel)
}

func traceInstalledArchive(t *testing.T, strace, hook, checkout string, env []string, inject string, policy archivalTracePolicy) ([]byte, archivalTraceSummary, string, error) {
	t.Helper()
	trace := filepath.Join(t.TempDir(), "syscalls")
	args := []string{"-f", "-yy", "-s", "0", "-o", trace, "-e", "trace=%file,fsync,fchdir,clone,clone3,fork,vfork,exit_group"}
	// Let strace drain sibling syscall stops before exit_group destroys their
	// register state. Keep every selected call, including interrupted installs;
	// an undecodable record still fails the unchanged observer.
	args = append(args, "-e", "inject=exit_group:delay_enter=100ms")
	if inject != "" {
		args = append(args, "-e", "inject="+inject)
	}
	args = append(args, hook)
	cmd := exec.Command(strace, args...)
	cmd.Dir = checkout
	// Even an adversarial TMPDIR cannot authorize record-adjacent stages.
	cmd.Env = append(env, "TMPDIR="+filepath.Join(checkout, ".punt-labs", "ethos"))
	out, hookErr := cmd.CombinedOutput()
	calls, err := os.ReadFile(trace)
	require.NoError(t, err)
	summary, placementErr := checkArchivalTrace(string(calls), policy)
	assert.NoError(t, placementErr, "classify namespace destination operands, not just creates")
	// Keep the exact trace in the test receipt, including removed intermediates
	// and resumed calls. Retries use this same observer and policy.
	t.Logf("installed archival inject=%q trace:\n%s", inject, calls)
	return out, summary, string(calls), hookErr
}

func assertArchivalPublication(t *testing.T, store, checkout string, env []string, mode string, live, workingSource, rawIndex []byte, source archivalIndexEntry, policy archivalTracePolicy) {
	t.Helper()
	retained, err := os.ReadFile(filepath.Join(policy.local, "sessions", sessionID+".audit.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, live, retained, "successful seal/retry retains live source bytes")
	assert.Equal(t, policy.archive, assertInstalledArchive(t, store, live))
	working, err := os.ReadFile(filepath.Join(checkout, source.path))
	require.NoError(t, err)
	assert.Equal(t, workingSource, working, "unstaged source B must remain unchanged")
	entries := archivalIndexEntries(t, checkout, env)
	assert.Equal(t, source, entries[source.path], "unrelated staged entry tuple must remain exact (mode, OID, stage, path); do not restage B")
	if mode == "full" {
		rel, err := filepath.Rel(checkout, policy.published)
		require.NoError(t, err)
		rel = filepath.ToSlash(rel)
		require.Len(t, entries, 2, "only eligible publication plus unrelated staged source")
		publication, ok := entries[rel]
		require.True(t, ok, "publication must be in actual C index")
		assert.Equal(t, "100644", publication.mode, "publication is a regular non-executable blob")
		assert.Equal(t, "0", publication.stage, "publication is not an unmerged entry")
		assert.Equal(t, live, archivalGitBytes(t, checkout, env, "cat-file", "blob", publication.oid), "indexed publication blob bytes must match eligible retained audit")
		data, err := os.ReadFile(policy.published)
		require.NoError(t, err)
		assert.Equal(t, live, data)
	} else {
		after, err := os.ReadFile(policy.index)
		require.NoError(t, err)
		assert.Equal(t, rawIndex, after, "blame seal/retry must preserve raw actual index bytes")
		assert.Len(t, entries, 1)
		assert.NoFileExists(t, policy.published)
	}
}

func TestArchivalIndexEntryBytes(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	// -z and literal pathspecs must preserve tabs, newlines, leading pathspec
	// syntax and non-UTF-8 filename bytes, not trim or C-quote them.
	testhelpers.RequireNonUTF8Names(t)
	path := ":(glob)odd\tname\n\xff"
	writeFile(t, filepath.Join(repo, path), "staged A\n")
	runGit(t, repo, "--literal-pathspecs", "add", "--", path)
	env := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	entries := archivalIndexEntries(t, repo, env, path)
	require.Len(t, entries, 1)
	entry := entries[path]
	assert.Equal(t, path, entry.path)
	assert.Equal(t, "100644", entry.mode)
	assert.Equal(t, "0", entry.stage)
	writeFile(t, filepath.Join(repo, path), "unstaged B\n")
	assert.Equal(t, entries, archivalIndexEntries(t, repo, env, path))
	assert.Equal(t, []byte("staged A\n"), archivalGitBytes(t, repo, env, "cat-file", "blob", entry.oid))
	for _, data := range [][]byte{[]byte("100644 abc 0\tpath"), []byte("missing header\x00"), []byte("100644 abc 0\tp\x00100644 abc 1\tp\x00")} {
		_, err := parseArchivalIndex(data)
		assert.Error(t, err)
	}
}

// This is Linux syscall evidence, not a post-exit walk presented as transient
// safety. Blame only archives in S-local. Full additionally installs eligible
// final copies in C and uses C's actual index lock plus Git-owned object files;
// those legitimate writes must not be mislabeled as forbidden Ethos staging.
func TestEnabledInstalledPreCommitArchivalPlacement(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("archival syscall placement/fault acceptance requires Linux strace")
	}
	strace, err := exec.LookPath("strace")
	if err != nil {
		t.Skip("archival transient acceptance requires strace; not established")
	}
	for _, linked := range []bool{false, true} {
		for _, mode := range []string{"blame", "full"} {
			for _, fault := range []struct{ name, inject, diagnostic string }{
				{"success", "", ""},
				{"cross-device install", "linkat:error=EXDEV", "cross-device"},
				{"stage sync failure", "fsync:error=EIO", "persisting audit staging file"},
				{"interrupted install", "linkat:error=EINTR:signal=SIGKILL", ""},
			} {
				t.Run(strings.Join([]string{map[bool]string{false: "ordinary", true: "linked"}[linked], mode, fault.name}, "/"), func(t *testing.T) {
					store, checkout, hooksDir, env := enabledMessageRepo(t, linked)
					writeFile(t, filepath.Join(store, ".punt-labs", "ethos.yaml"), "record: "+mode+"\n")
					writeFile(t, filepath.Join(checkout, "source"), "unrelated staged A\n")
					runGit(t, checkout, "add", "source")
					source := archivalIndexEntries(t, checkout, env, "source")["source"]
					require.Equal(t, "100644", source.mode)
					require.Equal(t, "0", source.stage)
					workingSource := []byte("unrelated unstaged B\n")
					writeFile(t, filepath.Join(checkout, "source"), string(workingSource))
					index := archivalGitOutput(t, checkout, env, "rev-parse", "--git-path", "index")
					if !filepath.IsAbs(index) {
						index = filepath.Join(checkout, index)
					}
					before, err := os.ReadFile(index)
					require.NoError(t, err)
					live := seedInstalledAudit(t, store, checkout, env)
					require.Contains(t, string(live), `"record":"`+mode+`"`)
					local := filepath.Join(store, ".punt-labs", "local", "ethos")
					archive, published := installedAuditPaths(t, store, checkout, live)
					policy := archivalTracePolicy{checkout: checkout, local: local, archive: archive, published: published, index: index, objects: filepath.Join(store, ".git", "objects"), global: archivalGlobalRoot(env), full: mode == "full"}
					hook := filepath.Join(hooksDir, "pre-commit")
					out, summary, calls, hookErr := traceInstalledArchive(t, strace, hook, checkout, env, fault.inject, policy)
					interrupted := fault.name == "interrupted install"
					if fault.inject != "" && (mode == "full" || interrupted) {
						var exit *exec.ExitError
						require.ErrorAs(t, hookErr, &exit, "%s", out)
						assert.Equal(t, 2, exit.ExitCode(), "installed shell reports the failed helper honestly")
					} else {
						require.NoError(t, hookErr, "%s", out)
						if fault.inject != "" {
							assert.Contains(t, string(out), "retained-data warning")
						}
					}
					if fault.diagnostic != "" {
						assert.Contains(t, string(out), fault.diagnostic)
					}
					retained, err := os.ReadFile(filepath.Join(local, "sessions", sessionID+".audit.jsonl"))
					require.NoError(t, err)
					assert.Equal(t, live, retained, "failure/interruption must retain the raw source")
					assert.True(t, summary.stageCreated, "must reach actual chunk staging on every case")
					if fault.name != "stage sync failure" {
						assert.True(t, summary.archiveInstall, "must reach archival install, even for injected errors")
					}
					if interrupted {
						assert.Contains(t, string(calls), "killed by SIGKILL", "uncatchable interruption actually reached")
					} else if fault.inject != "" {
						assert.Contains(t, string(calls), "(INJECTED)")
					}
					after, err := os.ReadFile(index)
					require.NoError(t, err)
					publications, err := filepath.Glob(filepath.Join(checkout, ".punt-labs", "ethos", "sessions", "*", "audit-*.jsonl"))
					require.NoError(t, err)
					if fault.inject == "" {
						assertArchivalPublication(t, store, checkout, env, mode, live, workingSource, before, source, policy)
					}
					if mode == "full" && fault.inject == "" {
						assert.Equal(t, []string{published}, publications)
						assert.True(t, summary.indexLocked, "full publication reaches the actual C index lock")
						assert.True(t, summary.publicationInstall, "full publication reaches the exact eligible final destination")
					} else {
						assert.Equal(t, before, after, "blame and pre-publication failures preserve actual index bytes")
						assert.Empty(t, publications)
						assert.False(t, summary.indexLocked, "no publication attempted before archival succeeds")
					}
					if fault.inject != "" {
						assert.NoFileExists(t, archive, "pre-install fault has not completed archival")
						working, err := os.ReadFile(filepath.Join(checkout, "source"))
						require.NoError(t, err)
						assert.Equal(t, workingSource, working)
						assert.Equal(t, source, archivalIndexEntries(t, checkout, env, "source")["source"])
						out, retrySummary, _, retryErr := traceInstalledArchive(t, strace, hook, checkout, env, "", policy)
						require.NoError(t, retryErr, "retry: %s", out)
						assert.True(t, retrySummary.stageCreated)
						assert.True(t, retrySummary.archiveInstall)
						assert.Equal(t, mode == "full", retrySummary.indexLocked)
						assert.Equal(t, mode == "full", retrySummary.publicationInstall)
						assertArchivalPublication(t, store, checkout, env, mode, live, workingSource, before, source, policy)
					}
					if linked {
						assert.NoDirExists(t, filepath.Join(checkout, ".punt-labs", "local"))
					}
				})
			}
		}
	}
}

// archivalGlobalRoot is the global ethos store the hook sees: ~/.punt-labs/ethos
// under the HOME its environment names. The repair log it appends to lives there.
func archivalGlobalRoot(env []string) string {
	for _, e := range env {
		if home, ok := strings.CutPrefix(e, "HOME="); ok && home != "" {
			return filepath.Join(home, ".punt-labs", "ethos")
		}
	}
	return ""
}
