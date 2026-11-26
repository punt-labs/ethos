package hook

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"golang.org/x/sys/unix"
)

const memoExperimentLimit = 16 << 20

type handoffMemoKey struct {
	Dev, Inode         uint64
	Size, Mtime, Ctime int64
}

type handoffMemoRecord struct {
	Key    handoffMemoKey
	Digest string
}

type handoffSystemMemo struct {
	path string
	prev map[string]handoffMemoRecord
	mu   sync.Mutex
	next map[string]handoffMemoRecord
}

func loadHandoffSystemMemo(path string) *handoffSystemMemo {
	if path != "/home/linuxbrew/.linuxbrew" {
		return nil
	}
	root, err := layout.MachineRoot()
	if err != nil {
		return nil
	}
	memo := &handoffSystemMemo{
		path: filepath.Join(root, "handoff-linuxbrew-memo-experiment", "v1.json"),
		next: make(map[string]handoffMemoRecord),
	}
	fd, err := unix.Open(memo.path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return memo
	}
	f := os.NewFile(uintptr(fd), memo.path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > memoExperimentLimit {
		return memo
	}
	data, err := io.ReadAll(io.LimitReader(f, memoExperimentLimit+1))
	if err == nil && len(data) <= memoExperimentLimit {
		var prev map[string]handoffMemoRecord
		if json.Unmarshal(data, &prev) == nil {
			memo.prev = prev
		}
	}
	return memo
}

func handoffMemoIdentity(info fs.FileInfo) handoffMemoKey {
	stat := info.Sys().(*syscall.Stat_t)
	return handoffMemoKey{Dev: uint64(stat.Dev), Inode: uint64(stat.Ino), Size: stat.Size,
		Mtime: stat.Mtim.Nano(), Ctime: stat.Ctim.Nano()}
}

func (m *handoffSystemMemo) get(path string, key handoffMemoKey) (string, bool) {
	entry, ok := m.prev[path]
	if !ok || entry.Key != key || len(entry.Digest) != 64 {
		return "", false
	}
	if _, err := hex.DecodeString(entry.Digest); err != nil {
		return "", false
	}
	m.put(path, entry)
	return entry.Digest, true
}

func (m *handoffSystemMemo) put(path string, entry handoffMemoRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next[path] = entry
}

func (m *handoffSystemMemo) save() {
	if len(m.next) == len(m.prev) {
		unchanged := true
		for path, entry := range m.next {
			if m.prev[path] != entry {
				unchanged = false
				break
			}
		}
		if unchanged {
			return
		}
	}
	data, err := json.Marshal(m.next)
	if err != nil || len(data) > memoExperimentLimit {
		return
	}
	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	f, err := os.CreateTemp(dir, "memo-")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	closeErr := f.Close()
	if err == nil && closeErr == nil {
		_ = os.Rename(f.Name(), m.path)
	}
}
