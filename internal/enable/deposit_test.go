package enable

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDepositBootstrapGrandfathersExistingGuide(t *testing.T) {
	dir := t.TempDir()
	// A guide already on disk from a prior non-manifest enable, no manifest.
	if err := os.MkdirAll(filepath.Join(dir, ".punt-labs", "ethos"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, guideRel), []byte("old guide\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := deposit(dir, []byte("new guide\n")); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, guideRel)); string(got) != "new guide\n" {
		t.Errorf("guide = %q, want overwritten to new guide", got)
	}
	if !exists(filepath.Join(dir, manifestRel)) {
		t.Error("manifest not written")
	}
}

func TestDepositCollisionOnUnlistedExistingPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".punt-labs", "ethos"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A previous manifest that lists only the guide.
	if err := os.WriteFile(filepath.Join(dir, manifestRel), []byte(guideRel+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// manifestRel exists but is not in the previous set → collision.
	if err := deposit(dir, []byte("guide\n")); err == nil {
		t.Fatal("expected a collision error")
	}
}

func TestReadManifestAbsent(t *testing.T) {
	paths, err := readManifest(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	if paths != nil {
		t.Errorf("paths = %v, want nil for an absent manifest", paths)
	}
}

func TestManifestBytesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m")
	set := []string{guideRel, manifestRel}
	if err := os.WriteFile(p, manifestBytes(set), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readManifest(p)
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	if len(got) != 2 || got[0] != guideRel || got[1] != manifestRel {
		t.Errorf("round-trip = %v, want %v", got, set)
	}
}
