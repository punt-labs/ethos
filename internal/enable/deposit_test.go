package enable

import (
	"os"
	"path/filepath"
	"strings"
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
	warns, err := deposit(dir, []byte("new guide\n"), []byte("new setup\n"))
	if err != nil {
		t.Fatalf("deposit: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, guideRel)); string(got) != "new guide\n" {
		t.Errorf("guide = %q, want overwritten to new guide", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, setupRel)); string(got) != "new setup\n" {
		t.Errorf("setup = %q, want new setup", got)
	}
	if !exists(filepath.Join(dir, manifestRel)) {
		t.Error("manifest not written")
	}
	// The differing-content overwrite must be surfaced, not silent (S2).
	found := false
	for _, w := range warns {
		if strings.Contains(w, guideRel) && strings.Contains(w, "overwritten") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a grandfather-overwrite warning naming %s, got %v", guideRel, warns)
	}
}

func TestDepositBootstrapNoWarningWhenContentMatches(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".punt-labs", "ethos"), 0o755); err != nil {
		t.Fatal(err)
	}
	// An existing guide identical to what we deposit → no overwrite warning.
	if err := os.WriteFile(filepath.Join(dir, guideRel), []byte("same guide\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	warns, err := deposit(dir, []byte("same guide\n"), []byte("same setup\n"))
	if err != nil {
		t.Fatalf("deposit: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings for identical content: %v", warns)
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
	if _, err := deposit(dir, []byte("guide\n"), []byte("setup\n")); err == nil {
		t.Fatal("expected a collision error")
	}
}

func TestDepositWritesGuideAndSetupWithRightContent(t *testing.T) {
	dir := t.TempDir()
	if _, err := deposit(dir, []byte("guide content\n"), []byte("setup content\n")); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, guideRel)); string(got) != "guide content\n" {
		t.Errorf("guide = %q, want %q", got, "guide content\n")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, setupRel)); string(got) != "setup content\n" {
		t.Errorf("setup = %q, want %q", got, "setup content\n")
	}
	paths, err := readManifest(filepath.Join(dir, manifestRel))
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	if len(paths) != 3 || paths[0] != guideRel || paths[1] != setupRel || paths[2] != manifestRel {
		t.Errorf("manifest = %v, want [%s %s %s]", paths, guideRel, setupRel, manifestRel)
	}
}

func TestDepositSetupCollisionOnUnlistedExistingPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".punt-labs", "ethos"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A prior manifest from before tier-C shipped: guide + manifest only.
	if err := os.WriteFile(filepath.Join(dir, manifestRel), []byte(guideRel+"\n"+manifestRel+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// setupRel exists (e.g. repo-owned content at that path) but no manifest
	// has ever listed it → collision, same as the guide-collision case.
	if err := os.WriteFile(filepath.Join(dir, setupRel), []byte("repo-owned\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := deposit(dir, []byte("guide\n"), []byte("setup\n")); err == nil {
		t.Fatal("expected a collision error on the unlisted setup file")
	}
}

func TestDepositRecoversFromInterruptAfterManifestBeforeContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".punt-labs", "ethos"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Simulate a deposit that was interrupted right after the manifest was
	// rewritten to the new (tier-C) set, but before the guide and setup
	// content were written: the manifest already names setupRel, but the
	// file itself does not exist yet.
	newSet := []string{guideRel, setupRel, manifestRel}
	if err := os.WriteFile(filepath.Join(dir, manifestRel), manifestBytes(newSet), 0o644); err != nil {
		t.Fatal(err)
	}

	warns, err := deposit(dir, []byte("guide content\n"), []byte("setup content\n"))
	if err != nil {
		t.Fatalf("deposit: %v, want the retry to recover without a collision", err)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings on retry: %v", warns)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, guideRel)); string(got) != "guide content\n" {
		t.Errorf("guide = %q, want %q", got, "guide content\n")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, setupRel)); string(got) != "setup content\n" {
		t.Errorf("setup = %q, want %q", got, "setup content\n")
	}
	paths, err := readManifest(filepath.Join(dir, manifestRel))
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	if len(paths) != 3 || paths[0] != guideRel || paths[1] != setupRel || paths[2] != manifestRel {
		t.Errorf("manifest = %v, want %v", paths, newSet)
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
