package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestFilesystemReflectionShapes uses the real CLI only as a writer. Every read
// below uses plain YAML/JSON and the paths in docs/storage-layout.md, not the
// mission reader, its types, or a production path classifier.
func TestFilesystemReflectionShapes(t *testing.T) {
	source, err := filepath.Abs("..")
	require.NoError(t, err)
	binary := filepath.Join(t.TempDir(), "ethos")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-race", "-o", binary, "./cmd/ethos")
	build.Dir = source
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build writer: %s", out)

	for _, mode := range []string{"blame", "full"} {
		t.Run(mode, func(t *testing.T) {
			f := newFilesystemFixture(t, binary, mode)
			contractBefore := filesystemRead(t, f.contract)
			initialized := filesystemYAML(t, contractBefore)
			authority := filesystemMap(t, initialized["round_decisions"])
			require.Equal(t, 1, authority["version"])
			require.Empty(t, filesystemList(t, authority["entries"]))
			assert.NoFileExists(t, f.archive)
			assert.NoFileExists(t, f.receipt)

			// Invalid writer inputs must not install a decision or detail.
			for _, malformed := range []string{
				"mission: [invalid\n",
				f.reflection + "unknown_field: true\n",
				f.reflection + "round: 1\n",
				f.reflection + "---\n{}\n",
				strings.Replace(f.reflection, "round: 1", "round: 2", 1),
			} {
				f.write(t, f.input, malformed)
				_, stderr, err := f.run(t, binary, "mission", "reflect", f.id, "--file", f.input, "--json")
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit, "%s", stderr)
				require.Equal(t, 1, exit.ExitCode(), "%s", stderr)
				require.Contains(t, string(stderr), "mission reflect:")
				require.Equal(t, contractBefore, filesystemRead(t, f.contract))
				assert.NoFileExists(t, f.archive)
				assert.NoFileExists(t, f.receipt)
			}
			f.write(t, f.input, f.reflection)
			f.cli(t, "mission", "reflect", f.id, "--file", f.input, "--json")
			envelope := filesystemYAML(t, filesystemRead(t, f.archive))
			f.assertEnvelope(t, envelope, mode == "full")
			decision := filesystemDecision(t, filesystemRead(t, f.contract))
			require.Equal(t, decision, envelope["decision"], "all five authority fields must match")
			assert.NoFileExists(t, f.receipt)
			assert.NoFileExists(t, filepath.Join(filepath.Dir(f.archive), "reflections.yaml"))
			if mode == "full" {
				published := filesystemYAML(t, filesystemRead(t, f.published))
				assert.Len(t, published, 1)
				assert.Equal(t, []any{envelope["reflection"]}, filesystemList(t, published["reflections"]))
			} else {
				assert.NoFileExists(t, f.published)
			}

			// A contract-only handoff is submitted/detail-unavailable, not an
			// empty decision log. This is a file-copy fixture, not a Git clone.
			handoff := filepath.Join(t.TempDir(), "handoff")
			shared := filepath.Join(handoff, ".punt-labs", "ethos", "missions", f.id, "contract.yaml")
			f.write(t, shared, string(filesystemRead(t, f.contract)))
			require.Equal(t, decision, filesystemDecision(t, filesystemRead(t, shared)))
			_, err := os.Stat(filepath.Join(handoff, ".punt-labs", "local", "ethos"))
			require.ErrorIs(t, err, os.ErrNotExist)
			assert.NoFileExists(t, filepath.Join(filepath.Dir(shared), "reflections.yaml"))
		})
	}

	t.Run("pending receipt after optional archive failure", func(t *testing.T) {
		f := newFilesystemFixture(t, binary, "full")
		initialized := filesystemRead(t, f.contract)
		// Fail optional archival after acceptance without permissions, timing,
		// private hooks, or a process crash. The CLI itself persists the receipt.
		require.NoError(t, os.MkdirAll(f.archive, 0o700))
		result := f.cli(t, "mission", "reflect", f.id, "--file", f.input, "--json")
		var response struct {
			Outcome struct {
				Application string   `json:"application"`
				Trace       string   `json:"trace"`
				Diagnostics []string `json:"diagnostics"`
			} `json:"outcome"`
		}
		require.NoError(t, json.Unmarshal(result, &response))
		require.Equal(t, "applied", response.Outcome.Application)
		require.Equal(t, "pending", response.Outcome.Trace)
		require.NotEmpty(t, response.Outcome.Diagnostics)
		receiptBytes := filesystemRead(t, f.receipt)
		receipt := filesystemYAML(t, receiptBytes)
		f.assertEnvelope(t, receipt, true)
		require.Equal(t, filesystemDecision(t, filesystemRead(t, f.contract)), receipt["decision"])
		assert.NoFileExists(t, f.published)

		// Pairing the writer's receipt with the saved pre-submission authority
		// models a prepared-only observation, not an injected pre-accept crash.
		// The full payload/event alone cannot supply the missing shared entry.
		before := filesystemYAML(t, initialized)
		require.Empty(t, filesystemList(t, filesystemMap(t, before["round_decisions"])["entries"]))
		require.NotEmpty(t, filesystemMap(t, receipt["decision"])["id"])

		require.NoError(t, os.Remove(f.archive))
		// Current mode cannot revoke the receipt's captured publication consent.
		f.write(t, filepath.Join(f.repo, ".punt-labs", "ethos.yaml"), "resolution: repo-only\nrecord: blame\n")
		f.cli(t, "mission", "reflect", f.id, "--file", f.input, "--json")
		require.Equal(t, receiptBytes, filesystemRead(t, f.archive))
		assert.NoFileExists(t, f.receipt)
		published := filesystemYAML(t, filesystemRead(t, f.published))
		assert.Equal(t, []any{receipt["reflection"]}, filesystemList(t, published["reflections"]))
		f.assertEnvelope(t, filesystemYAML(t, filesystemRead(t, f.archive)), true)
	})
}

type filesystemFixture struct {
	repo       string
	binary     string
	id         string
	contract   string
	archive    string
	receipt    string
	published  string
	input      string
	reflection string
	env        []string
}

func newFilesystemFixture(t *testing.T, binary, mode string) *filesystemFixture {
	t.Helper()
	root := t.TempDir()
	f := &filesystemFixture{repo: filepath.Join(root, "repo with 'quotes'"), binary: binary}
	home, tmp := filepath.Join(root, "home"), filepath.Join(root, "tmp")
	for _, path := range []string{f.repo, home, tmp} {
		require.NoError(t, os.MkdirAll(path, 0o700))
	}
	// Allowlist only execution requirements. Never inherit the caller's Git
	// index/config, Ethos mission/session/store, credentials, hooks, or home.
	for _, key := range []string{"PATH", "SystemRoot", "WINDIR"} {
		if value, ok := os.LookupEnv(key); ok {
			f.env = append(f.env, key+"="+value)
		}
	}
	f.env = append(f.env, "HOME="+home, "USERPROFILE="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
		"TMPDIR="+tmp, "TMP="+tmp, "TEMP="+tmp,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(home, "gitconfig"),
		"GIT_TERMINAL_PROMPT=0", "GIT_CEILING_DIRECTORIES="+root)
	f.write(t, filepath.Join(home, "gitconfig"), "")
	for _, args := range [][]string{
		{"init", "--template=", "-b", "main"},
		{"config", "user.name", "Filesystem Fixture"},
		{"config", "user.email", "fixture@example.invalid"},
	} {
		out, stderr, err := f.run(t, "git", args...)
		require.NoError(t, err, "git %q: %s%s", args, out, stderr)
	}
	f.write(t, filepath.Join(f.repo, ".punt-labs", "ethos.yaml"), "resolution: repo-only\nrecord: "+mode+"\n")
	record := filepath.Join(f.repo, ".punt-labs", "ethos")
	for _, kind := range []string{"personalities", "writing-styles", "talents"} {
		f.write(t, filepath.Join(record, kind, "fixture.md"), "# Fixture\n\nFilesystem acceptance content.\n")
	}
	for _, handle := range []string{"claude", "bwk", "djb"} {
		f.write(t, filepath.Join(record, "identities", handle+".yaml"),
			fmt.Sprintf("name: %s\nhandle: %s\nkind: agent\npersonality: fixture\nwriting_style: fixture\ntalents: [fixture]\n", handle, handle))
	}
	f.write(t, filepath.Join(record, "archetypes", "implement.yaml"), "name: implement\ndescription: Filesystem acceptance\n")
	contractInput := filepath.Join(root, "contract.yaml")
	f.write(t, contractInput, "leader: claude\nworker: bwk\nevaluator:\n  handle: djb\n"+
		"write_set: [work/]\nsuccess_criteria: [Filesystem reader evidence]\n"+
		"budget:\n  rounds: 2\n  reflection_after_each: true\ncontext: Filesystem acceptance\n")
	var created struct {
		MissionID string `json:"mission_id"`
	}
	require.NoError(t, json.Unmarshal(f.cli(t, "mission", "create", "--file", contractInput, "--json"), &created))
	require.Regexp(t, `^m-\d{4}-\d{2}-\d{2}-\d{3}$`, created.MissionID)
	f.id = created.MissionID
	f.contract = filepath.Join(record, "missions", f.id, "contract.yaml")
	local := filepath.Join(f.repo, ".punt-labs", "local", "ethos")
	f.archive = filepath.Join(local, "trace", "missions", f.id, "reflection-1.yaml")
	f.receipt = filepath.Join(local, "missions", f.id, "reflection-intents", "1.yaml")
	f.published = filepath.Join(record, "missions", f.id, "reflections.yaml")
	f.input = filepath.Join(root, "reflection.yaml")
	f.reflection = fmt.Sprintf("mission: %s\nround: 1\nauthor: claude\nconverging: true\n"+
		"signals: [filesystem-reader-signal]\nrecommendation: continue\nreason: Filesystem reader rationale\n", f.id)
	f.write(t, f.input, f.reflection)
	return f
}

func (f *filesystemFixture) run(t *testing.T, program string, args ...string) ([]byte, []byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir, cmd.Env = f.repo, f.env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	return out, stderr.Bytes(), err
}

func (f *filesystemFixture) cli(t *testing.T, args ...string) []byte {
	t.Helper()
	out, stderr, err := f.run(t, f.binary, args...)
	require.NoError(t, err, "ethos %q: %s%s", args, out, stderr)
	return out
}

func (f *filesystemFixture) write(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func filesystemRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err, "read %s", path)
	return data
}

func filesystemYAML(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var value map[string]any
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	require.NoError(t, decoder.Decode(&value))
	var extra any
	require.ErrorIs(t, decoder.Decode(&extra), io.EOF, "expected one YAML document")
	return value
}

func filesystemMap(t *testing.T, value any) map[string]any {
	t.Helper()
	mapping, ok := value.(map[string]any)
	require.True(t, ok, "expected YAML mapping, got %T", value)
	return mapping
}

func filesystemList(t *testing.T, value any) []any {
	t.Helper()
	list, ok := value.([]any)
	require.True(t, ok, "expected YAML sequence, got %T", value)
	return list
}

func filesystemDecision(t *testing.T, data []byte) map[string]any {
	t.Helper()
	authority := filesystemMap(t, filesystemYAML(t, data)["round_decisions"])
	require.Equal(t, 1, authority["version"])
	entries := filesystemList(t, authority["entries"])
	require.Len(t, entries, 1)
	decision := filesystemMap(t, entries[0])
	require.Len(t, decision, 5)
	require.Regexp(t, `^[0-9a-f]{32}$`, decision["id"])
	return decision
}

func (f *filesystemFixture) assertEnvelope(t *testing.T, envelope map[string]any, publish bool) {
	t.Helper()
	require.Len(t, envelope, 8)
	assert.Equal(t, 1, envelope["version"])
	assert.Equal(t, "reflect", envelope["kind"])
	assert.Equal(t, f.id, envelope["mission"])
	assert.Equal(t, publish, envelope["publish"])
	decision := filesystemMap(t, envelope["decision"])
	reflection := filesystemMap(t, envelope["reflection"])
	require.Len(t, reflection, 8)
	assert.Equal(t, f.id, reflection["mission"])
	assert.Equal(t, 1, reflection["round"])
	assert.Equal(t, "claude", reflection["author"])
	assert.Equal(t, "continue", reflection["recommendation"])
	for _, field := range []string{"round", "created_at", "author", "recommendation"} {
		assert.Equal(t, decision[field], reflection[field], field)
	}
	createdAt, ok := reflection["created_at"].(string)
	require.True(t, ok)
	_, err := time.Parse(time.RFC3339, createdAt)
	require.NoError(t, err)
	assert.Equal(t, true, reflection["converging"])
	assert.Equal(t, []any{"filesystem-reader-signal"}, reflection["signals"])
	assert.Equal(t, "Filesystem reader rationale", reflection["reason"])
	event := filesystemMap(t, envelope["event"])
	require.Len(t, event, 7)
	assert.Equal(t, "reflect", event["event"])
	assert.Equal(t, reflection["author"], event["actor"])
	assert.Equal(t, decision["id"], event["operationid"])
	assert.Equal(t, "", event["acceptance"], "serialized event is not authority")
	assert.Equal(t, map[string]any{"round": 1, "recommendation": "continue", "converging": true, "signal_count": 1}, event["details"])
	mode := "blame"
	if publish {
		mode = "full"
	}
	assert.Equal(t, mode, event["record"])
	session, ok := envelope["event_session"].(string)
	require.True(t, ok)
	require.NotEmpty(t, session)
	stream := filepath.Join(f.repo, ".punt-labs", "local", "ethos", "missions", f.id, session+".log.jsonl")
	count := 0
	for _, line := range bytes.Split(bytes.TrimSpace(filesystemRead(t, stream)), []byte("\n")) {
		var logged map[string]any
		require.NoError(t, json.Unmarshal(line, &logged))
		if logged["operation_id"] == decision["id"] {
			count++
			assert.Equal(t, "reflect", logged["event"])
			assert.Equal(t, event["ts"], logged["ts"])
			assert.Equal(t, mode, logged["record"])
		}
	}
	require.Equal(t, 1, count, "one exact event in the envelope's original stream")
}
