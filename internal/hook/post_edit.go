package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/redact"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// PostEdit emits edit feedback. Ethos errors are always advisory and fail open.
func PostEdit(ctx context.Context, input io.Reader, output io.Writer, agent ...string) {
	if Killed() {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			postEditAdvice(output, fmt.Sprintf("post-edit: %v", recovered))
		}
	}()
	payload, err := postEditInput(ctx, input)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		postEditAdvice(output, "post-edit input: "+err.Error())
		return
	}
	if len(agent) > 0 {
		diagnostic.SetHookIdentity(ctx, "", "", agent[0])
	}
	diagnostic.SetHookInput(ctx, payload)
	tool, _ := payload["tool_input"].(map[string]any)
	file, _ := tool["file_path"].(string)
	if file == "" {
		postEditAdvice(output, "post-edit input: tool_input.file_path is required")
		return
	}
	checkout, err := postEditCheckout(ctx)
	if err != nil {
		postEditAdvice(output, "post-edit scope: "+err.Error())
		return
	}
	root, file, err := postEditScope(checkout, file)
	if err != nil {
		postEditAdvice(output, "post-edit scope: "+err.Error())
		return
	}
	if root == "" {
		return
	}
	checks := defaultEditChecks(root, file)
	handle := ""
	if len(agent) > 0 {
		handle = agent[0]
	}
	configured, selected, err := configuredEditChecks(checkout, root, file, handle)
	if err != nil {
		postEditAdvice(output, "post-edit policy: "+err.Error())
		return
	}
	if selected {
		checks = configured
	}
	stage, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	results := make([]string, len(checks))
	var wg sync.WaitGroup
	for i, check := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = runLoggedCheck(stage, root, "edit", check)
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}
	var advisory, blocking []string
	for i, result := range results {
		if result != "" {
			if checks[i].onFail == "block" {
				blocking = append(blocking, result)
			} else {
				advisory = append(advisory, result)
			}
		}
	}
	if len(advisory) > 0 || len(blocking) > 0 {
		postEditFeedback(output, strings.Join(advisory, "\n\n"), strings.Join(blocking, "\n\n"))
	}
}

// postEditInput reads the payload, giving up when ctx ends: a cancelled hook
// must not wait out InputTimeout on a writer that never closes.
func postEditInput(ctx context.Context, input io.Reader) (map[string]any, error) {
	type read struct {
		payload map[string]any
		err     error
	}
	done := make(chan read, 1)
	go func() {
		payload, err := ReadInput(input, InputTimeout)
		done <- read{payload, err}
	}()
	select {
	case r := <-done:
		return r.payload, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func editRedact(message string) string {
	message = redact.HomePaths(redact.SecretsString(message))
	return redact.EmailPattern.ReplaceAllString(message, redact.EmailToken)
}

// Killed reports whether the operator kill switch disables punt hooks.
func Killed() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(home, ".punt-hooks-kill"))
	return err == nil
}

func postEditAdvice(out io.Writer, message string) {
	postEditFeedback(out, message, "")
}

func postEditFeedback(out io.Writer, advisory, blocking string) {
	response := make(map[string]any)
	if advisory != "" {
		response["hookSpecificOutput"] = map[string]string{"hookEventName": "PostToolUse", "additionalContext": editRedact(advisory)}
	}
	if blocking != "" {
		response["decision"], response["reason"] = "block", editRedact(blocking)
	}
	_ = json.NewEncoder(out).Encode(response)
}

func postEditCheckout(ctx context.Context) (string, error) {
	if _, err := gitenv.Output("", "git", "--version"); err != nil {
		return "", fmt.Errorf("git: %w", err)
	}
	return hookCheckout(ctx)
}

func hookStartDir(cwd string) string {
	start := os.Getenv("CLAUDE_PROJECT_DIR")
	if start == "" {
		start = cwd
	}
	return start
}

// RecordDisabledHookProject labels a killed hook without starting Git or loading policy.
func RecordDisabledHookProject(ctx context.Context) {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	checkout, store, err := resolve.CheckoutAndStoreWithoutGit(hookStartDir(cwd))
	if err == nil {
		diagnostic.SetHookProject(ctx, store, checkout)
	}
}

func hookCheckout(ctx context.Context) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	checkout, store, err := resolve.CheckoutAndStoreOf(hookStartDir(cwd))
	if err != nil {
		return "", err
	}
	diagnostic.SetHookProject(ctx, store, checkout)
	if checkout == "" {
		return filepath.EvalSymlinks(cwd)
	}
	return checkout, nil
}

func postEditRoot(checkout, file string) (string, string, error) {
	if !filepath.IsAbs(file) {
		// Preserve link/.. until EvalSymlinks has resolved it physically.
		file = checkout + string(filepath.Separator) + file
	}
	ancestor := file
	physical, err := filepath.EvalSymlinks(ancestor)
	for os.IsNotExist(err) {
		raw := strings.TrimRight(ancestor, string(filepath.Separator))
		if _, linkErr := os.Lstat(raw); !os.IsNotExist(linkErr) {
			return "", "", err
		}
		index := strings.LastIndexAny(raw, string(filepath.Separator))
		if index < 0 || raw[index+1:] == ".." {
			return "", "", err
		}
		parent := raw[:index+1]
		if parent == ancestor {
			return "", "", err
		}
		ancestor = parent
		physical, err = filepath.EvalSymlinks(ancestor)
	}
	if err != nil {
		return "", "", err
	}
	dir := filepath.Dir(physical)
	if ancestor != file {
		dir = physical
		physical = filepath.Join(physical, file[len(ancestor):])
	}
	root, err := resolve.CheckoutOf(dir)
	if err != nil {
		return "", "", err
	}
	if root == "" {
		return "", "", nil
	}
	if root != checkout {
		_, common, commonErr := resolve.HandGitDirs(checkout)
		if commonErr != nil {
			return "", "", commonErr
		}
		_, otherCommon, otherErr := resolve.HandGitDirs(root)
		if otherErr != nil {
			return "", "", otherErr
		}
		common, err = filepath.EvalSymlinks(common)
		if err != nil {
			return "", "", err
		}
		otherCommon, err = filepath.EvalSymlinks(otherCommon)
		if err != nil {
			return "", "", err
		}
		if common != otherCommon {
			return "", "", nil
		}
	}
	return root, physical, nil
}

func postEditScope(checkout, file string) (string, string, error) {
	root, physical, err := postEditRoot(checkout, file)
	if err != nil || root == "" {
		return root, physical, err
	}
	rel, err := filepath.Rel(root, physical)
	if err != nil {
		return "", "", err
	}
	rel = filepath.ToSlash(rel)
	if rel == ".tmp" || strings.HasPrefix(rel, ".tmp/") || rel == ".punt-labs/local" || strings.HasPrefix(rel, ".punt-labs/local/") {
		return "", "", nil
	}
	return root, physical, nil
}

type editCheck struct {
	name         string
	argv         []string
	failOnOutput bool
	onFail       string
	timeout      time.Duration
	cwd          string
	env          []string
	policy       *checkset.Check
}

func defaultEditChecks(root, file string) []editCheck {
	var checks []editCheck
	add := func(name string, failOnOutput bool, argv ...string) {
		if _, err := exec.LookPath(argv[0]); err == nil {
			checks = append(checks, editCheck{name: name, argv: argv, failOnOutput: failOnOutput})
		}
	}
	marker := func(name string) bool {
		info, err := os.Stat(filepath.Join(root, name))
		return err == nil && info.Mode().IsRegular()
	}
	switch filepath.Ext(file) {
	case ".go":
		if marker("go.mod") {
			add("gofmt", true, "gofmt", "-l", file)
			dir, _ := filepath.Rel(root, filepath.Dir(file))
			if dir == "." {
				dir = ""
			}
			add("go vet", false, "go", "vet", "./"+filepath.ToSlash(dir))
		}
	case ".py":
		if marker("pyproject.toml") {
			if _, err := exec.LookPath("ruff"); err == nil {
				add("ruff", false, "ruff", "check", file)
			} else {
				add("ruff", false, "uv", "run", "ruff", "check", file)
			}
		}
	case ".sh":
		add("shellcheck", false, "shellcheck", file)
	}
	return checks
}

// editTail bounds captured diagnostics even when a tool floods a single line.
type editTail struct{ data []byte }

func (b *editTail) Write(p []byte) (int, error) {
	n := len(p)
	const limit = 256 << 10
	if len(p) >= limit {
		b.data = append(b.data[:0], p[len(p)-limit:]...)
	} else {
		if len(b.data)+len(p) > limit {
			b.data = b.data[len(b.data)+len(p)-limit:]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}

func runLoggedCheck(ctx context.Context, root, stage string, check editCheck) string {
	started := time.Now()
	failure := runEditCheck(ctx, root, check)
	outcome := "completed"
	if failure != "" {
		outcome = "failed"
	}
	diagnostic.LogHookCheck(ctx, root, check.cwd, stage, check.name, outcome, time.Since(started))
	return failure
}

func runEditCheck(stage context.Context, root string, check editCheck) string {
	if len(check.argv) == 0 {
		return check.name + ": command has no executable after placeholder expansion"
	}
	timeout := check.timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(stage, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, check.argv[0], check.argv[1:]...)
	cmd.Dir = root
	if check.cwd != "" {
		cmd.Dir = check.cwd
	}
	if check.policy != nil {
		if err := check.policy.Probe(cmd.Dir); err != nil {
			return check.name + ": " + err.Error()
		}
	}
	cmd.Env = gitenv.Parsed()
	if check.env != nil {
		cmd.Env = check.env
	}
	cmd.WaitDelay = time.Second
	stopGroup := editProcessGroup(cmd)
	defer stopGroup()
	var stdout, stderr editTail
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() == nil && err == nil && (!check.failOnOutput || len(stdout.data) == 0) {
		return ""
	}
	detail := "non-empty formatting output"
	if err != nil {
		detail = err.Error()
	}
	if ctx.Err() != nil {
		detail = "timed out: " + ctx.Err().Error()
	}
	lines := strings.Split(strings.TrimRight(string(bytes.Join([][]byte{stdout.data, stderr.data}, []byte("\n"))), "\n"), "\n")
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	return fmt.Sprintf("%s: %s\ncommand: %s\n%s", check.name, detail, strings.Join(check.argv, " "), strings.Join(lines, "\n"))
}
