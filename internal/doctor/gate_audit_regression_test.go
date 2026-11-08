package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gateAuditFixture(t *testing.T, doctor, githook string) (string, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	dirs := map[string]string{"doctor": filepath.Join(dir, "doctor"), "githook": filepath.Join(dir, "githook")}
	for pkg, src := range map[string]string{"doctor": doctor, "githook": githook} {
		require.NoError(t, os.MkdirAll(dirs[pkg], 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dirs[pkg], pkg+".go"), []byte(src), 0o600))
	}
	return filepath.Join(dirs["doctor"], "doctor.go"), dirs
}

// These are negative fixtures: each ungatedCheck must be rejected, not merely
// balanced by a positive example that the audit happens to accept.
func TestGateAuditLocatedAliases(t *testing.T) {
	for _, tc := range []struct{ name, declaration string }{
		{"short declaration", `physicalRoot := repoRoot`},
		{"var declaration", `var physicalRoot = repoRoot`},
		{"typed declaration", `var physicalRoot string = repoRoot`},
		{"grouped declarations", `var (first = repoRoot; physicalRoot string = first)`},
		{"multiple declarations", `var first, physicalRoot = "", repoRoot; _ = first`},
		{"assignment chain", `var first, physicalRoot string; first = repoRoot; physicalRoot = first`},
		{"parentheses", `var physicalRoot = (repoRoot)`},
		{"concatenation chain", `var first = repoRoot + "/.punt-labs"; var physicalRoot = first + "/ethos"`},
		{"path derivation chain", `var first = filepath.Join(repoRoot, ".punt-labs"); var physicalRoot = filepath.Clean(first)`},
		{"conversion", `var physicalRoot = string([]byte(repoRoot))`},
		{"local named type conversion", `type path string; var physicalRoot = path(repoRoot)`},
		{"composite and selector", `var first = struct{ root string }{root: repoRoot}; var physicalRoot = first.root`},
		{"index and slice", `var first = []string{repoRoot}; var physicalRoot = first[:][0]`},
		{"store alias", `var physicalRoot = s`},
		{"team alias", `var physicalRoot = teams`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, dirs := gateAuditFixture(t, `package doctor
import "path/filepath"
func runAll(repoRoot, storeRoot string, s, teams Store) {
`+tc.declaration+`
ungatedCheck(physicalRoot)
}`, `package githook`)
			_, ungated := gateAudit(parseRunAll(t, file), locatingFuncs(t, dirs))
			assert.Contains(t, ungated, "ungatedCheck")
		})
	}
}

func TestGateAuditCurrentLocatorAPIs(t *testing.T) {
	for _, tc := range []struct{ name, imports, call string }{
		{"roots", `import "github.com/punt-labs/ethos/v4/internal/resolve"`, `resolve.Roots()`},
		{"checkout", `import "github.com/punt-labs/ethos/v4/internal/resolve"`, `resolve.CheckoutOf(".")`},
		{"renamed roots import", `import roots "github.com/punt-labs/ethos/v4/internal/resolve"`, `roots.Roots()`},
		{"renamed checkout import", `import roots "github.com/punt-labs/ethos/v4/internal/resolve"`, `roots.CheckoutOf(".")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, dirs := gateAuditFixture(t, `package doctor
import hooks "github.com/punt-labs/ethos/v4/internal/githook"
func runAll(repoRoot string) {
 checkout := func(r Result) Result { return r }
 ungatedCheck()
 checkout(ungatedCheck())
}
func ungatedCheck() Result { return helper() }
func helper() Result { hooks.Find(); return Result{} }
`, `package githook
`+tc.imports+`
func Find() { `+tc.call+` }
`)
			locating := locatingFuncs(t, dirs)
			assert.True(t, locating["githook.Find"])
			assert.True(t, locating["doctor.helper"])
			assert.True(t, locating["doctor.ungatedCheck"])
			_, ungated := gateAudit(parseRunAll(t, file), locating)
			assert.Equal(t, []string{"ungatedCheck"}, ungated)

			// The same API used directly by runAll must not need a helper
			// entry in the call graph to be recognized.
			direct, _ := gateAuditFixture(t, "package doctor\n"+tc.imports+"\nfunc runAll(repoRoot string) { "+tc.call+" }", `package githook`)
			_, ungated = gateAudit(parseRunAll(t, direct), nil)
			assert.Len(t, ungated, 1)
		})
	}
}

func TestGateAuditLocalBindingsAndGateKinds(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		ungated    []string
	}{
		{"checkout input", `ungatedCheck(repoRoot)`, []string{"ungatedCheck"}},
		{"store input", `ungatedCheck(storeRoot)`, []string{"ungatedCheck"}},
		{"wrong checkout gate", `checkout(ungatedCheck(storeRoot))`, []string{"ungatedCheck"}},
		{"wrong store gate", `store(ungatedCheck(repoRoot))`, []string{"ungatedCheck"}},
		{"mixed inputs need both gates", `checkout(ungatedCheck(repoRoot, s))`, []string{"ungatedCheck"}},
		{"nested gates", `checkout(store(ungatedCheck(repoRoot, teams)))`, nil},
		{"gated var alias", `var physicalRoot = repoRoot; checkout(ungatedCheck(physicalRoot))`, nil},
		{"shadowed root", `{ repoRoot := "not located"; ungatedCheck(repoRoot) }`, nil},
		{"shadowed alias", `var first = repoRoot; { first := "not located"; ungatedCheck(first) }; checkout(ungatedCheck(first))`, nil},
		{"shadowed gate", `{ checkout := func(r Result) Result { return r }; checkout(ungatedCheck(repoRoot)) }`, []string{"ungatedCheck"}},
		{"method receiver", `var layer = s; layer.ungatedCheck()`, []string{"layer.ungatedCheck"}},
		{"path import alias", `var first = paths.Join(repoRoot, "x"); ungatedCheck(first)`, []string{"ungatedCheck"}},
		{"result is not a root", `r := checkout(ungatedCheck(repoRoot)); report(r)`, nil},
		{"paired assignment does not taint sibling", `var first, second = repoRoot, "not located"; checkout(ungatedCheck(first)); report(second)`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, _ := gateAuditFixture(t, `package doctor
import paths "path/filepath"
func runAll(repoRoot, storeRoot string, s, teams Store) {
 checkout := func(r Result) Result { return r }
 store := func(r Result) Result { return r }
 `+tc.body+`
}`, `package githook`)
			_, ungated := gateAudit(parseRunAll(t, file), nil)
			assert.Equal(t, tc.ungated, ungated)
		})
	}
}

func TestGateAuditLocatorImportBindings(t *testing.T) {
	for _, tc := range []struct{ name, imports, body string }{
		{"shadowed resolve import", `import "github.com/punt-labs/ethos/v4/internal/resolve"`, `resolve := struct{ Roots func() }{}; resolve.Roots()`},
		{"shadowed renamed import", `import roots "github.com/punt-labs/ethos/v4/internal/resolve"`, `roots := struct{ CheckoutOf func(string) }{}; roots.CheckoutOf(".")`},
		{"different package named resolve", `import resolve "example.invalid/not-resolve"`, `resolve.Roots(); resolve.LocateRepoRoot()`},
		{"nonlocator resolve API", `import "github.com/punt-labs/ethos/v4/internal/resolve"`, `resolve.ResolutionFromConfig(nil); resolve.ConfigOf("."); resolve.IsWorkingTree(".")`},
		{"shadowed helper", `import "github.com/punt-labs/ethos/v4/internal/resolve"`, `ungatedCheck := func() {}; ungatedCheck()`},
		{"shadowed githook import", `import "github.com/punt-labs/ethos/v4/internal/githook"`, `githook := struct{ HooksDir func() }{}; githook.HooksDir()`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, dirs := gateAuditFixture(t, "package doctor\n"+tc.imports+`
func runAll(repoRoot string) { `+tc.body+` }
func ungatedCheck() { _ = resolve.Roots() }
`, `package githook
import "github.com/punt-labs/ethos/v4/internal/resolve"
func HooksDir() { _ = resolve.Roots() }
`)
			_, ungated := gateAudit(parseRunAll(t, file), locatingFuncs(t, dirs))
			assert.Empty(t, ungated)
		})
	}
}

// In particular, exercise the actual dependency that the former *Repo*/Locate*
// heuristic missed, not just helpers with names constructed by a fixture.
func TestGateAuditCurrentDependencies(t *testing.T) {
	locating := locatingFuncs(t, map[string]string{"doctor": ".", "githook": filepath.Join("..", "githook")})
	for _, name := range []string{"githook.HooksDir", "doctor.checkHookPresence", "doctor.CheckHookCurrency", "doctor.CheckUnclassifiedFiles"} {
		assert.True(t, locating[name], name)
	}
}
