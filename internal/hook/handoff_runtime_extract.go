package hook

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

func (b handoffRuntime) extract(ctx context.Context, dest string) error {
	for {
		busy, err := audit.TryLock(b.archive+".lock", func() error {
			return b.extractLocked(ctx, dest)
		})
		if !busy || err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.New("runtime extraction interrupted; retry the handoff")
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (b handoffRuntime) extractLocked(ctx context.Context, dest string) error {
	file, err := b.open()
	if err != nil {
		return err
	}
	defer file.Close()
	stage, err := os.MkdirTemp(filepath.Dir(dest), layout.ScratchDirPattern(".runtime"))
	if err != nil {
		return errors.New("runtime extraction could not stage the pinned bundle")
	}
	defer os.RemoveAll(stage)
	root, err := os.OpenRoot(stage)
	if err != nil {
		return errors.New("runtime extraction could not open the staging directory")
	}
	h := sha256.New()
	archive := io.TeeReader(file, &gateHashBudget{ctx: ctx, dst: h, remaining: handoffRuntimeArchiveBytes})
	err = errors.Join(extractHandoffRuntime(root, archive), root.Close())
	if ctx.Err() != nil {
		return errors.New("runtime extraction interrupted; retry the handoff")
	}
	if err != nil {
		return fmt.Errorf("runtime-invalid: cannot extract the pinned archive: %w", err)
	}
	if _, err := io.Copy(io.Discard, archive); err != nil || hex.EncodeToString(h.Sum(nil)) != b.platform.SHA256 {
		return b.digestMismatch()
	}
	if err := os.Rename(stage, dest); err != nil {
		return errors.New("runtime extraction could not publish the verified bundle")
	}
	return nil
}

type runtimeArchiveLink struct {
	name, target string
	symbolic     bool
}

func extractHandoffRuntime(root *os.Root, archive io.Reader) error {
	z, err := gzip.NewReader(archive)
	if err != nil {
		return err
	}
	defer z.Close()
	const maxRuntimeBytes = 16 << 30
	const maxRuntimeEntries = 250_000
	stream := &io.LimitedReader{R: z, N: maxRuntimeBytes + 1}
	tr := tar.NewReader(stream)
	seen := make(map[string]byte)
	var links []runtimeArchiveLink
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name, valid := runtimeMemberPath(header.Name)
		if !valid || (name == "." && header.Typeflag != tar.TypeDir) {
			return fmt.Errorf("unsafe archive path %q", header.Name)
		}
		if _, exists := seen[name]; exists || len(seen) >= maxRuntimeEntries || header.Size > stream.N {
			return errors.New("duplicate archive entry or archive exceeds the extraction budget")
		}
		seen[name] = header.Typeflag
		if err := root.MkdirAll(path.Dir(name), 0o755); err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeRuntimeMember(root, name, os.FileMode(header.Mode).Perm(), tr); err != nil {
				return err
			}
		case tar.TypeLink:
			target, valid := runtimeMemberPath(header.Linkname)
			if !valid || target == "." {
				return fmt.Errorf("unsafe hard link %q", name)
			}
			links = append(links, runtimeArchiveLink{name: name, target: target})
		case tar.TypeSymlink:
			target := header.Linkname
			if !validRuntimeLinkTarget(target) || !filepath.IsLocal(path.Join(path.Dir(name), target)) {
				return fmt.Errorf("unsafe symbolic link %q", name)
			}
			links = append(links, runtimeArchiveLink{name: name, target: target, symbolic: true})
		default:
			return fmt.Errorf("unsupported archive entry %q", name)
		}
	}
	if _, err := io.Copy(io.Discard, stream); err != nil {
		return err
	}
	if stream.N <= 0 {
		return errors.New("archive exceeds the extraction budget")
	}
	for _, link := range links {
		if link.symbolic {
			if err := root.Symlink(link.target, link.name); err != nil {
				return err
			}
			continue
		}
		if kind, ok := seen[link.target]; !ok || kind != tar.TypeReg {
			return fmt.Errorf("hard link %q does not target an archived regular file", link.name)
		}
		if err := root.Link(link.target, link.name); err != nil {
			return err
		}
	}
	for _, link := range links {
		if link.symbolic {
			if _, err := root.Stat(link.name); err != nil {
				return fmt.Errorf("symbolic link %q does not resolve inside the archive", link.name)
			}
		}
	}
	return nil
}

func validRuntimeLinkTarget(name string) bool {
	return name != "" && !path.IsAbs(name) && !strings.ContainsAny(name, "\\\x00")
}

func runtimeMemberPath(name string) (string, bool) {
	clean := path.Clean(name)
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", false
		}
	}
	return clean, validRuntimeLinkTarget(name) && filepath.IsLocal(clean)
}

func writeRuntimeMember(root *os.Root, name string, mode os.FileMode, data io.Reader) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, data)
	if err == nil {
		err = f.Chmod(mode)
	}
	return errors.Join(err, f.Close())
}
