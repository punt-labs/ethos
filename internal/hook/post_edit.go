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

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/redact"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// PostEdit provides advisory feedback only; errors never block the next edit.
func PostEdit(ctx context.Context, input io.Reader, output io.Writer) {
	defer func() {
		if recovered := recover(); recovered != nil {
			postEditAdvice(output, fmt.Sprintf("post-edit: %v", recovered))
		}
	}()
	payload, err := ReadInput(input, InputTimeout)
	if err != nil {
		postEditAdvice(output, "post-edit input: "+err.Error())
		return
	}
	tool, _ := payload["tool_input"].(map[string]any)
	file, _ := tool["file_path"].(string)
	if file == "" {
		postEditAdvice(output, "post-edit input: tool_input.file_path is required")
		return
	}
	root, file, err := postEditScope(file)
	if err != nil {
		postEditAdvice(output, "post-edit scope: "+err.Error())
		return
	}
	if root == "" {
		return
	}
	checks := defaultEditChecks(root, file)
	stage, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	results := make([]string, len(checks))
	var wg sync.WaitGroup
	for i, check := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = runEditCheck(stage, root, check)
		}()
	}
	wg.Wait()
	var failures []string
	for _, result := range results {
		if result != "" {
			failures = append(failures, result)
		}
	}
	if len(failures) > 0 {
		postEditAdvice(output, strings.Join(failures, "\n\n"))
	}
}

func postEditAdvice(out io.Writer, message string) {
	message = redact.HomePaths(redact.SecretsString(message))
	message = redact.EmailPattern.ReplaceAllString(message, redact.EmailToken)
	_ = json.NewEncoder(out).Encode(map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName": "PostToolUse", "additionalContext": message,
		},
	})
}

func postEditScope(file string) (string, string, error) {
	if _, err := gitenv.Output("", "git", "--version"); err != nil {
		return "", "", fmt.Errorf("git: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", err
	}
	start := os.Getenv("CLAUDE_PROJECT_DIR")
	if start == "" {
		start = cwd
	}
	checkout, err := resolve.CheckoutOf(start)
	if err != nil {
		return "", "", err
	}
	if checkout == "" {
		checkout = cwd
	}
	checkout, err = filepath.EvalSymlinks(checkout)
	if err != nil {
		return "", "", err
	}
	if !filepath.IsAbs(file) {
		// Preserve link/.. until EvalSymlinks has resolved it physically.
		file = checkout + string(filepath.Separator) + file
	}
	physical, err := filepath.EvalSymlinks(file)
	if os.IsNotExist(err) {
		index := strings.LastIndexAny(file, string(filepath.Separator))
		var parent string
		parent, err = filepath.EvalSymlinks(file[:index+1])
		physical = filepath.Join(parent, file[index+1:])
	}
	if err != nil {
		return "", "", err
	}
	root, err := resolve.CheckoutOf(filepath.Dir(physical))
	if err != nil {
		return "", "", err
	}
	if root == "" {
		return "", "", nil
	}
	if root != checkout {
		_, common, commonErr := resolve.HandGitDirs(checkout)
		if commonErr != nil {
			return "", "", nil
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
}

func defaultEditChecks(root, file string) []editCheck {
	var checks []editCheck
	add := func(name string, failOnOutput bool, argv ...string) {
		if _, err := exec.LookPath(argv[0]); err == nil {
			checks = append(checks, editCheck{name, argv, failOnOutput})
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

func runEditCheck(stage context.Context, root string, check editCheck) string {
	ctx, cancel := context.WithTimeout(stage, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, check.argv[0], check.argv[1:]...)
	cmd.Dir = root
	cmd.Env = gitenv.Parsed()
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
