package autorepair

import "testing"

// Exports for the external test package, which may import internal/hook
// (an in-package test importing hook would cycle once hook imports this
// package).
var RunLiveTails = runLiveTails

// DamageEnv is damageEnv for package autorepair_test.
func DamageEnv(t *testing.T, name string) Env {
	env, _ := damageEnv(t, name)
	return env
}

// FixtureHome is fixtureHome for package autorepair_test.
func FixtureHome(t *testing.T) string { return fixtureHome(t) }

// ResolveHome is resolveHome for package autorepair_test.
func ResolveHome(t *testing.T, dir, home string) { resolveHome(t, dir, home) }
