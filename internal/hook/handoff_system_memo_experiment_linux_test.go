package hook

import (
	"context"
	"os"
	"path/filepath"
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
	require.Equal(t, original, hash(memo))
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
	require.NotEqual(t, original, hash(memo))

	require.NoError(t, os.Chmod(memoPath, 0o644))
	require.Empty(t, readHandoffSystemMemo(memoPath).prev)
	require.Nil(t, loadHandoffSystemMemo(dir))
	t.Setenv("HOME", dir)
	require.NotNil(t, loadHandoffSystemMemo("/home/linuxbrew/.linuxbrew"))
}
