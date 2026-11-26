package hook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

type handoffGitView struct {
	files       map[string][]byte
	objects     []byte
	fingerprint string
}

const handoffGitObjectList = "--batch-check=%(objectname)"

func runHandoffGit(ctx context.Context, checkout string, stdin io.Reader, stdout io.Writer, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, _ := ctx.Deadline()
	limit := time.Until(deadline)
	if limit <= 0 {
		return context.DeadlineExceeded
	}
	_, err := gitenv.Run(gitenv.Spec{
		Git: "git", Dir: checkout, Limit: limit, Stdin: stdin, Stdout: stdout,
		Env:  append(gitenv.Parsed(), "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0"),
		Args: append([]string{"--no-replace-objects", "-c", "protocol.allow=never"}, args...),
	})
	return err
}

func handoffGitOutput(ctx context.Context, checkout string, args ...string) ([]byte, error) {
	var out bytes.Buffer
	err := runHandoffGit(ctx, checkout, nil, &gateHashBudget{ctx: ctx, dst: &out, remaining: 8 << 20}, args...)
	return out.Bytes(), err
}

func captureHandoffGit(ctx context.Context, checkout string, profile *checkset.WorktreeSnapshot) (handoffGitView, error) {
	if profile == nil || profile.Git == nil {
		return handoffGitView{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, gateInputTime)
	defer cancel()
	view := handoffGitView{files: make(map[string][]byte)}
	read := func(name string, optional bool, args ...string) error {
		out, err := handoffGitOutput(ctx, checkout, args...)
		if err != nil {
			return err
		}
		path := strings.TrimSuffix(string(out), "\n")
		if path == "" && optional {
			return nil
		}
		if !filepath.IsAbs(path) {
			return errors.New("invalid Git metadata path")
		}
		if name == "" {
			name = filepath.Base(path)
			if !strings.HasPrefix(name, "sharedindex.") {
				return errors.New("invalid Git shared index path")
			}
		}
		data, err := readHandoffGitFile(ctx, path)
		if optional && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		view.files[name] = data
		return nil
	}
	for _, name := range []string{"HEAD", "index", "shallow"} {
		if err := read(name, name != "HEAD", "rev-parse", "--path-format=absolute", "--git-path", name); err != nil {
			return view, errors.New("cannot capture Git view " + name)
		}
	}
	if err := read("", true, "rev-parse", "--path-format=absolute", "--shared-index-path"); err != nil {
		return view, errors.New("cannot capture Git view shared index")
	}
	format, err := handoffGitOutput(ctx, checkout, "rev-parse", "--show-object-format")
	if err != nil {
		return view, errors.New("cannot resolve Git object format")
	}
	oidLen := 40
	switch string(format) {
	case "sha1\n":
		view.files["config"] = []byte("[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n")
	case "sha256\n":
		view.files["config"] = []byte("[core]\n\trepositoryformatversion = 1\n\tfilemode = true\n[extensions]\n\tobjectformat = sha256\n")
		oidLen = 64
	default:
		return view, errors.New("unsupported Git object format")
	}
	refs, err := handoffGitOutput(ctx, checkout, "for-each-ref", "--format=%(objectname) %(refname) %(symref) %(upstream)")
	if err != nil {
		return view, errors.New("cannot capture Git view refs")
	}
	tips, err := view.addRefs(refs, profile.Git, oidLen)
	if err != nil {
		return view, err
	}
	view.objects, err = handoffReachableGitObjects(ctx, checkout, tips, oidLen)
	if err != nil {
		return view, errors.New("cannot enumerate Git view objects within the key budget")
	}
	h := sha256.New()
	if err := json.NewEncoder(h).Encode(view.files); err != nil {
		return view, err
	}
	_, _ = h.Write(view.objects)
	view.fingerprint = hex.EncodeToString(h.Sum(nil))
	return view, nil
}

func readHandoffGitFile(ctx context.Context, path string) ([]byte, error) {
	dir, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	f, err := openGateInput(dir, filepath.Base(path))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("git metadata is not a regular file")
	}
	var out bytes.Buffer
	_, err = io.Copy(&gateHashBudget{ctx: ctx, dst: &out, remaining: 8 << 20}, f)
	return out.Bytes(), err
}

type handoffGitRef struct {
	oid, target, upstream string
}

func (v handoffGitView) addRefs(data []byte, git *checkset.SnapshotGit, oidLen int) (map[string]bool, error) {
	all := make(map[string]handoffGitRef)
	required := slices.Clone(git.Refs)
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, " ")
		if len(fields) != 4 || !validHandoffGitOID(fields[0], oidLen) || !validHandoffGitRef(fields[1]) {
			return nil, errors.New("invalid Git view ref")
		}
		name := fields[1]
		all[name] = handoffGitRef{oid: fields[0], target: fields[2], upstream: fields[3]}
		if strings.HasPrefix(name, "refs/tags/") {
			required = append(required, name)
		}
	}
	if git.BaseRef != "" {
		required = append(required, git.BaseRef)
		if upstream := all[git.BaseRef].upstream; strings.HasPrefix(upstream, "refs/remotes/") {
			if _, exists := all[upstream]; exists {
				required = append(required, upstream)
			}
		}
	}
	tips := make(map[string]bool)
	head := strings.TrimSuffix(string(v.files["HEAD"]), "\n")
	if branch, symbolic := strings.CutPrefix(head, "ref: "); symbolic && validHandoffGitRef(branch) {
		if _, exists := all[branch]; exists {
			required = append(required, branch)
		}
	} else if validHandoffGitOID(head, oidLen) {
		tips[head] = true
	} else {
		return nil, errors.New("invalid Git view HEAD")
	}
	for _, name := range required {
		for name != "" {
			if _, included := v.files[name]; included {
				break
			}
			ref, exists := all[name]
			if !exists {
				return nil, fmt.Errorf("git view ref %s is missing", name)
			}
			value := ref.oid
			if ref.target != "" {
				if !validHandoffGitRef(ref.target) {
					return nil, errors.New("invalid Git view symbolic ref")
				}
				value = "ref: " + ref.target
			}
			v.files[name] = []byte(value + "\n")
			tips[ref.oid] = true
			name = ref.target
		}
	}
	return tips, nil
}

func handoffReachableGitObjects(ctx context.Context, checkout string, tips map[string]bool, oidLen int) ([]byte, error) {
	index, err := handoffGitOutput(ctx, checkout, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	for _, entry := range strings.Split(string(index), "\x00") {
		if entry == "" {
			continue
		}
		meta, _, hasPath := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !hasPath || len(fields) != 3 || !validHandoffGitOID(fields[1], oidLen) {
			return nil, errors.New("invalid Git index entry")
		}
		if fields[0] != "160000" {
			tips[fields[1]] = true
		}
	}
	if len(tips) == 0 {
		return nil, nil
	}
	var input, out bytes.Buffer
	for oid := range tips {
		input.WriteString(oid)
		input.WriteByte('\n')
	}
	err = runHandoffGit(ctx, checkout, &input, &gateHashBudget{ctx: ctx, dst: &out, remaining: 8 << 20},
		"rev-list", "--objects", "--no-object-names", "--stdin")
	if err != nil {
		return nil, err
	}
	objects := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	slices.Sort(objects)
	return []byte(strings.Join(slices.Compact(objects), "\n") + "\n"), nil
}

func validHandoffGitRef(ref string) bool {
	return strings.HasPrefix(ref, "refs/") && filepath.IsLocal(ref) && filepath.Clean(ref) == ref
}

func validHandoffGitOID(oid string, size int) bool {
	_, err := hex.DecodeString(oid)
	return len(oid) == size && err == nil
}

func (v handoffGitView) materialize(ctx context.Context, checkout, dest string) error {
	ctx, cancel := context.WithTimeout(ctx, gitenv.Timeout)
	defer cancel()
	for _, dir := range []string{"objects/pack", "refs", "worktrees"} {
		if err := os.MkdirAll(filepath.Join(dest, dir), 0o700); err != nil {
			return err
		}
	}
	for name, content := range v.files {
		path := filepath.Join(dest, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return err
		}
	}
	if len(v.objects) == 0 {
		return nil
	}
	stage, err := os.MkdirTemp(dest, layout.ScratchDirPattern("pack"))
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	pack, err := os.Create(filepath.Join(stage, "snapshot.pack"))
	if err != nil {
		return err
	}
	err = runHandoffGit(ctx, checkout, bytes.NewReader(v.objects),
		&gateHashBudget{ctx: ctx, dst: pack, remaining: gateBytes},
		"pack-objects", "--stdout", "--no-reuse-delta")
	if err := errors.Join(err, pack.Close()); err != nil {
		return err
	}
	out, err := handoffGitOutput(ctx, filepath.Dir(dest), "--git-dir="+dest, "index-pack", pack.Name())
	oid := strings.TrimSpace(string(out))
	if err != nil || !(validHandoffGitOID(oid, 40) || validHandoffGitOID(oid, 64)) {
		return errors.New("cannot index Git view pack")
	}
	prefix := filepath.Join(dest, "objects", "pack", "pack-"+oid)
	for _, source := range []string{pack.Name(), strings.TrimSuffix(pack.Name(), ".pack") + ".idx"} {
		if err := os.Rename(source, prefix+filepath.Ext(source)); err != nil {
			return err
		}
	}
	objects, err := handoffGitOutput(ctx, filepath.Dir(dest), "--git-dir="+dest, "cat-file", "--batch-all-objects", handoffGitObjectList)
	if err != nil || !bytes.Equal(objects, v.objects) {
		return errors.New("materialized Git objects do not match the key")
	}
	return nil
}
