package enable

import (
	"context"
	"fmt"
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

const ignoreContentChildRoot = "ETHOS_TEST_IGNORE_CONTENT_ROOT"

// Exercise existing callers, not the new guard directly: the exact same tests
// compile and assert normal refusal under an overlay of the intake production.
func invokeIgnoreContent(t *testing.T, api, checkout, store, host string) error {
	t.Helper()
	if strings.HasPrefix(api, "EnableTo") {
		rep, err := EnableTo(checkout, store)
		require.NotNil(t, rep)
		assert.Empty(t, rep.Steps, "refusal must precede deposit and every mutation")
		assert.Empty(t, rep.Warnings)
		return err
	}
	p, err := layout.NewRepository(store, checkout, layout.RecordBlame)
	require.NoError(t, err)
	switch api {
	case "preflight", "retirement":
		return preflightLegacyIgnores(host)
	case "ensure", "ensure-retirement":
		_, _, err = ensureGitignoreWithIO(host, p, defaultGitignoreIO())
	case "LocalIgnored":
		_, err = LocalIgnored(host)
	case "EnsureLocalIgnored":
		_, err = ensureLocalIgnoredWithIO(host, p, defaultGitignoreIO())
	case "unnegated":
		_, err = unnegatedLocalIgnore(host)
	default:
		t.Fatalf("unknown ignore API %q", api)
	}
	return err
}

func ignoreContentChild(t *testing.T, api, checkout, store, host, rel, diagnostic string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestIgnoreContentRefusesHazards$")
	cmd.Env = append(os.Environ(), ignoreContentChildRoot+"="+checkout,
		"ETHOS_TEST_IGNORE_CONTENT_STORE="+store,
		"ETHOS_TEST_IGNORE_CONTENT_HOST="+host,
		"ETHOS_TEST_IGNORE_CONTENT_API="+api,
		"ETHOS_TEST_IGNORE_CONTENT_REL="+rel,
		"ETHOS_TEST_IGNORE_CONTENT_DIAGNOSTIC="+diagnostic)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	t.Logf("child api=%s source=%s elapsed=%s deadline=%v exit=%v output=%s", api, rel, time.Since(start), ctx.Err(), err, out)
	assert.NoError(t, ctx.Err(), "timeout/kill is the availability defect, not refusal")
	assert.NoError(t, err, "refusal must be a normal child test exit, not a crash or signal: %s", out)
	assert.Contains(t, string(out), "IGNORE_CONTENT_REFUSED "+rel)
}

func ignoreContentHazard(t *testing.T, host, outside, rel, kind string) {
	t.Helper()
	path := filepath.Join(host, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	if _, err := os.Lstat(path); err == nil {
		require.NoError(t, os.Remove(path))
	} else {
		require.True(t, os.IsNotExist(err), "%v", err)
	}
	switch kind {
	case "guide-fifo-link", "setup-fifo-link":
		leaf := guideRel
		if kind == "setup-fifo-link" {
			leaf = setupRel
		}
		bootstrapContentHazard(t, host, outside, leaf, "fifo")
		target, err := filepath.Rel(filepath.Dir(path), filepath.Join(host, leaf))
		require.NoError(t, err)
		require.NoError(t, os.Symlink(target, path))
	default:
		bootstrapContentHazard(t, host, outside, rel, kind)
	}
}

func TestIgnoreContentRefusesHazards(t *testing.T) {
	if checkout := os.Getenv(ignoreContentChildRoot); checkout != "" {
		err := invokeIgnoreContent(t, os.Getenv("ETHOS_TEST_IGNORE_CONTENT_API"), checkout,
			os.Getenv("ETHOS_TEST_IGNORE_CONTENT_STORE"), os.Getenv("ETHOS_TEST_IGNORE_CONTENT_HOST"))
		require.ErrorContains(t, err, "ignore source")
		require.ErrorContains(t, err, filepath.FromSlash(os.Getenv("ETHOS_TEST_IGNORE_CONTENT_REL")))
		require.ErrorContains(t, err, os.Getenv("ETHOS_TEST_IGNORE_CONTENT_DIAGNOSTIC"))
		fmt.Println("IGNORE_CONTENT_REFUSED " + os.Getenv("ETHOS_TEST_IGNORE_CONTENT_REL"))
		return
	}
	for _, rel := range []string{".gitignore", ".punt-labs/.gitignore"} {
		apis := []string{"EnableTo", "EnableTo-linked-C", "EnableTo-linked-S", "unnegated"}
		if rel == ".gitignore" {
			apis = append(apis, "preflight", "ensure", "LocalIgnored", "EnsureLocalIgnored")
		} else {
			apis = append(apis, "retirement", "ensure-retirement")
		}
		for _, api := range apis {
			for _, kind := range []string{"fifo", "guide-fifo-link", "setup-fifo-link", "external-fifo-link", "regular-link", "dangling-link", "directory"} {
				t.Run(strings.ReplaceAll(rel, "/", "_")+"/"+api+"/"+kind, func(t *testing.T) {
					parent, checkout, store := manifestBoundaryRoots(t, strings.Contains(api, "-linked-"))
					host := checkout
					if api == "EnableTo-linked-S" {
						host = store
					}
					outside := filepath.Join(parent, "outside")
					manifestBoundaryWrite(t, filepath.Join(outside, "sentinel"), "external sentinel\n")
					if rel != ".gitignore" {
						// Force the actual retirement path into unnegatedLocalIgnore:
						// ls-files with the retiring rule must return a canonical-local
						// file. Merely adding a nested ignore would not reach that read.
						manifestBoundaryWrite(t, filepath.Join(host, ".gitignore"), legacyGitignoreMarker+"\n.punt-labs/**/*.local.*\n")
						manifestBoundaryWrite(t, filepath.Join(host, ".punt-labs/local/ethos/private.local.yaml"), "private fixture\n")
						out := gitOutput(t, host, "ls-files", "-z", "--cached", "--others", "--ignored", "--exclude=.punt-labs/**/*.local.*", "--", ".punt-labs/")
						require.Contains(t, out, ".punt-labs/local/ethos/private.local.yaml\x00", "retirement trigger must exist before installing hazard")
					}
					ignoreContentHazard(t, host, outside, rel, kind)
					before := snapshotBootstrapContent(t, parent)
					ignoreContentChild(t, api, checkout, store, host, rel, "not a regular file")
					assertBootstrapContentUnchanged(t, parent, before)
					for _, root := range []string{checkout, store} {
						assert.NoFileExists(t, filepath.Join(root, manifestRel), "no deposit")
						assert.NoFileExists(t, filepath.Join(root, markerRel), "no enablement marker")
					}
				})
			}
		}
	}
}

func TestIgnoreContentInternalAncestorRefusal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures require privileges on Windows; cross-vet checks compilation")
	}
	for _, kind := range []string{"directory-link", "dangling-link", "file", "fifo-link"} {
		t.Run(kind, func(t *testing.T) {
			parent, checkout, store := manifestBoundaryRoots(t, false)
			outside := filepath.Join(parent, "outside")
			target := outside
			if kind == "directory-link" {
				manifestBoundaryWrite(t, filepath.Join(outside, "sentinel"), "unchanged\n")
				bootstrapContentHazard(t, outside, outside, ".gitignore", "fifo")
			} else if kind == "fifo-link" {
				target = filepath.Join(parent, "ancestor-fifo")
				bootstrapContentHazard(t, parent, outside, "ancestor-fifo", "fifo")
			}
			path := filepath.Join(checkout, ".punt-labs")
			if kind == "file" {
				manifestBoundaryWrite(t, path, "not a directory\n")
			} else {
				require.NoError(t, os.Symlink(target, path))
			}
			before := snapshotBootstrapContent(t, parent)
			ignoreContentChild(t, "unnegated", checkout, store, checkout, ".punt-labs", "not a physical directory")
			assertBootstrapContentUnchanged(t, parent, before)
		})
	}
}

func TestIgnoreContentControls(t *testing.T) {
	for _, initial := range []string{"missing", "regular", "nested", "negated"} {
		t.Run(initial, func(t *testing.T) {
			parent, checkout, store := manifestBoundaryRoots(t, false)
			require.NoError(t, os.Remove(filepath.Join(checkout, ".gitignore")))
			if initial != "missing" {
				manifestBoundaryWrite(t, filepath.Join(checkout, ".gitignore"), LocalIgnoreRule+"\n")
			}
			if initial == "nested" || initial == "negated" {
				data := "local/\r\n"
				if initial == "negated" {
					data = "!local/\n"
				}
				manifestBoundaryWrite(t, filepath.Join(checkout, ".punt-labs/.gitignore"), data)
			}
			before := snapshotBootstrapContent(t, parent)
			require.NoError(t, preflightLegacyIgnores(checkout))
			ok, err := unnegatedLocalIgnore(checkout)
			require.NoError(t, err)
			assert.Equal(t, initial != "negated", ok)
			covered, err := LocalIgnored(checkout)
			require.NoError(t, err)
			assert.Equal(t, initial != "missing" && initial != "negated", covered)
			assertBootstrapContentUnchanged(t, parent, before)
			if initial == "negated" {
				return // conflicting rules are operator-owned, not a writer control
			}
			p, err := layout.NewRepository(store, checkout, layout.RecordBlame)
			require.NoError(t, err)
			_, _, err = ensureGitignoreWithIO(checkout, p, defaultGitignoreIO())
			require.NoError(t, err)
			_, err = EnsureLocalIgnored(checkout, p)
			require.NoError(t, err)
			rep, err := EnableTo(checkout, store)
			require.NoError(t, err)
			require.NotEmpty(t, rep.Steps)
			assert.FileExists(t, filepath.Join(checkout, markerRel))
		})
	}
}

func TestIgnoreContentConcurrentRefusals(t *testing.T) {
	parent, checkout, _ := manifestBoundaryRoots(t, false)
	ignoreContentHazard(t, checkout, filepath.Join(parent, "outside"), ".gitignore", "directory")
	before := snapshotBootstrapContent(t, parent)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { errs <- preflightLegacyIgnores(checkout) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.ErrorContains(t, err, "not a regular file")
	}
	assertBootstrapContentUnchanged(t, parent, before)
}
