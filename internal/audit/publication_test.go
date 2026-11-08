package audit

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func publicationFixture(t *testing.T) (layout.Policy, string, string) {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("staged source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "source.txt")
	p, err := layout.NewRepository(root, root, layout.RecordFull)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(p.CheckoutRecordRoot(), "sessions", "2026-11-04-s", SessionChunkFile(100, 100))
	if err := WriteLocalAtomic(p, path, []byte(`{"ts":"`+FormatLineTS(100)+`","record":"full"}`+"\n")); err != nil {
		t.Fatal(err)
	}
	return p, path, filepath.Join(root, ".git", "index")
}

func TestRepositoryPublicationFailurePreservesActualIndex(t *testing.T) {
	p, path, index := publicationFixture(t)
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index+".lock", []byte("foreign lock"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := StagePublication(p, []string{path}); err == nil {
		t.Fatal("foreign index lock ignored")
	}
	after, err := os.ReadFile(index)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("actual index changed: %v", err)
	}
	lock, err := os.ReadFile(index + ".lock")
	if err != nil || string(lock) != "foreign lock" {
		t.Fatalf("foreign lock changed: %q %v", lock, err)
	}
	if err := os.Remove(index + ".lock"); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".punt-labs/ethos/sessions/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := StagePublication(p, []string{path}); err == nil {
		t.Fatal("ignored eligible publication silently skipped")
	}
	after, err = os.ReadFile(index)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed git add changed actual index: %v", err)
	}
	entries, err := os.ReadDir(p.TempDir())
	if err != nil || len(entries) != 0 {
		t.Fatalf("candidate index debris: %v %v", entries, err)
	}
}

func TestRepositoryPublicationHonorsAlternateCommittingIndex(t *testing.T) {
	p, path, index := publicationFixture(t)
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	alt := filepath.Join(p.LocalRoot(), "alternate-index")
	if err := os.MkdirAll(filepath.Dir(alt), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(alt, before, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_INDEX_FILE", alt)
	if err := StagePublication(p, []string{path}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(index)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("ordinary index changed: %v", err)
	}
	root := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
	data, present, err := IndexBlob(root, filepath.Join(root, "source.txt"))
	if err != nil || !present || string(data) != "staged source\n" {
		t.Fatalf("source index blob=%q %v %v", data, present, err)
	}
	if _, present, err := IndexBlob(root, path); err != nil || !present {
		t.Fatalf("eligible chunk not in actual committing index: %v %v", present, err)
	}
}
