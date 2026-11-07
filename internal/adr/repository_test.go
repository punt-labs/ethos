package adr

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func repositoryStore(t *testing.T, mode layout.RecordMode) (*Store, *Store, layout.Policy) {
	t.Helper()
	root := canonicalTempDir(t)
	p, err := layout.NewRepository(root, canonicalTempDir(t), mode)
	require.NoError(t, err)
	legacy := NewStore(filepath.Join(canonicalTempDir(t), "adrs"))
	s, err := NewRepositoryStore(p, legacy.Dir())
	require.NoError(t, err)
	return s, legacy, p
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	// macOS commonly aliases /var to /private/var. Repository placement guards
	// require the same physical roots the production resolver supplies.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return dir
}

func sampleADR() *ADR {
	return &ADR{Title: "A decision", Decision: "Use typed YAML", Author: "bwk"}
}

func assertOnlyADRs(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			assert.True(t, strings.HasSuffix(entry.Name(), ".yaml"), path)
			assert.NotContains(t, entry.Name(), layout.TmpMarker, path)
			assert.NotContains(t, entry.Name(), ".lock", path)
		}
		return nil
	}))
}

func TestRepositoryStore_ReadPrecedenceAndCopyOnWrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, legacy, p := repositoryStore(t, layout.RecordBlame)
	a := sampleADR()
	require.NoError(t, legacy.Create(a))
	source, err := os.ReadFile(legacy.path(a.ID))
	require.NoError(t, err)
	got, err := s.Load(a.ID)
	require.NoError(t, err)
	assert.Equal(t, a, got)
	require.NoError(t, s.Update(a.ID, func(candidate *ADR) {
		candidate.Status = StatusSettled
		candidate.Decision = "Repository decision"
	}))
	got, err = s.Load(a.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusSettled, got.Status)
	assert.Equal(t, a.CreatedAt, got.CreatedAt)
	assert.Equal(t, a.ID, got.ID)
	assert.Equal(t, "Repository decision", got.Decision)
	unchanged, err := os.ReadFile(legacy.path(a.ID))
	require.NoError(t, err)
	assert.Equal(t, source, unchanged)
	ids, err := s.List()
	require.NoError(t, err)
	assert.Equal(t, []string{a.ID}, ids, "deduplicate repository and legacy copies")
	assert.Equal(t, filepath.Join(p.RecordRoot(), "adrs"), s.Dir())
	assertOnlyADRs(t, p.RecordRoot())
	lock, err := p.LockPath("adr", "store")
	require.NoError(t, err)
	assert.FileExists(t, lock)
}

func TestRepositoryStore_RefusesReadErrorsWithoutFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, kind := range []string{"malformed", "unknown-field", "multiple-documents", "mismatched-id", "symlink", "directory", "ancestor-symlink", "ancestor-file"} {
		t.Run(kind, func(t *testing.T) {
			s, legacy, _ := repositoryStore(t, layout.RecordBlame)
			a := sampleADR()
			require.NoError(t, legacy.Create(a))
			require.NoError(t, os.MkdirAll(s.dir, 0o700))
			source, err := os.ReadFile(legacy.path(a.ID))
			require.NoError(t, err)
			switch kind {
			case "malformed":
				require.NoError(t, os.WriteFile(s.path(a.ID), []byte("title: ["), 0o600))
			case "unknown-field":
				require.NoError(t, os.WriteFile(s.path(a.ID), append(source, []byte("unexpected: x\n")...), 0o600))
			case "multiple-documents":
				require.NoError(t, os.WriteFile(s.path(a.ID), append(source, []byte("---\nid: DES-001\n")...), 0o600))
			case "mismatched-id":
				require.NoError(t, os.WriteFile(s.path(a.ID), []byte(strings.ReplaceAll(string(source), a.ID, "DES-002")), 0o600))
			case "symlink":
				require.NoError(t, os.Symlink(legacy.path(a.ID), s.path(a.ID)))
			case "directory":
				require.NoError(t, os.Mkdir(s.path(a.ID), 0o700))
			case "ancestor-symlink":
				require.NoError(t, os.Remove(s.dir))
				require.NoError(t, os.Symlink(legacy.dir, s.dir))
			case "ancestor-file":
				require.NoError(t, os.Remove(s.dir))
				require.NoError(t, os.WriteFile(s.dir, []byte("not a directory"), 0o600))
			}
			_, err = s.Load(a.ID)
			require.Error(t, err)
			assert.False(t, errors.Is(err, os.ErrNotExist), "only absence, not corruption, permits fallback")
			err = s.Update(a.ID, func(a *ADR) { a.Status = StatusSettled })
			require.Error(t, err)
			unchanged, err := os.ReadFile(legacy.path(a.ID))
			require.NoError(t, err)
			assert.Equal(t, source, unchanged)
		})
	}
}

func TestRepositoryStore_LocalRecordDoesNotNeedReadableLegacy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, _, _ := repositoryStore(t, layout.RecordBlame)
	a := sampleADR()
	require.NoError(t, s.Create(a))
	broken := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(broken, []byte("unreadable legacy store"), 0o600))
	s.legacy = NewStore(broken)
	_, err := s.Load(a.ID)
	require.NoError(t, err, "a local read must not touch broken global storage")
	require.NoError(t, s.Update(a.ID, func(a *ADR) { a.Status = StatusSettled }))
	_, err = s.Load("DES-999")
	require.Error(t, err, "a missing local ADR must expose the legacy read error")
	_, err = s.List()
	require.Error(t, err, "listing cannot silently report a partial union")
	b := sampleADR()
	require.Error(t, s.Create(b), "allocation must refuse when fallback IDs cannot be enumerated")
}

func TestRepositoryStore_AllocatesAcrossFallbackAndRefusesExhaustion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, legacy, _ := repositoryStore(t, layout.RecordBlame)
	for i := 0; i < 3; i++ {
		require.NoError(t, legacy.Create(sampleADR()))
	}
	a := sampleADR()
	require.NoError(t, s.Create(a))
	assert.Equal(t, "DES-004", a.ID)
	last, err := os.ReadFile(legacy.path("DES-003"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(legacy.path("DES-999"), []byte(strings.ReplaceAll(string(last), "DES-003", "DES-999")), 0o600))
	b := sampleADR()
	before := clone(b)
	require.ErrorContains(t, s.Create(b), "max 999")
	assert.Equal(t, before, b)
}

func TestRepositoryStore_RedactsAllFreeTextAndPreservesSemantics(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, mode := range []layout.RecordMode{layout.RecordBlame, layout.RecordFull} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			s, _, p := repositoryStore(t, mode)
			secret := "ghp_" + strings.Repeat("x", 30)
			prose := home + "/private alice@example.org api_key=synthetic " + secret
			alternatives := []string{prose, "Consider " + filepath.Dir(filepath.Dir(p.CheckoutRecordRoot())) + "/design"}
			a := &ADR{Title: prose, Context: prose, Decision: prose, Alternatives: alternatives, Author: "bwk", MissionID: "m-2026-11-04-001", BeadID: "ethos-yjh0"}
			require.NoError(t, s.Create(a))
			assert.Equal(t, prose, alternatives[0], "do not mutate the caller's slice backing array")
			for _, value := range append([]string{a.Title, a.Context, a.Decision}, a.Alternatives...) {
				assert.NotContains(t, value, home)
				assert.NotContains(t, value, "alice@example.org")
				assert.NotContains(t, value, "synthetic")
				assert.NotContains(t, value, secret)
			}
			assert.Equal(t, "bwk", a.Author)
			assert.Equal(t, "m-2026-11-04-001", a.MissionID)
			assert.Equal(t, "ethos-yjh0", a.BeadID)
			assert.Equal(t, "Consider <repo>/design", a.Alternatives[1])
			created := a.CreatedAt
			require.NoError(t, s.Update(a.ID, func(a *ADR) {
				a.Context = "Authorization: Bearer syntheticcredential123456"
				a.Alternatives = []string{prose}
			}))
			loaded, err := s.Load(a.ID)
			require.NoError(t, err)
			assert.NotContains(t, loaded.Context, "syntheticcredential")
			assert.NotContains(t, loaded.Alternatives[0], secret)
			assert.Equal(t, created, loaded.CreatedAt)
			assertOnlyADRs(t, p.RecordRoot())
		})
	}
}

func TestRepositoryStore_RefusesRedactorFailureAndResidualLeaks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, _, _ := repositoryStore(t, layout.RecordBlame)
	for _, value := range []string{"app/home/alice/file", "%2Fhome%2Falice%2Ffile", string([]byte{0xff})} {
		a := sampleADR()
		a.Context = value
		before := clone(a)
		err := s.Create(a)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), value)
		assert.Equal(t, before, a)
	}
	a := sampleADR()
	require.NoError(t, s.Create(a))
	original, err := os.ReadFile(s.path(a.ID))
	require.NoError(t, err)
	for _, home := range []string{"", "/", "relative-home"} {
		t.Run(fmt.Sprintf("home=%q", home), func(t *testing.T) {
			t.Setenv("HOME", home)
			b := sampleADR()
			before := clone(b)
			require.Error(t, s.Create(b))
			assert.Equal(t, before, b)
			require.Error(t, s.Update(a.ID, func(a *ADR) { a.Context = "new context" }))
			data, err := os.ReadFile(s.path(a.ID))
			require.NoError(t, err)
			assert.Equal(t, original, data)
		})
	}
}

func TestRepositoryStore_SharedRedactorCorpusAtEverySink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := []struct {
		name  string
		value string
		leak  string
	}{
		{"owned-home", home + "/private", home},
		{"dash-home", strings.ReplaceAll(home, string(filepath.Separator), "-") + "-work", strings.ReplaceAll(home, string(filepath.Separator), "-")},
		{"foreign-home", "/Users/another-person/private", "/Users/another-person"},
		{"email", "private@example.org", "private@example.org"},
		{"github", "ghp_" + strings.Repeat("x", 30), "ghp_"},
		{"aws", "AKIA" + strings.Repeat("A", 16), "AKIA"},
		{"bearer", "Authorization: Bearer syntheticcredential123456", "syntheticcredential123456"},
		{"pem", "-----BEGIN PRIVATE KEY-----\nsyntheticprivatekey\n-----END PRIVATE KEY-----", "syntheticprivatekey"},
	}
	for _, tc := range cases {
		for _, sink := range []string{"create", "update", "migrate"} {
			t.Run(tc.name+"/"+sink, func(t *testing.T) {
				s, legacy, _ := repositoryStore(t, layout.RecordBlame)
				a := sampleADR()
				a.Title, a.Context, a.Decision = tc.value, tc.value, tc.value
				a.Alternatives = []string{tc.value}
				switch sink {
				case "create":
					require.NoError(t, s.Create(a))
				case "update":
					a = sampleADR()
					require.NoError(t, s.Create(a))
					require.NoError(t, s.Update(a.ID, func(a *ADR) {
						a.Title, a.Context, a.Decision = tc.value, tc.value, tc.value
						a.Alternatives = []string{tc.value}
					}))
				case "migrate":
					require.NoError(t, legacy.Create(a))
					_, err := s.Migrate([]string{a.ID}, true)
					require.NoError(t, err)
				}
				stored, err := s.loadOwn(a.ID)
				require.NoError(t, err)
				for _, value := range []string{stored.Title, stored.Context, stored.Decision, stored.Alternatives[0]} {
					assert.NotContains(t, value, tc.leak)
				}
				data, err := os.ReadFile(s.path(a.ID))
				require.NoError(t, err)
				assert.NotContains(t, string(data), tc.leak)
			})
		}
	}
}

func TestRepositoryStore_CommitFailuresPreservePriorDataAndLocalStaging(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, mode := range []layout.RecordMode{layout.RecordBlame, layout.RecordFull} {
		for _, operation := range []string{"create", "update", "copy-on-write", "migrate"} {
			t.Run(fmt.Sprintf("%d/%s", mode, operation), func(t *testing.T) {
				s, legacy, p := repositoryStore(t, mode)
				a := sampleADR()
				if operation == "update" {
					require.NoError(t, s.Create(a))
				} else {
					require.NoError(t, legacy.Create(a))
				}
				source, err := os.ReadFile(legacy.path(a.ID))
				if operation != "update" {
					require.NoError(t, err)
				}
				prior, err := os.ReadFile(s.path(a.ID))
				if operation == "update" {
					require.NoError(t, err)
				}
				fail := func(src, dst string) error {
					assert.Equal(t, p.TempDir(), filepath.Dir(src))
					assert.FileExists(t, src)
					assertOnlyADRs(t, p.RecordRoot())
					return &os.LinkError{Op: "commit", Old: src, New: dst, Err: syscall.EXDEV}
				}
				s.rename, s.link = fail, fail
				if operation == "create" {
					b := sampleADR()
					before := clone(b)
					err = s.Create(b)
					assert.Equal(t, before, b)
				} else if operation == "migrate" {
					_, err = s.Migrate([]string{a.ID}, true)
				} else {
					err = s.Update(a.ID, func(a *ADR) { a.Context = "uncommitted" })
				}
				require.ErrorIs(t, err, syscall.EXDEV)
				if operation == "update" {
					got, err := os.ReadFile(s.path(a.ID))
					require.NoError(t, err)
					assert.Equal(t, prior, got)
				} else {
					got, err := os.ReadFile(legacy.path(a.ID))
					require.NoError(t, err)
					assert.Equal(t, source, got)
				}
				entries, err := os.ReadDir(p.TempDir())
				require.NoError(t, err)
				assert.Empty(t, entries, "ordinary pre-commit errors clean their local temporary")
				assertOnlyADRs(t, p.RecordRoot())
			})
		}
	}
}

func TestRepositoryStore_NoClobber(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, _, _ := repositoryStore(t, layout.RecordBlame)
	s.link = func(src, dest string) error {
		require.NoError(t, os.WriteFile(dest, []byte("uncoordinated writer"), 0o600))
		return os.Link(src, dest)
	}
	a := sampleADR()
	before := clone(a)
	require.ErrorIs(t, s.Create(a), os.ErrExist)
	assert.Equal(t, before, a)
	data, err := os.ReadFile(s.path("DES-001"))
	require.NoError(t, err)
	assert.Equal(t, "uncoordinated writer", string(data))
}

func TestRepositoryStore_ConcurrentUpdatesAcrossSnapshots(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, _, p := repositoryStore(t, layout.RecordBlame)
	a := sampleADR()
	require.NoError(t, s.Create(a))
	full, err := layout.NewRepository(filepath.Dir(filepath.Dir(p.RecordRoot())), canonicalTempDir(t), layout.RecordFull)
	require.NoError(t, err)
	other, err := NewRepositoryStore(full, "")
	require.NoError(t, err)
	const workers = 24
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := s
			if i%2 == 1 {
				store = other
			}
			errs <- store.Update(a.ID, func(a *ADR) { a.Alternatives = append(a.Alternatives, fmt.Sprint(i)) })
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	got, err := s.Load(a.ID)
	require.NoError(t, err)
	assert.Len(t, got.Alternatives, workers, "no lost read/modify/write updates")
	assert.Equal(t, a.CreatedAt, got.CreatedAt)
	before := clone(got)
	for _, mutate := range []func(*ADR){func(a *ADR) { a.ID = "DES-999" }, func(a *ADR) { a.CreatedAt = "2020-01-01T00:00:00Z" }} {
		require.ErrorContains(t, s.Update(a.ID, mutate), "preserve")
	}
	got, err = s.Load(a.ID)
	require.NoError(t, err)
	assert.True(t, reflect.DeepEqual(before, got))
}

func TestRepositoryStore_RejectsInvalidPolicyAndRedirectedLocalStorage(t *testing.T) {
	_, err := NewRepositoryStore(layout.Policy{}, "")
	require.Error(t, err)
	s, _, p := repositoryStore(t, layout.RecordFull)
	require.NoError(t, os.MkdirAll(filepath.Dir(p.LocalRoot()), 0o700))
	require.NoError(t, os.Symlink(p.RecordRoot(), p.LocalRoot()))
	require.ErrorContains(t, s.Create(sampleADR()), "symlink")
	assert.NoFileExists(t, s.path("DES-001"))
}
