package hook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

type liveEventsAPI struct {
	stringCreate                                func(uintptr, string, uint32) uintptr
	arrayCreate                                 func(uintptr, *uintptr, int, uintptr) uintptr
	release                                     func(uintptr)
	streamCreate                                func(uintptr, uintptr, uintptr, uintptr, uint64, float64, uint32) uintptr
	streamQueue                                 func(uintptr, uintptr)
	streamStart                                 func(uintptr) bool
	streamStop, streamInvalidate, streamRelease func(uintptr)
	queueCreate                                 func(string, uintptr) uintptr
	queueRelease                                func(uintptr)
	queueSync                                   func(uintptr, uintptr, uintptr)
	callback                                    uintptr
	drain                                       uintptr
}

var liveEventStreams sync.Map

var loadLiveEventsAPI = sync.OnceValues(func() (*liveEventsAPI, error) {
	core, err := purego.Dlopen("/System/Library/Frameworks/CoreServices.framework/CoreServices", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, err
	}
	cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, err
	}
	dispatch, err := purego.Dlopen("/usr/lib/system/libdispatch.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, err
	}
	a := new(liveEventsAPI)
	for _, binding := range []struct {
		lib  uintptr
		name string
		fn   any
	}{
		{cf, "CFStringCreateWithCString", &a.stringCreate},
		{cf, "CFArrayCreate", &a.arrayCreate},
		{cf, "CFRelease", &a.release},
		{core, "FSEventStreamCreate", &a.streamCreate},
		{core, "FSEventStreamSetDispatchQueue", &a.streamQueue},
		{core, "FSEventStreamStart", &a.streamStart},
		{core, "FSEventStreamStop", &a.streamStop},
		{core, "FSEventStreamInvalidate", &a.streamInvalidate},
		{core, "FSEventStreamRelease", &a.streamRelease},
		{dispatch, "dispatch_queue_create", &a.queueCreate},
		{dispatch, "dispatch_release", &a.queueRelease},
		{dispatch, "dispatch_sync_f", &a.queueSync},
	} {
		symbol, err := purego.Dlsym(binding.lib, binding.name)
		if err != nil {
			return nil, err
		}
		purego.RegisterFunc(binding.fn, symbol)
	}
	a.callback = purego.NewCallback(receiveLiveEvents)
	a.drain = purego.NewCallback(func(uintptr) {})
	return a, nil
})

type darwinLiveEvents struct {
	root, dir, marker string
	api               *liveEventsAPI
	stream, queue     uintptr
	started           bool
	mu                sync.Mutex
	last              uint64
	cutoff            chan struct{}
	paths             map[string]bool
	err               error
}

func armDarwinLiveEvents(root string) (*darwinLiveEvents, error) {
	api, err := loadLiveEventsAPI()
	if err != nil {
		return nil, err
	}
	git := filepath.Join(root, ".git")
	if info, err := os.Lstat(git); err != nil || !info.IsDir() {
		return nil, errors.New("live-verified sentinel needs a local .git directory")
	}
	sentinels := filepath.Join(git, "ethos-live")
	if err := os.Mkdir(sentinels, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if info, err := os.Lstat(sentinels); err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("live-verified sentinel directory is not private")
	}
	dir, err := os.MkdirTemp(sentinels, "run-")
	if err != nil {
		return nil, err
	}
	e := &darwinLiveEvents{root: root, dir: dir, api: api, marker: filepath.Join(dir, "opening"), cutoff: make(chan struct{}), paths: make(map[string]bool)}
	if err := e.start(); err != nil {
		e.close()
		return nil, err
	}
	return e, nil
}

func (e *darwinLiveEvents) start() error {
	a := e.api
	path := a.stringCreate(0, e.root, 0x08000100)
	if path == 0 {
		return errors.New("cannot create FSEvents root")
	}
	defer a.release(path)
	watched := a.arrayCreate(0, &path, 1, 0)
	if watched == 0 {
		return errors.New("cannot create FSEvents watch list")
	}
	defer a.release(watched)
	e.queue = a.queueCreate("ethos.handoff.live-events", 0)
	if e.queue == 0 {
		return errors.New("cannot create FSEvents dispatch queue")
	}
	const fileEvents, watchRoot, noDefer = 0x10, 0x04, 0x02
	e.stream = a.streamCreate(0, a.callback, 0, watched, ^uint64(0), 0, fileEvents|watchRoot|noDefer)
	if e.stream == 0 {
		return errors.New("cannot create FSEvents stream")
	}
	liveEventStreams.Store(e.stream, e)
	a.streamQueue(e.stream, e.queue)
	e.started = a.streamStart(e.stream)
	if !e.started {
		return errors.New("cannot start FSEvents stream")
	}
	return nil
}

func (e *darwinLiveEvents) close() {
	if e.stream != 0 {
		if e.started {
			e.api.streamStop(e.stream)
		}
		e.api.streamInvalidate(e.stream)
		e.api.queueSync(e.queue, 0, e.api.drain)
		liveEventStreams.Delete(e.stream)
		e.api.streamRelease(e.stream)
	}
	if e.queue != 0 {
		e.api.queueRelease(e.queue)
	}
	_ = os.RemoveAll(e.dir)
}

func receiveLiveEvents(stream, _ uintptr, count uintptr, paths **byte, flags *uint32, ids *uint64) {
	value, ok := liveEventStreams.Load(stream)
	if !ok {
		return
	}
	e := value.(*darwinLiveEvents)
	e.mu.Lock()
	defer e.mu.Unlock()
	if count > 65536 || paths == nil || flags == nil || ids == nil {
		e.err = errors.New("invalid FSEvents callback")
		return
	}
	flagList := unsafe.Slice(flags, int(count))
	idList := unsafe.Slice(ids, int(count))
	for i, p := range unsafe.Slice(paths, int(count)) {
		if e.marker == "" {
			break
		}
		const lostCoverage = 0x2f // MustScanSubDirs, UserDropped, KernelDropped, EventIdsWrapped, RootChanged.
		if flagList[i]&lostCoverage != 0 || p == nil || idList[i] < e.last {
			e.err = errors.New("FSEvents coverage lost or unordered")
			continue
		}
		e.last = idList[i]
		n := 0
		for n < 4096 && *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 {
			n++
		}
		if n == 4096 {
			e.err = errors.New("unrecognized FSEvents path")
			continue
		}
		path := string(unsafe.Slice(p, n))
		if path == e.marker {
			close(e.cutoff)
			if path == filepath.Join(e.dir, "opening") {
				clear(e.paths)
				e.marker = filepath.Join(e.dir, "closing")
				e.cutoff = make(chan struct{})
			} else {
				e.marker = ""
			}
		} else if sentinels := filepath.Dir(e.dir); !strings.HasPrefix(path, sentinels+"/") && path != sentinels {
			if len(e.paths) >= 8192 {
				e.err = errors.New("FSEvents path budget exceeded")
				continue
			}
			e.paths[path] = true
		}
	}
}

func (e *darwinLiveEvents) barrier(ctx context.Context, name string) error {
	e.mu.Lock()
	cutoff, err := e.cutoff, e.err
	e.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(e.dir, name), nil, 0o600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	select {
	case <-cutoff:
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.err
	case <-ctx.Done():
		return fmt.Errorf("FSEvents %s sentinel not observed: %w", name, ctx.Err())
	}
}

func (e *darwinLiveEvents) finish(ctx context.Context, inputs []string) error {
	if err := e.barrier(ctx, "closing"); err != nil {
		return err
	}
	keyed := make(map[string]bool, len(inputs))
	for _, input := range inputs {
		keyed[filepath.Join(e.root, input)] = true
	}
	git := filepath.Join(e.root, ".git")
	e.mu.Lock()
	paths := make([]string, 0, len(e.paths))
	for path := range e.paths {
		paths = append(paths, path)
	}
	e.mu.Unlock()
	slices.Sort(paths)
	var nonKeyed []string
	for _, path := range paths {
		if keyed[path] || path == git || strings.HasPrefix(path, git+"/") {
			return fmt.Errorf("FSEvents observed a change at %q", path)
		}
		rel, err := filepath.Rel(e.root, path)
		if err != nil || !filepath.IsLocal(rel) || rel == "." {
			return errors.New("FSEvents reported an unclassifiable checkout path")
		}
		nonKeyed = append(nonKeyed, rel)
	}
	if len(nonKeyed) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, gateInputTime)
	defer cancel()
	var ignored bytes.Buffer
	input := strings.NewReader(strings.Join(nonKeyed, "\x00") + "\x00")
	err := runHandoffGit(ctx, e.root, input, &gateHashBudget{ctx: ctx, dst: &ignored, remaining: 8 << 20}, "check-ignore", "--no-index", "-z", "--stdin")
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return errors.New("FSEvents could not classify changes to unkeyed paths")
	}
	ignoredPaths := make(map[string]bool, len(nonKeyed))
	for _, path := range strings.Split(ignored.String(), "\x00") {
		ignoredPaths[path] = true
	}
	for _, path := range nonKeyed {
		if !ignoredPaths[path] {
			return fmt.Errorf("FSEvents observed a change at %q", path)
		}
	}
	return nil
}

type darwinHandoffLive struct {
	events *darwinLiveEvents
	vnodes *darwinLiveVnodes
}

func armHandoffLive(ctx context.Context, root string, inputs []string) (handoffLiveGuard, error) {
	events, err := armDarwinLiveEvents(root)
	if err != nil {
		return nil, fmt.Errorf("live-verified FSEvents unavailable: %w", err)
	}
	if err := events.barrier(ctx, "opening"); err != nil {
		events.close()
		return nil, err
	}
	vnodes, err := armDarwinLiveVnodes(ctx, root, inputs, 4096)
	if err != nil {
		events.close()
		return nil, err
	}
	return &darwinHandoffLive{events: events, vnodes: vnodes}, nil
}

func (g *darwinHandoffLive) close() {
	g.vnodes.close()
	g.events.close()
}

func (g *darwinHandoffLive) finish(ctx context.Context, inputs []string) error {
	events := g.events.finish(ctx, inputs)
	vnodes := g.vnodes.drain()
	return errors.Join(events, vnodes)
}
