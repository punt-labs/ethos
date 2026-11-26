package hook

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/punt-labs/ethos/v4/internal/attribute"
	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

const (
	handoffDataBytes  = 64 << 20
	handoffDataHeader = 8 + sha256.Size
)

type handoffData struct {
	checkset.SnapshotData
	file, checkout, agent string
	digest                [sha256.Size]byte
}

func newHandoffData(loc layout.Policy, spec checkset.SnapshotData, checkout, agent string) handoffData {
	key := sha256.Sum256([]byte(spec.URL))
	return handoffData{
		SnapshotData: spec, checkout: checkout, agent: agent,
		file: filepath.Join(loc.LocalRoot(), "handoff-data-v1", fmt.Sprintf("%x", key)),
	}
}

func (d handoffData) govulndb() bool {
	return strings.TrimSuffix(d.URL, "/") == "https://vuln.go.dev"
}

func (d handoffData) unavailable() error {
	cmd := "ethos handoff refresh-data"
	if d.agent != "" && attribute.ValidateSlug(d.agent) == nil {
		cmd += " --agent " + d.agent
	}
	dir := handoffShellPath(d.checkout)
	if editRedact(dir) != dir {
		if ref := handoffDataWorktreeRef(d.checkout); ref != "" {
			return fmt.Errorf("data-unavailable: %q is missing, expired or invalid; run %s --worktree %s", d.Name, cmd, audit.ShellQuote(ref))
		}
	}
	return fmt.Errorf("data-unavailable: %q is missing, expired or invalid; run (cd %s && %s)", d.Name, dir, cmd)
}

func handoffDataWorktreeRef(checkout string) string {
	dir, common, err := resolve.HandGitDirs(checkout)
	if err != nil {
		return ""
	}
	if dir == common {
		return "primary"
	}
	if filepath.Dir(dir) == filepath.Join(common, "worktrees") {
		return "linked:" + filepath.Base(dir)
	}
	return ""
}

// HandoffDataCheckout selects a registered worktree in the current repository.
func HandoffDataCheckout(loc resolve.Located, ref string) (resolve.Located, error) {
	if ref == "" {
		return loc, nil
	}
	if err := loc.Err(); err != nil {
		return resolve.Located{}, err
	}
	list, err := gitenv.Output(loc.Checkout, "git", "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return resolve.Located{}, errors.New("cannot list the repository worktrees for refresh-data")
	}
	for field := range strings.SplitSeq(string(list), "\x00") {
		checkout, found := strings.CutPrefix(field, "worktree ")
		if found && handoffDataWorktreeRef(checkout) == ref {
			root, store, err := resolve.CheckoutAndStoreOf(checkout)
			if err == nil && root != "" && store == loc.Store {
				loc.Checkout = root
				return loc, nil
			}
			break
		}
	}
	return resolve.Located{}, fmt.Errorf("refresh-data worktree %q is not registered in this repository", ref)
}

// RefreshHandoffData fetches the selected policy's data outside the gate.
func RefreshHandoffData(ctx context.Context, loc resolve.Located, agent string) ([]string, error) {
	return refreshHandoffData(ctx, loc, agent, handoffRuntimeHTTPClient())
}

func refreshHandoffData(ctx context.Context, loc resolve.Located, agent string, client *http.Client) ([]string, error) {
	if agent != "" {
		if err := attribute.ValidateSlug(agent); err != nil {
			return nil, err
		}
	}
	checks, _, err := stageChecksForLocation(loc, agent, "handoff")
	if err != nil {
		return nil, err
	}
	policy, err := loc.Layout()
	if err != nil {
		return nil, err
	}
	var refreshed []string
	seen := make(map[string]bool)
	for _, check := range checks {
		if check.InvalidSnapshotSidecar {
			return nil, errors.New("invalid snapshot sidecar")
		}
		if check.WorktreeSnapshot == nil {
			continue
		}
		for _, spec := range check.WorktreeSnapshot.Data {
			data := newHandoffData(policy, spec, loc.Checkout, agent)
			if seen[data.file] {
				continue
			}
			if err := data.refresh(ctx, policy, client); err != nil {
				return nil, err
			}
			seen[data.file] = true
			refreshed = append(refreshed, spec.Name)
		}
	}
	if len(refreshed) == 0 {
		return nil, errors.New("selected handoff policy declares no data")
	}
	return refreshed, nil
}

func (d handoffData) refresh(ctx context.Context, policy layout.Policy, client *http.Client) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	asset := d.URL
	if d.govulndb() {
		asset = "https://vuln.go.dev/vulndb.zip"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset, nil)
	if err != nil {
		return fmt.Errorf("invalid URL for handoff data %q", d.Name)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("cannot fetch handoff data %q", d.Name)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cannot fetch handoff data %q: HTTP %d", d.Name, resp.StatusCode)
	}
	return d.install(ctx, policy, resp.Body)
}

func (d handoffData) install(ctx context.Context, policy layout.Policy, src io.Reader) error {
	if err := layout.EnsureLocalDir(policy, filepath.Dir(d.file)); err != nil {
		return err
	}
	out, err := layout.CreateLocalTemp(policy, "handoff-data", ".bin")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	var header [handoffDataHeader]byte
	if _, err := out.Write(header[:]); err != nil {
		return err
	}
	h := sha256.New()
	size, err := io.Copy(&gateHashBudget{ctx: ctx, dst: io.MultiWriter(out, h), remaining: handoffDataBytes}, src)
	if err != nil || size == 0 {
		return fmt.Errorf("cannot read handoff data %q within the download budget", d.Name)
	}
	if d.govulndb() {
		if err := unpackGovulndb(ctx, io.NewSectionReader(out, handoffDataHeader, size), size, nil); err != nil {
			return fmt.Errorf("invalid handoff data %q: %w", d.Name, err)
		}
	}
	binary.BigEndian.PutUint64(header[:8], uint64(time.Now().Unix()))
	copy(header[8:], h.Sum(nil))
	if _, err := out.WriteAt(header[:], 0); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(out.Name(), d.file)
}

func (d handoffData) open(ctx context.Context) (*os.File, int64, [sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	root, err := os.OpenRoot(filepath.Dir(d.file))
	if err != nil {
		return nil, 0, digest, d.unavailable()
	}
	defer root.Close()
	f, err := openGateInput(root, filepath.Base(d.file))
	if err != nil {
		return nil, 0, digest, d.unavailable()
	}
	size, digest, err := d.verifyFile(ctx, f)
	if err != nil {
		_ = f.Close()
		return nil, 0, digest, d.unavailable()
	}
	return f, size, digest, nil
}

func (d handoffData) verifyFile(ctx context.Context, f *os.File) (int64, [sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= handoffDataHeader || info.Size() > handoffDataHeader+handoffDataBytes {
		return 0, digest, d.unavailable()
	}
	var header [handoffDataHeader]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return 0, digest, err
	}
	fetched := time.Unix(int64(binary.BigEndian.Uint64(header[:8])), 0)
	age := time.Since(fetched)
	if age < 0 || age.Hours() >= float64(d.MaxAgeHours) {
		return 0, digest, d.unavailable()
	}
	h := sha256.New()
	size, err := io.Copy(&gateHashBudget{ctx: ctx, dst: h, remaining: handoffDataBytes}, f)
	copy(digest[:], h.Sum(nil))
	if err != nil || size != info.Size()-handoffDataHeader || !slices.Equal(digest[:], header[8:]) {
		return 0, digest, d.unavailable()
	}
	return size, digest, nil
}

func selectHandoffData(ctx context.Context, loc layout.Policy, profile *checkset.WorktreeSnapshot, checkout, agent string) ([]handoffData, error) {
	if profile == nil {
		return nil, nil
	}
	data := make([]handoffData, 0, len(profile.Data))
	for _, spec := range profile.Data {
		item := newHandoffData(loc, spec, checkout, agent)
		f, _, digest, err := item.open(ctx)
		if err != nil {
			return nil, err
		}
		_ = f.Close()
		item.digest = digest
		data = append(data, item)
	}
	return data, nil
}

func materializeHandoffData(ctx context.Context, runtime string, data []handoffData) error {
	dir := filepath.Join(runtime, "data")
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return errors.New("cannot reserve runtime/data for the declared handoff data")
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return errors.New("cannot reserve runtime/data for the declared handoff data")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, item := range data {
		if err := item.materialize(ctx, root); err != nil {
			return err
		}
	}
	return nil
}

func (d handoffData) materialize(ctx context.Context, root *os.Root) error {
	f, size, digest, err := d.open(ctx)
	if err != nil {
		return err
	}
	defer f.Close()
	if digest != d.digest {
		return d.unavailable()
	}
	var payload bytes.Buffer
	if _, err := io.Copy(&gateHashBudget{ctx: ctx, dst: &payload, remaining: size}, io.NewSectionReader(f, handoffDataHeader, size)); err != nil ||
		int64(payload.Len()) != size || sha256.Sum256(payload.Bytes()) != digest {
		return d.unavailable()
	}
	var materializeErr error
	if d.govulndb() {
		materializeErr = root.MkdirAll(filepath.Dir(d.Mount), 0o700)
		if materializeErr == nil {
			materializeErr = root.Mkdir(d.Mount, 0o700)
		}
		if materializeErr == nil {
			var dest *os.Root
			dest, materializeErr = root.OpenRoot(d.Mount)
			if materializeErr == nil {
				materializeErr = errors.Join(unpackGovulndb(ctx, bytes.NewReader(payload.Bytes()), size, dest), dest.Close())
			}
		}
	} else {
		materializeErr = writeHandoffDataFile(ctx, root, d.Mount, &payload, size)
	}
	if errors.Is(materializeErr, os.ErrExist) {
		return fmt.Errorf("handoff data %q mount already exists", d.Name)
	}
	if materializeErr != nil {
		return fmt.Errorf("cannot materialize handoff data %q", d.Name)
	}
	return nil
}

func unpackGovulndb(ctx context.Context, f io.ReaderAt, size int64, dest *os.Root) error {
	archive, err := zip.NewReader(f, size)
	if err != nil {
		return err
	}
	if len(archive.File) > 25_000 {
		return errors.New("too many database entries")
	}
	budget := int64(128 << 20)
	seen := make(map[string]bool)
	for _, entry := range archive.File {
		if !entry.Mode().IsRegular() || !filepath.IsLocal(entry.Name) || path.Clean(entry.Name) != entry.Name ||
			path.Ext(entry.Name) != ".json" || strings.Contains(entry.Name, "\\") || seen[entry.Name] ||
			entry.UncompressedSize64 > uint64(budget) {
			return errors.New("invalid database entry")
		}
		seen[entry.Name] = true
		budget -= int64(entry.UncompressedSize64)
		src, err := entry.Open()
		if err != nil {
			return err
		}
		var copyErr error
		if dest == nil {
			var raw bytes.Buffer
			_, copyErr = io.Copy(&gateHashBudget{ctx: ctx, dst: &raw, remaining: int64(entry.UncompressedSize64)}, src)
			if copyErr == nil && !json.Valid(raw.Bytes()) {
				copyErr = errors.New("invalid database JSON")
			}
		} else {
			copyErr = writeHandoffDataFile(ctx, dest, entry.Name, src, int64(entry.UncompressedSize64))
		}
		if err := errors.Join(copyErr, src.Close()); err != nil {
			return err
		}
	}
	if !seen["index/db.json"] || !seen["index/modules.json"] || !seen["index/vulns.json"] {
		return errors.New("database index is incomplete")
	}
	return nil
}

func writeHandoffDataFile(ctx context.Context, root *os.Root, name string, src io.Reader, limit int64) error {
	if err := root.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return err
	}
	_, err = io.Copy(&gateHashBudget{ctx: ctx, dst: out, remaining: limit}, src)
	return errors.Join(err, out.Close())
}
