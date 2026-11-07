package enable

import (
	"context"
	"fmt"
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

	"github.com/punt-labs/ethos/v4/internal/layout"
)

type bootstrapContentSnapshot struct {
	entries map[string]manifestBoundaryEntry
	info    map[string]fs.FileInfo
}

func snapshotBootstrapContent(t *testing.T, root string) bootstrapContentSnapshot {
	t.Helper()
	s := bootstrapContentSnapshot{entries: manifestBoundarySnapshot(t, root), info: make(map[string]fs.FileInfo)}
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		s.info[path], err = d.Info() // lstat metadata: never open FIFOs or follow links
		return err
	}))
	return s
}

func assertBootstrapContentUnchanged(t *testing.T, root string, before bootstrapContentSnapshot) {
	t.Helper()
	after := snapshotBootstrapContent(t, root)
	assert.Equal(t, before.entries, after.entries, "names, bytes, modes and link targets")
	for path, info := range before.info {
		got, ok := after.info[path]
		if assert.True(t, ok, "missing %s", path) {
			assert.True(t, os.SameFile(info, got), "file identity changed: %s", path)
			assert.Equal(t, info.ModTime(), got.ModTime(), "modification time changed: %s", path)
		}
	}
}

func bootstrapContentHazard(t *testing.T, checkout, outside, rel, kind string) {
	t.Helper()
	path := filepath.Join(checkout, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.MkdirAll(outside, 0o755))
	if kind == "directory" {
		require.NoError(t, os.Mkdir(path, 0o750))
		return
	}
	if kind == "fifo" || strings.HasSuffix(kind, "fifo-link") {
		if runtime.GOOS == "windows" {
			t.Skip("Unix filesystem FIFOs are not available on Windows")
		}
		fifo := path
		if kind == "internal-fifo-link" {
			fifo = filepath.Join(filepath.Dir(path), "unopened-fifo")
		} else if kind == "external-fifo-link" {
			fifo = filepath.Join(outside, "unopened-fifo")
		}
		mkfifo, err := exec.LookPath("mkfifo")
		require.NoError(t, err, "FIFO coverage requires the POSIX mkfifo utility")
		out, err := exec.Command(mkfifo, fifo).CombinedOutput()
		require.NoError(t, err, "%s", out)
		if kind == "fifo" {
			return
		}
		require.NoError(t, os.Symlink(fifo, path))
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures require Windows privileges; cross-vet covers compilation")
	}
	target := filepath.Join(outside, "target")
	switch kind {
	case "regular-link":
		manifestBoundaryWrite(t, target, "outside target bytes\n")
	case "dangling-link":
		// Leave target absent. A dangling content link is not bootstrap absence.
	case "device-link":
		// Only the explicitly enabled fixed-branch control uses this link. Do
		// not open, read, create or snapshot the device itself.
		target = "/dev/zero"
	default:
		t.Fatalf("unknown bootstrap fixture %q", kind)
	}
	require.NoError(t, os.Symlink(target, path))
}

const bootstrapChildRoot = "ETHOS_TEST_BOOTSTRAP_CONTENT_ROOT"

func bootstrapContentChild(t *testing.T, checkout, store, api, rel string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBootstrapContentRefusesHazardousLeaf$")
	cmd.Env = append(os.Environ(), bootstrapChildRoot+"="+checkout,
		"ETHOS_TEST_BOOTSTRAP_CONTENT_STORE="+store,
		"ETHOS_TEST_BOOTSTRAP_CONTENT_API="+api,
		"ETHOS_TEST_BOOTSTRAP_CONTENT_REL="+rel)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	t.Logf("child api=%s leaf=%s elapsed=%s deadline=%v exit=%v output=%s", api, rel, time.Since(start), ctx.Err(), err, out)
	assert.NoError(t, ctx.Err(), "a killed/timed-out child is the availability defect, not successful refusal")
	assert.NoError(t, err, "refusal must be a normal successful child test, not a signal or crash: %s", out)
	assert.Contains(t, string(out), "BOOTSTRAP_CONTENT_REFUSED "+rel)
}

func bootstrapContentRefusalCase(t *testing.T, api, rel, kind string) {
	t.Helper()
	parent, checkout, store := manifestBoundaryRoots(t, api == "EnableTo-linked")
	other := guideRel
	if rel == guideRel {
		other = setupRel
	}
	manifestBoundaryWrite(t, filepath.Join(checkout, other), "unchanged other content\n")
	bootstrapContentHazard(t, checkout, filepath.Join(parent, "outside"), rel, kind)
	before := snapshotBootstrapContent(t, parent)
	bootstrapContentChild(t, checkout, store, api, rel)
	assertBootstrapContentUnchanged(t, parent, before)
}

func TestBootstrapContentRefusesHazardousLeaf(t *testing.T) {
	if checkout := os.Getenv(bootstrapChildRoot); checkout != "" {
		api := os.Getenv("ETHOS_TEST_BOOTSTRAP_CONTENT_API")
		rel := os.Getenv("ETHOS_TEST_BOOTSTRAP_CONTENT_REL")
		var err error
		if api == "deposit" {
			_, err = deposit(checkout, []byte("guide\n"), []byte("setup\n"))
		} else {
			require.Contains(t, []string{"EnableTo", "EnableTo-linked"}, api)
			var rep *Report
			rep, err = EnableTo(checkout, os.Getenv("ETHOS_TEST_BOOTSTRAP_CONTENT_STORE"))
			require.NotNil(t, rep)
			assert.Empty(t, rep.Steps)
			assert.Empty(t, rep.Warnings)
		}
		require.ErrorContains(t, err, "bootstrap content")
		require.ErrorContains(t, err, rel)
		require.ErrorContains(t, err, "not a regular file")
		fmt.Println("BOOTSTRAP_CONTENT_REFUSED " + rel)
		return
	}
	for _, api := range []string{"deposit", "EnableTo", "EnableTo-linked"} {
		for _, rel := range []string{guideRel, setupRel} {
			for _, kind := range []string{"fifo", "internal-fifo-link", "external-fifo-link", "dangling-link", "regular-link", "directory"} {
				t.Run(api+"/"+filepath.Base(rel)+"/"+kind, func(t *testing.T) {
					bootstrapContentRefusalCase(t, api, rel, kind)
				})
			}
		}
	}
}

func TestBootstrapContentDeviceLinkFixedOnly(t *testing.T) {
	if os.Getenv("ETHOS_TEST_BOOTSTRAP_DEVICE_CONTROL") != "1" {
		t.Skip("device-link control must only be enabled on the fixed branch, never the RED overlay")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Unix device-link control")
	}
	before, err := os.Stat("/dev/zero")
	require.NoError(t, err)
	require.NotZero(t, before.Mode()&os.ModeDevice)
	for _, api := range []string{"deposit", "EnableTo", "EnableTo-linked"} {
		for _, rel := range []string{guideRel, setupRel} {
			t.Run(api+"/"+filepath.Base(rel), func(t *testing.T) {
				bootstrapContentRefusalCase(t, api, rel, "device-link")
			})
		}
	}
	after, err := os.Stat("/dev/zero")
	require.NoError(t, err)
	assert.True(t, os.SameFile(before, after))
	assert.Equal(t, before.Mode(), after.Mode())
	assert.Equal(t, before.ModTime(), after.ModTime())
}

func TestBootstrapContentControls(t *testing.T) {
	for _, api := range []string{"deposit", "EnableTo", "EnableTo-linked"} {
		for _, initial := range []string{"absent", "guide-only", "setup-only", "equal", "different", "valid-manifest"} {
			t.Run(api+"/"+initial, func(t *testing.T) {
				_, checkout, store := manifestBoundaryRoots(t, api == "EnableTo-linked")
				guide, setup := Guide, Setup
				if api == "deposit" {
					guide, setup = []byte("guide\n"), []byte("setup\n")
				}
				if initial != "absent" {
					for rel, data := range map[string][]byte{guideRel: guide, setupRel: setup} {
						if (initial == "guide-only" && rel == setupRel) || (initial == "setup-only" && rel == guideRel) {
							continue
						}
						if initial != "equal" {
							data = []byte("different old content\n")
						}
						manifestBoundaryWrite(t, filepath.Join(checkout, rel), string(data))
					}
				}
				if initial == "valid-manifest" {
					manifestBoundaryWrite(t, filepath.Join(checkout, manifestRel), string(manifestBytes([]string{guideRel, setupRel, manifestRel})))
				}
				var warnings []string
				var err error
				if api == "deposit" {
					warnings, err = deposit(checkout, guide, setup)
				} else {
					var rep *Report
					rep, err = EnableTo(checkout, store)
					warnings = rep.Warnings
				}
				require.NoError(t, err)
				if initial == "different" || initial == "guide-only" || initial == "setup-only" {
					count := 2
					if initial != "different" {
						count = 1
					}
					assert.Len(t, warnings, count)
					if initial != "setup-only" {
						assert.Contains(t, strings.Join(warnings, "\n"), guideRel)
					}
					if initial != "guide-only" {
						assert.Contains(t, strings.Join(warnings, "\n"), setupRel)
					}
				} else {
					assert.Empty(t, warnings)
				}
				assert.Equal(t, string(guide), readFile(t, filepath.Join(checkout, guideRel)))
				assert.Equal(t, string(setup), readFile(t, filepath.Join(checkout, setupRel)))
			})
		}
	}
}

func TestBootstrapContentValidManifestDoesNotCompareLeaves(t *testing.T) {
	for _, rel := range []string{guideRel, setupRel} {
		for _, kind := range []string{"fifo", "internal-fifo-link", "external-fifo-link", "dangling-link", "regular-link"} {
			t.Run(filepath.Base(rel)+"/"+kind, func(t *testing.T) {
				root := physicalTempDir(t)
				checkout, outside := filepath.Join(root, "checkout"), filepath.Join(root, "outside")
				require.NoError(t, os.Mkdir(checkout, 0o755))
				bootstrapContentHazard(t, checkout, outside, rel, kind)
				before := snapshotBootstrapContent(t, outside)
				manifestBoundaryWrite(t, filepath.Join(checkout, manifestRel), string(manifestBytes([]string{guideRel, setupRel, manifestRel})))
				warnings, err := deposit(checkout, []byte("guide\n"), []byte("setup\n"))
				require.NoError(t, err)
				assert.Empty(t, warnings)
				assertBootstrapContentUnchanged(t, outside, before)
				info, err := os.Lstat(filepath.Join(checkout, rel))
				require.NoError(t, err)
				assert.True(t, info.Mode().IsRegular())
			})
		}
	}
}

func TestBootstrapContentManifestFirstRetry(t *testing.T) {
	root := physicalTempDir(t)
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	manifestBoundaryWrite(t, filepath.Join(root, manifestRel), string(manifestBytes([]string{guideRel, manifestRel})))
	// With an existing valid manifest, content is not compared at bootstrap.
	// Keep the interrupted-install retry contract independent of bootstrap's
	// new type refusal.
	require.NoError(t, os.Mkdir(filepath.Join(root, guideRel), 0o755))
	_, err = deposit(root, []byte("guide"), []byte("setup"), p)
	require.Error(t, err)
	assert.FileExists(t, filepath.Join(root, manifestRel))
	assert.Equal(t, string(manifestBytes([]string{guideRel, setupRel, manifestRel})), readFile(t, filepath.Join(root, manifestRel)),
		"updated manifest must land before the failing content installation")
	entries, err := os.ReadDir(p.TempDir())
	require.NoError(t, err)
	assert.Empty(t, entries)
	require.NoError(t, os.Remove(filepath.Join(root, guideRel)))
	_, err = deposit(root, []byte("guide"), []byte("setup"), p)
	require.NoError(t, err)
	assert.Equal(t, "guide", readFile(t, filepath.Join(root, guideRel)))
	assert.Equal(t, "setup", readFile(t, filepath.Join(root, setupRel)))
	// A failed staging allocation must still preserve the installed content.
	require.NoError(t, os.Remove(p.TempDir()))
	require.NoError(t, os.WriteFile(p.TempDir(), []byte("blocked"), 0o600))
	_, err = deposit(root, []byte("replacement guide"), []byte("replacement setup"), p)
	require.Error(t, err)
	assert.Equal(t, "guide", readFile(t, filepath.Join(root, guideRel)))
	assert.Equal(t, "setup", readFile(t, filepath.Join(root, setupRel)))
}

func TestBootstrapContentConcurrentRefusals(t *testing.T) {
	root := physicalTempDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, setupRel), 0o750))
	manifestBoundaryWrite(t, filepath.Join(root, guideRel), "unchanged guide\n")
	before := snapshotBootstrapContent(t, root)
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
		assert.ErrorContains(t, err, "bootstrap content "+setupRel+" is not a regular file")
	}
	assertBootstrapContentUnchanged(t, root, before)
}
