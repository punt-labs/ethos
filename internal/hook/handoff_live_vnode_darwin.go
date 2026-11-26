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

func armDarwinLiveVnodes(ctx context.Context, root string, inputs []string, budget int) (*darwinLiveVnodes, error) {
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
		return nil, errors.New("live-verified vnode fd budget exceeded")
	}
	budget = int(min(limit.Cur-uint64(len(open)+reserve), uint64(max(0, budget))))
	paths, err := darwinLiveVnodePaths(ctx, root, inputs, budget)
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
			return nil, fmt.Errorf("live-verified cannot arm vnode guard: %w", err)
		}
	}
	return guard, nil
}

func darwinLiveVnodePaths(ctx context.Context, root string, inputs []string, budget int) ([]string, error) {
	paths := map[string]bool{}
	add := func(path string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		for ; !paths[path]; path = filepath.Dir(path) {
			paths[path] = true
			if len(paths) >= budget {
				return errors.New("live-verified vnode fd budget exceeded")
			}
			if path == root {
				break
			}
		}
		return nil
	}
	for _, input := range inputs {
		if !filepath.IsLocal(input) {
			return nil, errors.New("live-verified vnode input is outside checkout")
		}
		if err := add(filepath.Join(root, input)); err != nil {
			return nil, err
		}
	}
	git := filepath.Join(root, ".git")
	info, err := os.Lstat(git)
	if err != nil || !info.IsDir() {
		return nil, errors.New("live-verified vnode guard needs local .git directory")
	}
	if err := add(filepath.Join(git, "HEAD")); err != nil {
		return nil, err
	}
	for _, name := range []string{"commondir", "reftable"} {
		if _, err := os.Lstat(filepath.Join(git, name)); !errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("live-verified vnode guard cannot cover Git metadata layout")
		}
	}
	for _, name := range []string{"index", "packed-refs", "refs"} {
		path := filepath.Join(git, name)
		if err := filepath.WalkDir(path, func(entry string, node fs.DirEntry, err error) error {
			if entry == path && node == nil && errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			return add(entry)
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
		if !ok || event.Filter != unix.EVFILT_VNODE || event.Flags&(unix.EV_ERROR|unix.EV_EOF) != 0 {
			return errors.New("live-verified vnode coverage uncertain")
		}
		if event.Fflags == unix.NOTE_ATTRIB {
			var now unix.Stat_t
			if unix.Fstat(int(event.Ident), &now) == nil && darwinAtimeOnly(node.stat, now) {
				continue
			}
		}
		return fmt.Errorf("live-verified vetoed by vnode note %#x", event.Fflags)
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
