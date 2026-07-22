package claudemd

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

const canonical = "@.punt-labs/ethos/CLAUDE.md"

// write a fixture file and return its path.
func fixture(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("reading %s: %v", p, err)
	}
	return string(data)
}

func TestRegisterAppendsEOL(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"LF", "a\nb\n", "a\nb\n" + canonical + "\n"},
		{"CRLF", "a\r\nb\r\n", "a\r\nb\r\n" + canonical + "\r\n"},
		{"lone CR", "a\rb\r", "a\rb\r" + canonical + "\r"},
		{"no trailing newline LF", "a\nb", "a\nb\n" + canonical + "\n"},
		{"no trailing newline plain", "solo", "solo\n" + canonical + "\n"},
		{"empty file", "", canonical + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := fixture(t, "CLAUDE.md", tt.body)
			wrote, err := Register(p, canonical)
			if err != nil {
				t.Fatalf("Register: %v", err)
			}
			if !wrote {
				t.Fatal("Register reported no write")
			}
			if got := read(t, p); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRegisterCreatesMissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "CLAUDE.md")
	wrote, err := Register(p, canonical)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !wrote {
		t.Fatal("Register reported no write for a new file")
	}
	if got := read(t, p); got != canonical+"\n" {
		t.Errorf("got %q, want %q", got, canonical+"\n")
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("new file mode = %o, want 0644", info.Mode().Perm())
	}
}

func TestRegisterIdempotent(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"LF host already carries the line", "a\n" + canonical + "\n"},
		{"CRLF host carries the line with CR", "a\r\n" + canonical + "\r\n"},
		{"line is the last line without terminator", "a\n" + canonical},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := fixture(t, "CLAUDE.md", tt.body)
			wrote, err := Register(p, canonical)
			if err != nil {
				t.Fatalf("Register: %v", err)
			}
			if wrote {
				t.Error("Register wrote a duplicate")
			}
			if got := read(t, p); got != tt.body {
				t.Errorf("file changed: got %q, want %q", got, tt.body)
			}
		})
	}
}

func TestRegisterSkipsCodeBlocks(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"backtick fence", "text\n```\n" + canonical + "\n```\n"},
		{"backtick fence with info string", "text\n```text\n" + canonical + "\n```\n"},
		{"tilde fence", "text\n~~~\n" + canonical + "\n~~~\n"},
		{"tilde fence with info string", "text\n~~~markdown\n" + canonical + "\n~~~\n"},
		{"indented four spaces", "text\n\n    " + canonical + "\n"},
		{"indented tab", "text\n\n\t" + canonical + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := fixture(t, "CLAUDE.md", tt.body)
			// The only match is inside a code block, so Register must
			// append a fresh top-level line.
			wrote, err := Register(p, canonical)
			if err != nil {
				t.Fatalf("Register: %v", err)
			}
			if !wrote {
				t.Fatal("Register treated a code-block line as present")
			}
			want := tt.body
			if !endsWithTerminator([]byte(tt.body)) {
				want += "\n"
			}
			want += canonical + "\n"
			if got := read(t, p); got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

func TestRegisterErrorsOnOpenFence(t *testing.T) {
	// A host ending inside an unterminated fence is malformed: Register must
	// error and write nothing rather than append inside the open fence.
	body := "text\n```sh\necho hi\n"
	p := fixture(t, "CLAUDE.md", body)
	wrote, err := Register(p, canonical)
	if err == nil {
		t.Fatal("Register accepted a host ending inside an open fence")
	}
	if wrote {
		t.Error("Register reported a write despite the error")
	}
	if got := read(t, p); got != body {
		t.Errorf("file changed on error: got %q, want %q", got, body)
	}
}

func TestRegisterClosedFenceAppendsAndIsIdempotent(t *testing.T) {
	// A properly closed fence is not open at EOF: Register appends a
	// top-level line and a re-run does not duplicate it.
	body := "text\n```sh\necho hi\n```\n"
	p := fixture(t, "CLAUDE.md", body)
	wrote, err := Register(p, canonical)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !wrote {
		t.Fatal("Register did not append past a closed fence")
	}
	want := body + canonical + "\n"
	if got := read(t, p); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	wrote, err = Register(p, canonical)
	if err != nil {
		t.Fatalf("re-Register: %v", err)
	}
	if wrote {
		t.Error("re-Register appended a duplicate")
	}
}

func TestTrailingWhitespaceTolerantMatch(t *testing.T) {
	// A hand-edited import line with trailing whitespace must be recognized:
	// Register does not duplicate it, and Deregister removes it.
	body := "top\n" + canonical + "   \n"
	p := fixture(t, "CLAUDE.md", body)
	wrote, err := Register(p, canonical)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if wrote {
		t.Error("Register appended a duplicate beside a trailing-whitespace line")
	}
	removed, err := Deregister(p, canonical)
	if err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	if !removed {
		t.Fatal("Deregister did not remove the trailing-whitespace line")
	}
	if got := read(t, p); got != "top\n" {
		t.Errorf("got %q, want %q", got, "top\n")
	}
}

func TestDeregisterRemovesTopLevelOnly(t *testing.T) {
	body := "top\n" + canonical + "\n```\n" + canonical + "\n```\n"
	p := fixture(t, "CLAUDE.md", body)
	wrote, err := Deregister(p, canonical)
	if err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	if !wrote {
		t.Fatal("Deregister reported no write")
	}
	want := "top\n```\n" + canonical + "\n```\n"
	if got := read(t, p); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDeregisterCollapsesDuplicates(t *testing.T) {
	body := "top\n" + canonical + "\nmid\n" + canonical + "\n"
	p := fixture(t, "CLAUDE.md", body)
	wrote, err := Deregister(p, canonical)
	if err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	if !wrote {
		t.Fatal("Deregister reported no write")
	}
	want := "top\nmid\n"
	if got := read(t, p); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDeregisterCRLF(t *testing.T) {
	body := "a\r\n" + canonical + "\r\nb\r\n"
	p := fixture(t, "CLAUDE.md", body)
	if _, err := Deregister(p, canonical); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	want := "a\r\nb\r\n"
	if got := read(t, p); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDeregisterMissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "CLAUDE.md")
	wrote, err := Deregister(p, canonical)
	if err != nil {
		t.Fatalf("Deregister on missing file: %v", err)
	}
	if wrote {
		t.Error("Deregister reported a write for a missing file")
	}
}

func TestSymlinkWriteThrough(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real-CLAUDE.md")
	if err := os.WriteFile(real, []byte("host\n"), 0o640); err != nil {
		t.Fatalf("writing real: %v", err)
	}
	link := filepath.Join(dir, "CLAUDE.md")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := Register(link, canonical); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// The link must survive and the real file must carry the line.
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat link: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("link was replaced by a regular file")
	}
	if got := read(t, real); got != "host\n"+canonical+"\n" {
		t.Errorf("real target = %q", got)
	}
	// Mode of the real file is preserved.
	ri, err := os.Stat(real)
	if err != nil {
		t.Fatalf("stat real: %v", err)
	}
	if ri.Mode().Perm() != 0o640 {
		t.Errorf("real mode = %o, want 0640", ri.Mode().Perm())
	}
}

func TestModePreserved(t *testing.T) {
	p := fixture(t, "CLAUDE.md", "host\n")
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := Register(p, canonical); err != nil {
		t.Fatalf("Register: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestLockContentionNoLostUpdate(t *testing.T) {
	p := fixture(t, "CLAUDE.md", "host\n")
	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Register(p, canonical); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Register: %v", err)
	}
	if got, want := len(matchIndices([]byte(read(t, p)), canonical)), 1; got != want {
		t.Errorf("line appears %d times, want %d", got, want)
	}
}
