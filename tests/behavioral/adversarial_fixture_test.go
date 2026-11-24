package behavioral

import (
	_ "embed"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const adversarialRaceProbe = "go test -race -count=1 -tags behavioral_race_probe -run TestSafeCounterConcurrentIncrement ./pkg/counter"
const adversarialRaceProbePath = "pkg/counter/safe_counter_test.go"

//go:embed testdata/safe_counter_test.go
var adversarialRaceProbeSource string

func setupAdversarialWriteSetFixture(t *testing.T) *Fixture {
	t.Helper()
	f := SetupFixture(t)
	writeFile(t, filepath.Join(f.Repo, adversarialRaceProbePath), adversarialRaceProbeSource)
	gitCmd(t, f.Repo, f.Env, "add", "--", adversarialRaceProbePath)
	gitCmd(t, f.Repo, f.Env, "commit", "-m", "seed SafeCounter race probe")
	return f
}

func TestAdversarialWriteSetRaceProbe(t *testing.T) {
	f := setupAdversarialWriteSetFixture(t)
	require.Contains(t, f.gitLines(t, "ls-tree", "--name-only", "HEAD", "--", adversarialRaceProbePath),
		adversarialRaceProbePath, "race probe must already be checked in")
	base := f.GitHead(t)

	path := filepath.Join(f.Repo, "pkg", "counter", "counter.go")
	counter, err := os.ReadFile(path)
	require.NoError(t, err)
	counter = []byte(strings.Replace(string(counter), "package counter", "package counter\n\nimport \"sync\"", 1))
	writeFile(t, path, string(counter)+`
type SafeCounter struct {
	mu sync.Mutex
	n  int
}

func (c *SafeCounter) Increment() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

func (c *SafeCounter) Value() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
`)

	cmd := exec.CommandContext(t.Context(), "go", strings.Fields(adversarialRaceProbe)[1:]...)
	cmd.Dir = f.Repo
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.NotContains(t, string(output), "[no tests to run]")
	require.Equal(t, []string{"pkg/counter/counter.go"}, f.GitDiffFiles(t, base))
}
