package doctor

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/punt-labs/ethos/internal/vendor"
)

// GitignoreRule is the pattern that keeps `.local.yaml` companions out
// of git. Vendor and setup emit it; this file asserts it.
const GitignoreRule = ".punt-labs/ethos/**/*.local.yaml"

// CheckRepoSetComplete is the authoritative completeness gate for a repo
// running `resolution: repo-only` (DES-057 Part A).
//
// Every other surface is deliberately softer — session-start degrades so
// a live session is never bricked, live Load stays additive on
// extensions — which means an incomplete set can run for a long time
// while quietly resolving less than it should. This check is where that
// stops being invisible. It runs the same predicate `ethos vendor` runs
// on its own output, so a set vendor called complete and a set doctor
// calls complete are the same thing by construction.
//
// A repo in layered mode PASSes with "not applicable": the global
// fallback is expected to catch the tail there, so an incomplete repo
// layer is not a fault.
func CheckRepoSetComplete(s identity.IdentityStore, storeRoot string) Result {
	name := "Repo-only completeness"

	if storeRoot == "" {
		return Result{Name: name, Status: "PASS", Detail: "not in a repo"}
	}
	mode, err := resolve.ResolveResolution(storeRoot)
	if err != nil {
		return Result{Name: name, Status: "FAIL", Detail: err.Error()}
	}
	if mode != resolve.ResolutionRepoOnly {
		return Result{Name: name, Status: "PASS", Detail: "resolution: layered — not applicable"}
	}

	// Check the layers the STORE reads, not a hardcoded
	// .punt-labs/ethos/. repo-only is legal with identities supplied
	// entirely by a repo-local bundle and no .punt-labs/ethos/ directory
	// at all; assuming that path failed a layout that resolves perfectly
	// at runtime (Bugbot, PR #410).
	roots := readRoots(s, storeRoot)
	if len(roots) == 0 {
		return Result{
			Name: name, Status: "FAIL",
			Detail: fmt.Sprintf("resolution: repo-only but neither %s nor an active repo-local bundle exists",
				filepath.Join(storeRoot, ".punt-labs", "ethos")),
		}
	}

	rep, err := vendor.Check(roots...)
	if err != nil {
		return Result{Name: name, Status: "FAIL", Detail: err.Error()}
	}
	if !rep.Complete() {
		// Name a command that actually runs. `ethos vendor --apply` with
		// no seeds exits "no identities selected"; the repair needs a seed
		// set, and the handles this repo already holds are exactly it
		// (Bugbot HIGH, PR #410).
		return Result{Name: name, Status: "FAIL",
			Detail: rep.Summary() + " — run `" + repairCommand(rep) + "` to complete the set"}
	}
	if rep.ExtUnverifiable {
		// Not a fault: a hand-authored set is legal. But the limit must be
		// visible, or a green check would imply a guarantee doctor cannot
		// make about extensions.
		return Result{Name: name, Status: "WARN", Detail: rep.Summary()}
	}
	return Result{Name: name, Status: "PASS", Detail: rep.Summary()}
}

// readRoots returns the layers repo-only resolution consults, in
// precedence order, skipping any that does not exist on disk.
//
// It asks the store, which already resolved the repo layer and the
// active bundle, rather than re-deriving the paths — a second derivation
// is a second chance to disagree with what resolution actually does. It
// falls back to the conventional path only for a store that cannot
// answer (a plain global store, in tests).
func readRoots(s identity.IdentityStore, storeRoot string) []string {
	var candidates []string
	if ls, ok := s.(*identity.LayeredStore); ok {
		candidates = []string{ls.RepoRoot(), ls.BundleRoot()}
	} else {
		candidates = []string{filepath.Join(storeRoot, ".punt-labs", "ethos")}
	}
	var roots []string
	for _, r := range candidates {
		if r == "" {
			continue
		}
		if info, err := os.Stat(r); err == nil && info.IsDir() {
			roots = append(roots, r)
		}
	}
	return roots
}

// repairCommand builds the `ethos vendor` invocation that completes this
// set. It seeds from the handles the set already holds, so re-running it
// re-walks the closure and fills whatever went missing. It falls back to
// `--all` when the set holds no readable identity to name — the only
// other seed source that needs no argument from the operator.
func repairCommand(rep *vendor.Report) string {
	if len(rep.Handles) == 0 {
		return "ethos vendor --all --apply"
	}
	return "ethos vendor " + strings.Join(rep.Handles, " ") + " --apply"
}

// CheckLocalExtNotTracked enforces DES-057 Part C's git boundary: the
// `.local.yaml` half of an extension namespace must not reach git.
//
// Two failures, in the order that matters. A tracked `.local.yaml` is a
// FAIL and says so first, because .gitignore does NOT untrack a file
// already in the index — adding the rule would leave the file committed
// and the repo looking clean. A missing rule alone is a WARN: nothing is
// exposed yet, but the next `ethos ext set --local` would be.
func CheckLocalExtNotTracked(repoRoot string) Result {
	name := "Local extension files"

	if repoRoot == "" {
		return Result{Name: name, Status: "PASS", Detail: "not in a repo"}
	}

	// A git failure means the check did not run. PASS would report that
	// `.local.yaml` tracking was verified when nothing was verified —
	// a false all-clear on secret-bearing files is the one answer this
	// check must never give.
	tracked, err := trackedLocalExt(repoRoot)
	if err != nil {
		return Result{Name: name, Status: "WARN", Detail: "could not verify: " + err.Error()}
	}
	if len(tracked) > 0 {
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
			"%s already tracked by git — gitignore does not untrack; run: git rm --cached %s",
			strings.Join(tracked, ", "), strings.Join(tracked, " "))}
	}

	ignored, err := gitignoreCovers(repoRoot)
	if err != nil {
		return Result{Name: name, Status: "WARN", Detail: "could not verify: " + err.Error()}
	}
	if !ignored {
		return Result{Name: name, Status: "WARN", Detail: fmt.Sprintf(
			"no .gitignore rule for %s — add it before running `ethos ext set --local`", GitignoreRule)}
	}
	return Result{Name: name, Status: "PASS", Detail: "ignored and untracked"}
}

// trackedLocalExt lists `*.local.yaml` files under .punt-labs/ethos/
// that git has in its index.
func trackedLocalExt(repoRoot string) ([]string, error) {
	out, err := exec.Command("git", "-C", repoRoot, "ls-files",
		"--", ".punt-labs/ethos/**/*.local.yaml", ".punt-labs/ethos/*.local.yaml").Output()
	if err != nil {
		return nil, fmt.Errorf("could not ask git for tracked files: %v", err)
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	sort.Strings(files)
	return files, nil
}

// gitignoreCovers asks git whether a representative `.local.yaml` path
// is ignored. Asking git rather than grepping .gitignore means any
// spelling of the rule counts, and a rule in .git/info/exclude or a
// parent .gitignore counts too.
func gitignoreCovers(repoRoot string) (bool, error) {
	probe := filepath.Join(".punt-labs", "ethos", "identities", "probe.ext", "quarry.local.yaml")
	cmd := exec.Command("git", "-C", repoRoot, "check-ignore", "-q", probe)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	// git check-ignore exits 1 for "not ignored" and 128 for a real
	// failure; only the latter is an error worth reporting.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("could not ask git about ignore rules: %v", err)
}

// CheckExtCredentialNames runs the same name-only credential lint
// `ethos vendor` blocks on, advisory here.
//
// Vendor blocks because it is the command that puts bytes into git.
// Doctor only reports: it is a health check, and a repo whose extensions
// were authored before the lint existed should be told, not stopped.
func CheckExtCredentialNames(s identity.IdentityStore) Result {
	name := "Extension key names"

	if s == nil {
		return Result{Name: name, Status: "PASS", Detail: "no identity store"}
	}
	list, err := s.List()
	if err != nil {
		return Result{Name: name, Status: "PASS", Detail: fmt.Sprintf("could not list identities: %v", err)}
	}

	var flagged []string
	for _, id := range list.Identities {
		namespaces, nsErr := s.ExtList(id.Handle)
		if nsErr != nil {
			continue
		}
		for _, ns := range namespaces {
			// BASE keys only, never the merged view. The remedy this
			// check prints is "move it to <ns>.local.yaml"; reading the
			// merged view would keep flagging the key after the user did
			// exactly that, so following the advice would never clear the
			// warning and the check would become noise.
			keys, keyErr := vendor.BaseExtKeys(filepath.Join(s.ExtDir(id.Handle), ns+".yaml"))
			if keyErr != nil {
				continue
			}
			for _, key := range keys {
				if vendor.Classify(key) == vendor.Block {
					flagged = append(flagged, fmt.Sprintf("%s %s/%s", id.Handle, ns, key))
				}
			}
		}
	}
	if len(flagged) == 0 {
		return Result{Name: name, Status: "PASS", Detail: "no credential-named keys"}
	}
	sort.Strings(flagged)
	return Result{Name: name, Status: "WARN", Detail: fmt.Sprintf(
		"credential-named: %s — move each to a <namespace>.local.yaml (`ethos ext set ... --local`); `ethos vendor` will refuse them",
		strings.Join(flagged, ", "))}
}
