package hook

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestHandoffSystemMemoPersistsAndInvalidatesDigest(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input")
	writeFile(t, input, "original")
	memoPath := filepath.Join(dir, "store", "memo.json")
	key := handoffMemoPathKey(input)
	ctx := context.Background()
	var buffer [handoffSystemReadBuffer]byte
	hash := func(memo *handoffSystemMemo) string {
		t.Helper()
		digest, err := fingerprintHandoffSystemFile(ctx, input, unix.O_NOFOLLOW, buffer[:], memo)
		require.NoError(t, err)
		return digest
	}
	start := readHandoffSystemMemo(memoPath)
	original := hash(start)
	start.save()

	memo := readHandoffSystemMemo(memoPath)
	require.Equal(t, original, memo.prev[key].Digest)
	sentinel := strings.Repeat("0", 64)
	require.NotEqual(t, original, sentinel)
	record := memo.prev[key]
	record.Digest = sentinel
	memo.put(key, record)
	memo.save()

	memo = readHandoffSystemMemo(memoPath)
	require.Equal(t, sentinel, hash(memo))
	require.Equal(t, memo.prev, memo.next)
	memo.save()

	before, err := os.Stat(input)
	require.NoError(t, err)
	writeFile(t, input, "modified")
	require.Eventually(t, func() bool {
		if err := os.Chtimes(input, before.ModTime(), before.ModTime()); err != nil {
			return false
		}
		info, err := os.Stat(input)
		return err == nil && handoffMemoIdentity(info).Ctime != handoffMemoIdentity(before).Ctime
	}, time.Second, 10*time.Millisecond)
	after, err := os.Stat(input)
	require.NoError(t, err)
	require.Equal(t, handoffMemoIdentity(before).Mtime, handoffMemoIdentity(after).Mtime)
	rehash := hash(memo)
	require.NotEqual(t, original, rehash)
	require.Equal(t, hash(nil), rehash)

	require.NoError(t, os.Chmod(memoPath, 0o644))
	require.Empty(t, readHandoffSystemMemo(memoPath).prev)
	require.Nil(t, loadHandoffSystemMemo(dir))
	t.Setenv("HOME", dir)
	require.NotNil(t, loadHandoffSystemMemo("/home/linuxbrew/.linuxbrew"))
}

func TestHandoffSystemMemoPersistsBoundedCurrentEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memo.json")
	stale := strings.Repeat("f", 64)
	entry := handoffMemoRecord{Digest: strings.Repeat("a", 64)}
	memo := readHandoffSystemMemo(path)
	memo.put(stale, entry)
	memo.save()
	memo = readHandoffSystemMemo(path)
	require.Contains(t, memo.prev, stale)

	const count = 90_000
	for i := count - 1; i >= 0; i-- {
		memo.put(fmt.Sprintf("%064x", i), entry)
	}
	memo.save()

	saved := readHandoffSystemMemo(path).prev
	require.NotContains(t, saved, stale)
	require.Equal(t, memoExperimentEntries, len(saved))
	for i := range len(saved) {
		key := fmt.Sprintf("%064x", i)
		if saved[key] != entry {
			t.Fatalf("deterministic retained entries are missing %s", key)
		}
	}
}
