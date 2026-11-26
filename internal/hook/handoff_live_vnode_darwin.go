package hook

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"golang.org/x/sys/unix"
)

const darwinLiveVnodeNotes = unix.NOTE_WRITE | unix.NOTE_EXTEND | unix.NOTE_LINK |
	unix.NOTE_RENAME | unix.NOTE_DELETE | unix.NOTE_REVOKE | unix.NOTE_ATTRIB

type darwinLiveVnode struct {
	path string
	stat unix.Stat_t
}

type darwinLiveVnodes struct {
	queue int
	nodes map[int]darwinLiveVnode
}

func armDarwinLiveVnodes(ctx context.Context, root string, git darwinLiveGit, inputs []string, budget int) (*darwinLiveVnodes, error) {
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		return nil, fmt.Errorf("live-verified cannot determine vnode fd limit: %w", err)
	}
	dir, err := os.Open("/dev/fd")
	if err != nil {
		return nil, fmt.Errorf("live-verified cannot count current fds: %w", err)
	}
	open, err := dir.Readdirnames(-1)
	if err := errors.Join(err, dir.Close()); err != nil {
		return nil, fmt.Errorf("live-verified cannot count current fds: %w", err)
	}
	const reserve = 32
	if limit.Cur <= uint64(len(open)+reserve) {
		return nil, fmt.Errorf("live-verified vnode fd budget exceeded before watching %q", root)
	}
	budget = int(min(limit.Cur-uint64(len(open)+reserve), uint64(max(0, budget))))
	paths, err := darwinLiveVnodePaths(ctx, root, git, inputs, budget)
	if err != nil {
		return nil, err
	}
	queue, err := unix.Kqueue()
	if err != nil {
		return nil, fmt.Errorf("live-verified cannot open vnode queue: %w", err)
	}
	unix.CloseOnExec(queue)
	guard := &darwinLiveVnodes{queue: queue, nodes: make(map[int]darwinLiveVnode, len(paths))}
	for _, path := range paths {
		if err := guard.add(ctx, path); err != nil {
			guard.close()
			return nil, fmt.Errorf("live-verified cannot arm vnode guard for %q: %w", path, err)
		}
	}
	return guard, nil
}

func darwinLiveVnodePaths(ctx context.Context, root string, git darwinLiveGit, inputs []string, budget int) ([]string, error) {
	paths := map[string]bool{}
	add := func(path, boundary string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		for ; !paths[path]; path = filepath.Dir(path) {
			paths[path] = true
			if len(paths) >= budget {
				return fmt.Errorf("live-verified vnode fd budget exceeded at %q", path)
			}
			if path == boundary {
				break
			}
		}
		return nil
	}
	for _, input := range inputs {
		if !filepath.IsLocal(input) {
			return nil, fmt.Errorf("live-verified vnode input %q is outside checkout %q", input, root)
		}
		if err := add(filepath.Join(root, input), root); err != nil {
			return nil, err
		}
	}
	if err := add(filepath.Join(root, ".git"), root); err != nil {
		return nil, err
	}
	localBoundary := git.local
	if darwinLiveContains(git.common, git.local) {
		localBoundary = git.common
	}
	if err := add(filepath.Join(git.local, "HEAD"), localBoundary); err != nil {
		return nil, err
	}
	for _, dir := range []string{git.local, git.common} {
		if _, err := os.Lstat(filepath.Join(dir, "reftable")); !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("live-verified vnode guard cannot cover Git metadata layout at %q", filepath.Join(dir, "reftable"))
		}
	}
	type watchPath struct{ path, boundary string }
	metadata := []watchPath{
		{filepath.Join(git.local, "index"), localBoundary},
		{filepath.Join(git.common, "packed-refs"), git.common},
		{filepath.Join(git.common, "refs"), git.common},
	}
	if git.local != git.common {
		for _, name := range []string{"commondir", "gitdir", "refs"} {
			metadata = append(metadata, watchPath{filepath.Join(git.local, name), localBoundary})
		}
	}
	if err := add(git.common, git.common); err != nil {
		return nil, err
	}
	for _, target := range metadata {
		path := target.path
		if err := filepath.WalkDir(path, func(entry string, node fs.DirEntry, err error) error {
			if entry == path && node == nil && errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			return add(entry, target.boundary)
		}); err != nil {
			return nil, fmt.Errorf("live-verified cannot enumerate vnode paths: %w", err)
		}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	slices.Sort(ordered)
	return ordered, nil
}

func (g *darwinLiveVnodes) add(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fd, err := unix.Open(path, unix.O_EVTONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	node := darwinLiveVnode{path: path}
	g.nodes[fd] = node
	event := unix.Kevent_t{Ident: uint64(fd), Filter: unix.EVFILT_VNODE,
		Flags: unix.EV_ADD | unix.EV_CLEAR, Fflags: darwinLiveVnodeNotes}
	if _, err := unix.Kevent(g.queue, []unix.Kevent_t{event}, nil, nil); err != nil {
		return err
	}
	if err := unix.Fstat(fd, &node.stat); err != nil {
		return err
	}
	var current unix.Stat_t
	if err := unix.Lstat(path, &current); err != nil {
		return err
	}
	kind := node.stat.Mode & unix.S_IFMT
	if kind != unix.S_IFDIR && kind != unix.S_IFREG || node.stat.Ino != current.Ino || node.stat.Dev != current.Dev {
		return errors.New("vnode path cannot be verified")
	}
	g.nodes[fd] = node
	return nil
}

func (g *darwinLiveVnodes) drain() error {
	events := make([]unix.Kevent_t, len(g.nodes))
	n, err := unix.Kevent(g.queue, nil, events, &unix.Timespec{})
	if err != nil {
		return fmt.Errorf("live-verified cannot drain vnode guard: %w", err)
	}
	for _, event := range events[:n] {
		node, ok := g.nodes[int(event.Ident)]
		if !ok {
			return fmt.Errorf("live-verified vnode coverage uncertain: no watched path for fd %d", event.Ident)
		}
		if event.Filter != unix.EVFILT_VNODE || event.Flags&(unix.EV_ERROR|unix.EV_EOF) != 0 {
			return fmt.Errorf("live-verified vnode coverage uncertain at %q: event flags %#x", node.path, event.Flags)
		}
		if event.Fflags == unix.NOTE_ATTRIB {
			var now unix.Stat_t
			if unix.Fstat(int(event.Ident), &now) == nil && darwinAtimeOnly(node.stat, now) {
				continue
			}
		}
		return fmt.Errorf("live-verified vetoed by vnode note %#x at %q", event.Fflags, node.path)
	}
	return nil
}

func darwinAtimeOnly(before, after unix.Stat_t) bool {
	return before.Atim != after.Atim && before.Mode == after.Mode && before.Uid == after.Uid &&
		before.Gid == after.Gid && before.Size == after.Size && before.Mtim == after.Mtim &&
		before.Ctim == after.Ctim && before.Dev == after.Dev && before.Ino == after.Ino &&
		before.Nlink == after.Nlink && before.Flags == after.Flags && before.Btim == after.Btim
}

func (g *darwinLiveVnodes) close() {
	_ = unix.Close(g.queue)
	for fd := range g.nodes {
		_ = unix.Close(fd)
	}
}
