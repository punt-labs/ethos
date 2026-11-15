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
		af, err := parser.ParseFile(fset, f, nil, 0)
		require.NoError(t, err)
		checked++
		for _, d := range af.Decls {
			fn, _ := d.(*ast.FuncDecl)
			allowed := fn != nil && (fn.Name.Name == "settledResidual" || fn.Name.Name == "actionableResidual")
			ast.Inspect(d, func(n ast.Node) bool {
				cl, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				if id, ok := cl.Type.(*ast.Ident); ok && id.Name == "Residual" && !allowed {
					t.Errorf("%s: Residual literal outside the constructors", fset.Position(cl.Pos()))
				}
				return true
			})
		}
	}
	assert.Greater(t, checked, 10)
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
