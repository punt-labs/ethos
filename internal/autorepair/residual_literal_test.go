package autorepair

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/mission"
)

// residualLiterals returns the positions of Residual composite literals in src
// outside the two constructors: Residual{...}, and the elided element literals
// of []Residual{{...}}, []*Residual{{...}} and map[K]Residual{k: {...}}.
func residualLiterals(t *testing.T, fset *token.FileSet, name string, src any) []token.Position {
	t.Helper()
	af, err := parser.ParseFile(fset, name, src, 0)
	require.NoError(t, err)
	isResidual := func(e ast.Expr) bool {
		if st, ok := e.(*ast.StarExpr); ok {
			e = st.X
		}
		id, ok := e.(*ast.Ident)
		return ok && id.Name == "Residual"
	}
	var out []token.Position
	for _, d := range af.Decls {
		fn, _ := d.(*ast.FuncDecl)
		if fn != nil && (fn.Name.Name == "settledResidual" || fn.Name.Name == "actionableResidual") {
			continue
		}
		ast.Inspect(d, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if cl.Type != nil && isResidual(cl.Type) {
				out = append(out, fset.Position(cl.Pos()))
			}
			var elt ast.Expr
			switch ty := cl.Type.(type) {
			case *ast.ArrayType:
				elt = ty.Elt
			case *ast.MapType:
				elt = ty.Value
			}
			if elt != nil && isResidual(elt) {
				for _, e := range cl.Elts {
					if kv, ok := e.(*ast.KeyValueExpr); ok {
						e = kv.Value
					}
					if inner, ok := e.(*ast.CompositeLit); ok && inner.Type == nil {
						out = append(out, fset.Position(inner.Pos()))
					}
				}
			}
			return true
		})
	}
	return out
}

// Every production residual goes through settledResidual or
// actionableResidual, so none can omit its class or its action.
func TestNoResidualLiteralOutsideConstructors(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		checked++
		for _, pos := range residualLiterals(t, fset, f, nil) {
			t.Errorf("%s: Residual literal outside the constructors", pos)
		}
	}
	assert.Greater(t, checked, 10)
}

// The guard sees every spelling of a hand-built residual, elided ones included.
func TestResidualLiteralGuardCatchesElidedLiterals(t *testing.T) {
	src := func(body string) string { return "package p\nfunc f() {\n" + body + "\n}\n" }
	for name, c := range map[string]struct {
		body string
		want int
	}{
		"explicit":      {"_ = Residual{Step: \"s\"}", 1},
		"elided slice":  {"_ = []Residual{{Step: \"s\"}, {Step: \"t\"}}", 2},
		"elided ptr":    {"_ = []*Residual{{Step: \"s\"}}", 1},
		"elided map":    {"_ = map[string]Residual{\"k\": {Step: \"s\"}}", 1},
		"constructor":   {"_ = []Residual{settledResidual(\"s\", \"i\", \"r\")}", 0},
		"other type":    {"_ = []Other{{Step: \"s\"}}", 0},
		"empty slice":   {"_ = []Residual{}", 0},
		"nil residuals": {"var r []Residual; _ = r", 0},
	} {
		t.Run(name, func(t *testing.T) {
			got := residualLiterals(t, token.NewFileSet(), "x.go", src(c.body))
			assert.Len(t, got, c.want)
		})
	}
}

func TestActionableResidualRequiresAction(t *testing.T) {
	assert.Panics(t, func() { actionableResidual("s", "i", "r", "") })
	r := actionableResidual("s", "i", "r", "do it")
	assert.Equal(t, ClassActionable, r.Class)
	assert.Equal(t, "do it", r.Action)
}

// The read-path allowlist is built from the producers' own strings, so a
// reworded reason cannot silently turn an old settled residual actionable.
func TestEffectiveClassAllowlistMatchesProducers(t *testing.T) {
	for _, k := range []mission.LegacyFence{mission.LegacyIncomplete, mission.LegacyRedactionRefused} {
		r := Residual{Step: "round-decisions", Reason: k.String() + fencedSuffix}
		assert.Equal(t, ClassSettled, r.EffectiveClass(), k.String())
	}
	for _, k := range []mission.LegacyFence{mission.LegacyCorruptCorrections, mission.LegacyUnloadable} {
		r := Residual{Step: "round-decisions", Reason: k.String() + fencedSuffix}
		assert.Equal(t, ClassActionable, r.EffectiveClass(), k.String())
	}
	assert.Equal(t, ClassSettled, Residual{Step: stepGlobalMissions, Reason: reasonImportedKept}.EffectiveClass())
	assert.Equal(t, ClassActionable, Residual{Step: stepGlobalMissions, Reason: mission.ReasonRepoCopyDiverged}.EffectiveClass())
	assert.Equal(t, ClassActionable, Residual{Step: "round-decisions", Reason: writeFailPrefix + fencedSuffix}.EffectiveClass())
	assert.Equal(t, ClassSettled, Residual{Step: "x", Class: ClassSettled}.EffectiveClass())
}
