package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

func handoffRenames(root string) (map[string]string, error) {
	pathData, err := gitenv.Output(root, "git", "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return nil, err
	}
	index, err := gitenv.IndexPathOutput(pathData)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", layout.ScratchDirPattern("ethos-handoff-index"))
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	privateIndex := filepath.Join(dir, "index")
	env := gitenv.WithIndex(gitenv.Parsed(), privateIndex)
	data, err := os.ReadFile(index)
	if err == nil {
		if err := os.WriteFile(privateIndex, data, 0o600); err != nil {
			return nil, err
		}
	} else if os.IsNotExist(err) {
		if _, err := gitenv.OutputEnv(root, "git", env, "read-tree", "--empty"); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	untracked, err := gitenv.OutputEnv(root, "git", env, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	if len(untracked) > 0 {
		_, err = gitenv.Run(gitenv.Spec{
			Git: "git", Dir: root, Env: env, Stdin: strings.NewReader(string(untracked)),
			Args: []string{"--literal-pathspecs", "-c", "core.splitIndex=false", "add", "--intent-to-add", "--pathspec-from-file=-", "--pathspec-file-nul"},
		})
		if err != nil {
			return nil, err
		}
	}
	data, err = gitenv.OutputEnv(root, "git", env, "diff", "--find-renames", "--name-status", "-z", "--no-ext-diff", "--no-textconv", "HEAD", "--")
	if err != nil {
		return nil, err
	}
	return parseHandoffRenames(string(data))
}

func parseHandoffRenames(data string) (map[string]string, error) {
	if data != "" && !strings.HasSuffix(data, "\x00") {
		return nil, fmt.Errorf("unterminated git rename status")
	}
	result := map[string]string{}
	fields := strings.Split(data, "\x00")
	for i := 0; i < len(fields)-1; {
		status := fields[i]
		i++
		count := 1
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			count = 2
		}
		if status == "" || i+count >= len(fields) {
			return nil, fmt.Errorf("invalid git rename status")
		}
		for _, path := range fields[i : i+count] {
			if path == "" {
				return nil, fmt.Errorf("empty git rename path")
			}
		}
		if strings.HasPrefix(status, "R") {
			result[fields[i]] = fields[i+1]
		}
		i += count
	}
	return result, nil
}
