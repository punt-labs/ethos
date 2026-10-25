package layout

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLocateReportsRelError: filepath.Rel fails when one path is relative
// and the other is not. That is an error to report, not a hooks directory
// outside the tree.
func TestLocateReportsRelError(t *testing.T) {
	abs := t.TempDir()
	if _, _, err := locate("relative/root", abs); err == nil {
		t.Error("locate(relative root, absolute dir) returned no error")
	}
	if _, err := HookEntries("relative/root", abs); err == nil {
		t.Error("HookEntries(relative root, absolute dir) returned no error")
	}
}

// TestSameOrUnder: a zone is recognised by file identity, not by spelling, so
// a case variant on a case-insensitive filesystem or a link to the zone is
// under it.
func TestSameOrUnder(t *testing.T) {
	root := t.TempDir()
	zone := filepath.Join(root, ".punt-labs")
	if err := os.MkdirAll(filepath.Join(zone, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(zone, filepath.Join(root, "alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "other", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		dir  string
		want bool
	}{
		{"zone itself", zone, true},
		{"beneath the zone", filepath.Join(zone, "hooks"), true},
		{"beneath a link to the zone", filepath.Join(root, "alias", "hooks"), true},
		{"not yet created, beneath the zone", filepath.Join(zone, "new", "hooks"), true},
		{"unrelated", filepath.Join(root, "other", "hooks"), false},
		{"root", root, false},
	}
	for _, tt := range tests {
		got, err := sameOrUnder(root, tt.dir, ".punt-labs")
		if err != nil || got != tt.want {
			t.Errorf("%s: sameOrUnder = %v, %v; want %v", tt.name, got, err, tt.want)
		}
	}
	if got, err := sameOrUnder(root, filepath.Join(root, "x"), ".missing"); err != nil || got {
		t.Errorf("absent zone: sameOrUnder = %v, %v; want false", got, err)
	}
}

// caseInsensitive reports whether the filesystem holding dir folds case.
func caseInsensitive(t *testing.T, dir string) bool {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "probe"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := os.Stat(filepath.Join(dir, "PROBE"))
	return err == nil
}

// TestHooksPathCaseVariantOfPuntLabs: on a case-insensitive filesystem
// core.hooksPath=.Punt-Labs/hooks is the .punt-labs zone, and gets no source
// entry. Skipped where the filesystem is case-sensitive; TestSameOrUnder covers
// the identity test there.
func TestHooksPathCaseVariantOfPuntLabs(t *testing.T) {
	root := t.TempDir()
	if !caseInsensitive(t, root) {
		t.Skip("case-sensitive filesystem")
	}
	if err := os.MkdirAll(filepath.Join(root, ".punt-labs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, z := range []string{".Punt-Labs", ".PUNT-LABS"} {
		got, err := HookEntries(root, filepath.Join(root, z, "hooks"))
		if err != nil || len(got) != 0 {
			t.Errorf("HookEntries under %s = %v, %v; want none", z, got, err)
		}
	}
}
