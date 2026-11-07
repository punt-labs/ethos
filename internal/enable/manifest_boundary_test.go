package enable

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests snapshot the entire isolated fixture, including Git metadata,
// both checkout/store roots, host guides/hooks/ignores and outside sentinels.
// Refusal must not even create a local staging or lock directory.
type manifestBoundaryEntry struct {
	Mode fs.FileMode
	Data string
	Link string
}

func manifestBoundarySnapshot(t *testing.T, root string) map[string]manifestBoundaryEntry {
	t.Helper()
	entries := make(map[string]manifestBoundaryEntry)
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entry := manifestBoundaryEntry{Mode: info.Mode()}
		switch {
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry.Data = string(data)
		case info.Mode()&os.ModeSymlink != 0:
			entry.Link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		entries[rel] = entry
		return nil
	}))
	return entries
}

func manifestBoundaryWrite(t *testing.T, path, data string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
	require.NoError(t, os.Chmod(path, 0o640))
}

func manifestBoundaryRepo(t *testing.T, root string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(root, 0o755))
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "config", "user.email", "test@example.com")
	gitRun(t, root, "config", "user.name", "test")
	gitRun(t, root, "config", "commit.gpgsign", "false")
}

func manifestBoundaryRoots(t *testing.T, linked bool) (parent, checkout, store string) {
	t.Helper()
	parent = physicalTempDir(t)
	store = filepath.Join(parent, "store")
	manifestBoundaryRepo(t, store)
	checkout = store
	if linked {
		// All Git writes are confined to this disposable fixture.
		gitRun(t, store, "commit", "--allow-empty", "-qm", "initial")
		checkout = filepath.Join(parent, "linked")
		gitRun(t, store, "worktree", "add", "--detach", checkout)
	}
	for _, root := range []string{store, checkout} {
		manifestBoundaryWrite(t, filepath.Join(root, "CLAUDE.md"), "# host guide\n")
		manifestBoundaryWrite(t, filepath.Join(root, ".gitignore"), "# user ignore\n*.build\n")
	}
	for _, name := range []string{"pre-commit", "commit-msg"} {
		manifestBoundaryWrite(t, filepath.Join(store, ".git", "hooks", name), "#!/bin/sh\n# host hook\n")
	}
	return parent, checkout, store
}

func TestManifestBoundaryRejectsUnownedEntriesBeforeMutation(t *testing.T) {
	for _, api := range []string{"deposit", "EnableTo", "EnableTo-linked"} {
		for _, entry := range []string{"identity", "other-guide", "traversal", "absolute"} {
			t.Run(api+"/"+entry, func(t *testing.T) {
				var parent, checkout, store string
				if api == "deposit" {
					parent = physicalTempDir(t)
					checkout = filepath.Join(parent, "checkout")
					require.NoError(t, os.Mkdir(checkout, 0o755))
					store = checkout
				} else {
					parent, checkout, store = manifestBoundaryRoots(t, api == "EnableTo-linked")
				}
				outside := filepath.Join(parent, "outside-sentinel")
				identityRel := ".punt-labs/ethos/identities/operator.yaml"
				otherGuideRel := ".punt-labs/vox/CLAUDE.md"
				for _, path := range []string{outside, filepath.Join(checkout, identityRel), filepath.Join(checkout, otherGuideRel)} {
					manifestBoundaryWrite(t, path, "sentinel bytes\n")
				}
				bad := map[string]string{
					"identity": identityRel, "other-guide": otherGuideRel,
					"traversal": "../outside-sentinel", "absolute": outside,
				}[entry]
				// Ordinary three paths first: validate the ENTIRE previous manifest,
				// not just a prefix or the new set, before any write or stale removal.
				manifestBoundaryWrite(t, filepath.Join(checkout, manifestRel), string(manifestBytes([]string{guideRel, setupRel, manifestRel, bad})))
				manifestBoundaryWrite(t, filepath.Join(checkout, guideRel), "old guide\n")
				manifestBoundaryWrite(t, filepath.Join(checkout, setupRel), "old setup\n")
				before := manifestBoundarySnapshot(t, parent)
				var err error
				if api == "deposit" {
					_, err = deposit(checkout, []byte("new guide\n"), []byte("new setup\n"))
				} else {
					var rep *Report
					rep, err = EnableTo(checkout, store)
					assert.Empty(t, rep.Steps, "refusal must precede all enable mutations")
				}
				assert.Error(t, err)
				assert.Equal(t, before, manifestBoundarySnapshot(t, parent))
			})
		}
	}
}

func TestManifestBoundaryDoesNotNormalizeEntries(t *testing.T) {
	for _, entry := range []string{
		" " + guideRel, guideRel + " ", "\t" + guideRel,
		"./" + guideRel, ".punt-labs/ethos/../ethos/CLAUDE.md",
		".punt-labs//ethos/CLAUDE.md", guideRel + "/", strings.ToLower(guideRel),
		strings.ReplaceAll(guideRel, "/", "\\"), "C:/outside", "C:outside",
		"\\\\server\\share\\sentinel", guideRel + "\x00", guideRel + "\r\r",
		"", setupRel, // empty interior line and duplicate owned entry
	} {
		t.Run(entry, func(t *testing.T) {
			root := physicalTempDir(t)
			manifestBoundaryWrite(t, filepath.Join(root, manifestRel), string(manifestBytes([]string{setupRel, manifestRel, entry})))
			before := manifestBoundarySnapshot(t, root)
			_, err := deposit(root, []byte("new guide"), []byte("new setup"))
			assert.Error(t, err)
			assert.Equal(t, before, manifestBoundarySnapshot(t, root))
		})
	}
}

func TestManifestBoundaryPhysicalPaths(t *testing.T) {
	for _, api := range []string{"deposit", "EnableTo", "EnableTo-linked"} {
		for _, kind := range []string{"manifest-link", "manifest-dangling", "manifest-directory", "punt-labs-link", "ethos-link", "punt-labs-file", "ethos-file"} {
			t.Run(api+"/"+kind, func(t *testing.T) {
				parent, checkout, store := manifestBoundaryRoots(t, api == "EnableTo-linked")
				outside := filepath.Join(parent, "outside")
				require.NoError(t, os.Mkdir(outside, 0o755))
				manifestBoundaryWrite(t, filepath.Join(outside, "manifest"), string(manifestBytes([]string{guideRel, setupRel, manifestRel})))
				// Include plausible deposits in redirected directories as well.
				for _, base := range []string{outside, filepath.Join(outside, "ethos")} {
					manifestBoundaryWrite(t, filepath.Join(base, ".vendored-manifest"), string(manifestBytes([]string{guideRel, setupRel, manifestRel})))
					manifestBoundaryWrite(t, filepath.Join(base, "CLAUDE.md"), "outside guide\n")
				}
				path := filepath.Join(checkout, manifestRel)
				target := filepath.Join(outside, "manifest")
				switch kind {
				case "manifest-dangling":
					target = filepath.Join(outside, "missing")
				case "punt-labs-link", "punt-labs-file":
					path, target = filepath.Join(checkout, ".punt-labs"), outside
				case "ethos-link", "ethos-file":
					path, target = filepath.Join(checkout, ".punt-labs", "ethos"), outside
				}
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				switch {
				case kind == "manifest-directory":
					require.NoError(t, os.Mkdir(path, 0o755))
				case strings.HasSuffix(kind, "-file"):
					manifestBoundaryWrite(t, path, "not a directory\n")
				default:
					if runtime.GOOS == "windows" {
						t.Skip("symlink creation needs Windows privilege; cross-vet still covers compilation")
					}
					require.NoError(t, os.Symlink(target, path))
				}
				before := manifestBoundarySnapshot(t, parent)
				var err error
				if api == "deposit" {
					_, err = deposit(checkout, []byte("guide"), []byte("setup"))
				} else {
					_, err = EnableTo(checkout, store)
				}
				assert.Error(t, err)
				assert.Equal(t, before, manifestBoundarySnapshot(t, parent))
			})
		}
	}
}

func TestManifestBoundaryExistingEmptyIsNotBootstrap(t *testing.T) {
	root := physicalTempDir(t)
	manifestBoundaryWrite(t, filepath.Join(root, manifestRel), "")
	manifestBoundaryWrite(t, filepath.Join(root, guideRel), "not owned by empty manifest\n")
	before := manifestBoundarySnapshot(t, root)
	_, err := deposit(root, []byte("guide"), []byte("setup"))
	assert.Error(t, err)
	assert.Equal(t, before, manifestBoundarySnapshot(t, root))
}

func TestManifestBoundaryFIFO(t *testing.T) {
	const childRoot = "ETHOS_TEST_MANIFEST_FIFO_ROOT"
	if root := os.Getenv(childRoot); root != "" {
		if os.Getenv("ETHOS_TEST_MANIFEST_FIFO_API") == "deposit" {
			_, err := deposit(root, []byte("guide"), []byte("setup"))
			require.ErrorContains(t, err, "not a regular file")
		} else {
			_, err := EnableTo(root, root)
			require.ErrorContains(t, err, "not a regular file")
		}
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix filesystem FIFOs")
	}
	mkfifo, err := exec.LookPath("mkfifo")
	require.NoError(t, err, "FIFO refusal must be exercised, not silently skipped")
	for _, api := range []string{"deposit", "EnableTo"} {
		t.Run(api, func(t *testing.T) {
			parent, root, _ := manifestBoundaryRoots(t, false)
			path := filepath.Join(root, manifestRel)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			out, err := exec.Command(mkfifo, path).CombinedOutput()
			require.NoError(t, err, "%s", out)
			before := manifestBoundarySnapshot(t, parent)
			// A regression must fail within a deadline rather than hanging the
			// package test runner in ReadFile on an unopened FIFO.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManifestBoundaryFIFO$")
			cmd.Env = append(os.Environ(), childRoot+"="+root, "ETHOS_TEST_MANIFEST_FIFO_API="+api)
			out, err = cmd.CombinedOutput()
			assert.NoError(t, ctx.Err(), "manifest FIFO read blocked")
			assert.NoError(t, err, "%s", out)
			assert.Equal(t, before, manifestBoundarySnapshot(t, parent))
		})
	}
}

func TestManifestBoundaryUnlistedDanglingTargetIsCollision(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs Windows privilege")
	}
	root := physicalTempDir(t)
	manifestBoundaryWrite(t, filepath.Join(root, manifestRel), string(manifestBytes([]string{guideRel, manifestRel})))
	require.NoError(t, os.Symlink("missing-target", filepath.Join(root, setupRel)))
	before := manifestBoundarySnapshot(t, root)
	_, err := deposit(root, []byte("guide"), []byte("setup"))
	assert.ErrorContains(t, err, "collision")
	assert.Equal(t, before, manifestBoundarySnapshot(t, root))
}

func TestManifestBoundaryUnterminatedCarriageReturnIsNotNewline(t *testing.T) {
	root := physicalTempDir(t)
	data := setupRel + "\n" + manifestRel + "\n" + guideRel + "\r"
	manifestBoundaryWrite(t, filepath.Join(root, manifestRel), data)
	before := manifestBoundarySnapshot(t, root)
	_, err := deposit(root, []byte("guide"), []byte("setup"))
	assert.Error(t, err)
	assert.Equal(t, before, manifestBoundarySnapshot(t, root))
}

func TestManifestBoundaryControls(t *testing.T) {
	for _, initial := range []string{"bootstrap", "legacy-two", "current-three", "interrupted", "crlf", "no-final-newline"} {
		t.Run(initial, func(t *testing.T) {
			root := physicalTempDir(t)
			if initial != "interrupted" {
				manifestBoundaryWrite(t, filepath.Join(root, guideRel), "old guide\n")
			}
			switch initial {
			case "legacy-two":
				// DES-059's original two paths; DES-071 added the setup file.
				manifestBoundaryWrite(t, filepath.Join(root, manifestRel), string(manifestBytes([]string{guideRel, manifestRel})))
			case "current-three", "interrupted":
				manifestBoundaryWrite(t, filepath.Join(root, manifestRel), string(manifestBytes([]string{setupRel, manifestRel, guideRel})))
			case "crlf":
				manifestBoundaryWrite(t, filepath.Join(root, manifestRel), strings.ReplaceAll(string(manifestBytes([]string{guideRel, manifestRel})), "\n", "\r\n"))
			case "no-final-newline":
				manifestBoundaryWrite(t, filepath.Join(root, manifestRel), guideRel+"\n"+manifestRel)
			}
			_, err := deposit(root, []byte("guide\n"), []byte("setup\n"))
			require.NoError(t, err)
			before := manifestBoundarySnapshot(t, root)
			_, err = deposit(root, []byte("guide\n"), []byte("setup\n"))
			require.NoError(t, err)
			assert.Equal(t, before, manifestBoundarySnapshot(t, root))
		})
	}
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "EnableTo", true: "EnableTo-linked"}[linked], func(t *testing.T) {
			parent, checkout, store := manifestBoundaryRoots(t, linked)
			_, err := EnableTo(checkout, store)
			require.NoError(t, err)
			before := manifestBoundarySnapshot(t, parent)
			_, err = EnableTo(checkout, store)
			require.NoError(t, err)
			assert.Equal(t, before, manifestBoundarySnapshot(t, parent))
			assert.Equal(t, string(Guide), readFile(t, filepath.Join(checkout, guideRel)))
			assert.Equal(t, string(Setup), readFile(t, filepath.Join(checkout, setupRel)))
			if linked {
				assert.NoFileExists(t, filepath.Join(store, manifestRel))
				assert.NoDirExists(t, filepath.Join(checkout, ".punt-labs", "local"))
			}
		})
	}
}

func TestManifestBoundaryConcurrentRefusals(t *testing.T) {
	root := physicalTempDir(t)
	manifestBoundaryWrite(t, filepath.Join(root, manifestRel), string(manifestBytes([]string{guideRel, setupRel, manifestRel, "../sentinel"})))
	before := manifestBoundarySnapshot(t, root)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			_, err := deposit(root, []byte("guide"), []byte("setup"))
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.Error(t, err)
	}
	assert.Equal(t, before, manifestBoundarySnapshot(t, root))
}
