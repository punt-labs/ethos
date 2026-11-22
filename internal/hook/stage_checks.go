package hook

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/punt-labs/ethos/v4/internal/gitenv"
)

// StageFeedback separates advisory output from failures that block progress.
type StageFeedback struct {
	Advisory string
	Blocking string
}

// RunStageChecks runs configured commit or handoff checks, without marker defaults.
func RunStageChecks(ctx context.Context, checkout, agent, stage string) StageFeedback {
	return runStageChecks(ctx, checkout, agent, stage, func() ([]string, error) { return stageFiles(checkout, stage) })
}

func runStageChecks(ctx context.Context, checkout, agent, stage string, filesForStage func() ([]string, error)) StageFeedback {
	return runStageRootChecks(ctx, checkout, agent, stage, func() (map[string][]string, error) {
		files, err := filesForStage()
		return map[string][]string{checkout: files}, err
	})
}

func runStageRootChecks(ctx context.Context, checkout, agent, stage string, filesForStage func() (map[string][]string, error)) StageFeedback {
	if postEditKilled() {
		return StageFeedback{}
	}
	cap := 15 * time.Minute
	if stage == "handoff" {
		cap = 30 * time.Minute
	} else if stage != "commit" {
		return StageFeedback{Blocking: "checks: unsupported stage " + stage}
	}
	ctx, cancel := context.WithTimeout(ctx, cap)
	defer cancel()
	var checks []checkset.Check
	var err error
	var env []string
	if stage == "commit" {
		checks, _, err = configuredStageChecks(checkout, agent, stage)
		if err != nil {
			return StageFeedback{Blocking: editRedact(stage + " policy: " + err.Error())}
		}
		if len(checks) == 0 {
			return StageFeedback{}
		}
		env, err = gitenv.ActualIndexEnv()
		if err != nil {
			return StageFeedback{Blocking: editRedact(stage + " index: " + err.Error())}
		}
		if index := os.Getenv("GIT_INDEX_FILE"); index != "" && !filepath.IsAbs(index) {
			env = gitenv.WithIndex(env, filepath.Join(checkout, index))
		}
	}
	filesByRoot, err := filesForStage()
	if err != nil {
		return StageFeedback{Blocking: editRedact(stage + " files: " + err.Error())}
	}
	var advisory, blocking []string
	roots := make([]string, 0, len(filesByRoot))
	for root := range filesByRoot {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	if stage == "commit" {
		for _, root := range roots {
			partial, err := partiallyStagedFiles(root, filesByRoot[root], checks, env)
			if err != nil {
				blocking = append(blocking, "commit files: "+err.Error())
				continue
			}
			for _, file := range partial {
				blocking = append(blocking, fmt.Sprintf("ethos: %q has unstaged changes; commit checks read files on disk. Stage or stash them first (git add -- <file> / git stash --keep-index).", file))
			}
		}
		if len(blocking) > 0 {
			return StageFeedback{Blocking: editRedact(strings.Join(blocking, "\n"))}
		}
	}
	for _, root := range roots {
		rootChecks := checks
		if stage == "handoff" {
			rootChecks, _, err = configuredStageChecks(root, agent, stage)
			if err != nil {
				blocking = append(blocking, stage+" policy: "+err.Error())
				continue
			}
		}
		for _, check := range rootChecks {
			for _, invocation := range stageInvocations(root, filesByRoot[root], check) {
				invocation.env = env
				if failure := runEditCheck(ctx, root, invocation); failure != "" {
					if check.OnFail == "block" {
						blocking = append(blocking, failure)
					} else {
						advisory = append(advisory, failure)
					}
				}
			}
		}
	}
	return StageFeedback{Advisory: editRedact(strings.Join(advisory, "\n\n")), Blocking: editRedact(strings.Join(blocking, "\n\n"))}
}

func partiallyStagedFiles(root string, files []string, checks []checkset.Check, env []string) ([]string, error) {
	matched := map[string]bool{}
	for _, file := range files {
		for _, check := range checks {
			if check.Files.All || check.Matches(filepath.ToSlash(file)) {
				matched[file] = true
				break
			}
		}
	}
	if len(matched) == 0 {
		return nil, nil
	}
	data, err := gitenv.OutputEnv(root, "git", env, "diff", "--name-only", "-z", "--no-ext-diff", "--no-textconv", "--no-renames", "--ignore-submodules=none")
	if err != nil {
		return nil, fmt.Errorf("git diff unstaged files: %w", err)
	}
	var partial []string
	for _, file := range strings.Split(string(data), "\x00") {
		if matched[file] {
			partial = append(partial, file)
		}
	}
	sort.Strings(partial)
	return partial, nil
}

func stageFiles(root, stage string) ([]string, error) {
	args := []string{"diff", "--cached", "--name-only", "-z", "--diff-filter=ACMR"}
	env := gitenv.Parsed()
	if stage == "commit" {
		var err error
		env, err = gitenv.ActualIndexEnv()
		if err != nil {
			return nil, err
		}
	} else {
		args = []string{"diff", "--name-only", "-z", "--diff-filter=ACMR"}
	}
	data, err := gitenv.OutputEnv(root, "git", env, args...)
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	if stage == "handoff" {
		staged, err := gitenv.Output(root, "git", "diff", "--cached", "--name-only", "-z", "--diff-filter=ACMR")
		if err != nil {
			return nil, err
		}
		data = append(data, staged...)
		untracked, err := gitenv.Output(root, "git", "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			return nil, err
		}
		data = append(data, untracked...)
	}
	seen := map[string]bool{}
	var files []string
	for _, file := range strings.Split(string(data), "\x00") {
		if file != "" && !seen[file] {
			seen[file] = true
			files = append(files, file)
		}
	}
	sort.Strings(files)
	return files, nil
}

func stageInvocations(root string, files []string, check checkset.Check) []editCheck {
	var matched []string
	for _, file := range files {
		if check.Matches(filepath.ToSlash(file)) {
			matched = append(matched, filepath.Join(root, file))
		}
	}
	if len(matched) == 0 && !check.Files.All {
		return nil
	}
	groups := [][]string{matched}
	if check.Cwd != "root" && len(matched) > 0 {
		groups = nil
		byDir := map[string]int{}
		for _, file := range matched {
			dir := filepath.Dir(file)
			i, ok := byDir[dir]
			if !ok {
				i = len(groups)
				byDir[dir] = i
				groups = append(groups, nil)
			}
			groups[i] = append(groups[i], file)
		}
	}
	var invocations []editCheck
	effective, err := check.Argv()
	if err != nil {
		return []editCheck{{name: check.Name, argv: []string{}, policy: &check}}
	}
	for _, group := range groups {
		cwd := root
		var packages []string
		seen := map[string]bool{}
		for _, file := range group {
			dir, _ := filepath.Rel(root, filepath.Dir(file))
			pkg := "./"
			if dir != "." {
				pkg += filepath.ToSlash(dir)
			}
			if !seen[pkg] {
				seen[pkg] = true
				packages = append(packages, pkg)
			}
		}
		if len(packages) == 0 {
			packages = []string{"./"}
		}
		if check.Cwd != "root" && len(group) > 0 {
			cwd = filepath.Dir(group[0])
		}
		var argv []string
		for _, arg := range effective {
			switch arg {
			case "{files}":
				argv = append(argv, group...)
			case "{package_dir}":
				argv = append(argv, packages...)
			case "{root}":
				argv = append(argv, root)
			default:
				argv = append(argv, arg)
			}
		}
		invocations = append(invocations, editCheck{name: check.Name, argv: argv, cwd: cwd, timeout: check.Duration(), onFail: check.OnFail, failOnOutput: check.FailOnOutput, policy: &check})
	}
	return invocations
}
