package hook

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

const (
	handoffUntrackedPaths = 512
	handoffUntrackedBytes = 64 << 10
	handoffRenameTimeout  = time.Second
)

type handoffUntrackedBuffer struct {
	data     bytes.Buffer
	exceeded bool
}

func (b *handoffUntrackedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	keep := min(n, handoffUntrackedBytes-b.data.Len())
	if keep < n {
		b.exceeded = true
	}
	_, _ = b.data.Write(p[:keep])
	return n, nil
}

func handoffRenames(root string) (map[string]string, error) {
	store, err := resolve.StoreOf(root)
	if err != nil {
		return nil, err
	}
	cfg, err := resolve.ConfigOf(store)
	if err != nil {
		return nil, err
	}
	policy, err := (resolve.Located{Store: store, Checkout: root, Config: cfg}).Layout()
	if err != nil {
		return nil, err
	}
	if err := layout.EnsureLocalDir(policy, policy.TempDir()); err != nil {
		return nil, err
	}
	pathData, err := gitenv.Output(root, "git", "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return nil, err
	}
	index, err := gitenv.IndexPathOutput(pathData)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(policy.TempDir(), layout.ScratchDirPattern("ethos-handoff-index"))
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
	var untracked handoffUntrackedBuffer
	_, err = gitenv.Run(gitenv.Spec{
		Git: "git", Dir: root, Env: env, Stdout: &untracked, Limit: handoffRenameTimeout,
		Args: []string{"ls-files", "--others", "--exclude-standard", "--exclude=/.punt-labs/local/", "-z"},
	})
	if err != nil {
		return nil, err
	}
	if untracked.exceeded || bytes.Count(untracked.data.Bytes(), []byte{0}) > handoffUntrackedPaths {
		return nil, fmt.Errorf("untracked rename budget exceeded (%d paths or %d bytes); inference skipped", handoffUntrackedPaths, handoffUntrackedBytes)
	}
	if untracked.data.Len() > 0 {
		_, err = gitenv.Run(gitenv.Spec{
			Git: "git", Dir: root, Env: env, Stdin: bytes.NewReader(untracked.data.Bytes()), Limit: handoffRenameTimeout,
			Args: []string{"--literal-pathspecs", "-c", "core.splitIndex=false", "add", "--intent-to-add", "--pathspec-from-file=-", "--pathspec-file-nul"},
		})
		if err != nil {
			return nil, err
		}
	}
	data, err = gitenv.Run(gitenv.Spec{
		Git: "git", Dir: root, Env: env, Limit: handoffRenameTimeout,
		Args: []string{"diff", "--find-renames", "--name-status", "-z", "--no-ext-diff", "--no-textconv", "HEAD", "--"},
	})
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
	var rejected []string
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
		if status == "R100" {
			result[fields[i]] = fields[i+1]
		} else if strings.HasPrefix(status, "R") {
			result[fields[i]] = ""
			rejected = append(rejected, fmt.Sprintf("%q to %q", fields[i], fields[i+1]))
		} else if status == "D" {
			result[fields[i]] = ""
		}
		i += count
	}
	if len(rejected) > 0 {
		return result, fmt.Errorf("non-exact renames ignored: %s", strings.Join(rejected, "; "))
	}
	return result, nil
}
