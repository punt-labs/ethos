package provenance

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/mission"
)

type Verification struct {
	Commit    string   `json:"commit"`
	Manifests []string `json:"manifests"`
}

// StagedManifests selects only added or modified provenance paths in the
// supplied index versus HEAD. An unborn branch compares against the empty tree,
// as git diff --cached does without an explicit revision. Historical manifests
// are not reverified against today's mutable mission records.
func (r Repository) StagedManifests(ctx context.Context) ([]string, error) {
	out, err := r.git(ctx, "diff", "--cached", "--name-only", "--diff-filter=AM", "--no-renames", "-z", "--", Directory)
	if err != nil {
		return nil, fmt.Errorf("selecting staged provenance manifests: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	if out[len(out)-1] != 0 {
		return nil, fmt.Errorf("malformed staged provenance path list")
	}
	names := strings.Split(string(out[:len(out)-1]), "\x00")
	for _, name := range names {
		if !strings.HasPrefix(name, Directory) || !safePath(name) {
			return nil, fmt.Errorf("invalid staged provenance path %q", name)
		}
	}
	return names, nil
}

// VerifyIndex checks the selected manifests against the actual committing
// index supplied by the caller's immutable environment, including alternate
// Git indexes. It does not install or repair missing records.
func (r Repository) VerifyIndex(ctx context.Context, names []string) error {
	if len(names) == 0 {
		return fmt.Errorf("index verification requires manifest references")
	}
	for _, name := range names {
		if _, err := r.VerifiedManifest(ctx, ":", name); err != nil {
			return err
		}
	}
	return nil
}

// VerifiedManifest validates a historical snapshot before a UI displays it.
func (r Repository) VerifiedManifest(ctx context.Context, ref, name string) (*Manifest, error) {
	m, err := r.LoadManifest(ctx, ref, name)
	if err != nil {
		return nil, err
	}
	if err := r.verifyManifest(ctx, ref, name, m); err != nil {
		return nil, err
	}
	return m, nil
}

// VerifyCommit checks explicit references in the REAL commit message, and every
// record/source blob at that commit. It never reads today's canonical store or
// requires documentary branch objects to exist.
func (r Repository) VerifyCommit(ctx context.Context, ref string) (Verification, error) {
	sha, err := r.Resolve(ctx, ref)
	if err != nil {
		return Verification{}, err
	}
	body, err := r.git(ctx, "log", "-1", "--format=%B", sha)
	if err != nil {
		return Verification{}, err
	}
	names := trailerValues(string(body), "Ethos-Provenance")
	if len(names) == 0 {
		return Verification{}, fmt.Errorf("commit %s has no Ethos-Provenance reference", sha)
	}
	result := Verification{Commit: sha}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			return Verification{}, fmt.Errorf("duplicate provenance reference %s", name)
		}
		seen[name] = true
		m, err := r.LoadManifest(ctx, sha, name)
		if err != nil {
			return Verification{}, err
		}
		if err := r.verifyManifest(ctx, sha, name, m); err != nil {
			return Verification{}, err
		}
		result.Manifests = append(result.Manifests, name)
	}
	return result, nil
}

func (r Repository) LoadManifest(ctx context.Context, ref, name string) (*Manifest, error) {
	if !strings.HasPrefix(name, Directory) || !safePath(name) {
		return nil, fmt.Errorf("invalid provenance reference %q", name)
	}
	data, err := r.record(ctx, ref, name)
	if err != nil {
		return nil, fmt.Errorf("reading referenced manifest %s: %w", name, err)
	}
	return Decode(name, data)
}

// SquashMessage accepts the final publication commit, which must add manifests.
// Obtain a new publication after changing source. Pass this body to gh pr merge
// --squash --match-head-commit <resolved ref> --body-file <generated file>, then
// verify the actual returned/fetched merge commit. No remote command runs here.
func (r Repository) SquashMessage(ctx context.Context, ref string) (string, string, error) {
	sha, err := r.Resolve(ctx, ref)
	if err != nil {
		return "", "", err
	}
	out, err := r.git(ctx, "diff-tree", "--root", "--no-commit-id", "--name-only", "--diff-filter=A", "-r", "-z", sha, "--", Directory)
	if err != nil {
		return "", "", err
	}
	var names []string
	for _, name := range strings.Split(string(out), "\x00") {
		if name == "" {
			continue
		}
		m, err := r.LoadManifest(ctx, sha, name)
		if err != nil {
			return "", "", err
		}
		if err := r.verifyManifest(ctx, sha, name, m); err != nil {
			return "", "", err
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return "", "", fmt.Errorf("reference must be the final publication commit adding a provenance manifest")
	}
	sort.Strings(names)
	var body strings.Builder
	body.WriteString("Ethos selected blame records and recorded source associations.\n\n")
	for _, name := range names {
		fmt.Fprintf(&body, "Ethos-Provenance: %s\n", name)
	}
	return body.String(), sha, nil
}

func (r Repository) verifyManifest(ctx context.Context, ref, name string, m *Manifest) error {
	if err := validate(*m); err != nil {
		return err
	}
	listed := map[string]bool{}
	snapshot := mission.PublicationSnapshot{Missions: m.Missions}
	for _, record := range m.Records {
		data, err := r.record(ctx, ref, record.Path)
		if err != nil {
			return fmt.Errorf("required record missing %s: %w", record.Path, err)
		}
		if Digest(data) != record.SHA256 {
			return fmt.Errorf("required record altered: %s", record.Path)
		}
		listed[record.Path] = true
		snapshot.Records = append(snapshot.Records, mission.PublicationRecord{Path: record.Path, Data: data})
	}
	for _, id := range m.Missions {
		prefix := ".punt-labs/ethos/missions/" + id + "/"
		var out []byte
		var err error
		if ref == ":" {
			out, err = r.git(ctx, "ls-files", "-z", "--", prefix)
		} else {
			out, err = r.git(ctx, "ls-tree", "-r", "--name-only", "-z", ref, "--", prefix)
		}
		if err != nil {
			return err
		}
		for _, p := range strings.Split(string(out), "\x00") {
			if p == "" {
				continue
			}
			rel := strings.TrimPrefix(p, prefix)
			if rel == "contract.yaml" || rel == "results.yaml" || rel == "corrections.yaml" || strings.HasPrefix(rel, "delegations/") && strings.HasSuffix(rel, "/record.yaml") {
				if !listed[p] {
					return fmt.Errorf("record omitted from inventory: %s", p)
				}
			} else if !supportedTracePath(rel) {
				return fmt.Errorf("unknown selected mission record shape: %s", p)
			}
		}
	}
	for _, a := range m.Associations {
		if _, err := r.AgentAt(ctx, ref, a); err != nil {
			return err
		}
	}
	if len(m.Summaries) > 0 {
		data, err := r.record(ctx, ref, SummaryPath)
		if err != nil {
			return err
		}
		rows, _, err := SummaryRows(data)
		if err != nil {
			return err
		}
		for _, row := range m.Summaries {
			summary, ok := rows[row.Mission]
			if !ok || Digest(summary) != row.SHA256 {
				return fmt.Errorf("selected summary missing or altered: %s", row.Mission)
			}
			snapshot.Summary = append(snapshot.Summary, summary...)
			snapshot.Summary = append(snapshot.Summary, '\n')
		}
	}
	if err := mission.ValidatePublicationSnapshot(snapshot); err != nil {
		return fmt.Errorf("manifest %s: %w", name, err)
	}
	for _, source := range m.Sources {
		blob, err := r.git(ctx, "rev-parse", treePath(ref, source.Path))
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(blob)) != source.Blob {
			return fmt.Errorf("source blob mismatch for %s; regenerate publication before squash", source.Path)
		}
		data, err := r.read(ctx, ref, source.Path)
		if err != nil {
			return err
		}
		lines := bytes.Count(data, []byte{'\n'})
		if len(data) > 0 && data[len(data)-1] != '\n' {
			lines++
		}
		for _, rng := range source.Ranges {
			if rng.End > lines {
				return fmt.Errorf("source range exceeds blob in %s", source.Path)
			}
		}
	}
	return nil
}

func supportedTracePath(rel string) bool {
	if rel == "reflections.yaml" || rel == "log.jsonl" || strings.HasPrefix(rel, "artifacts/") {
		return true
	}
	if !strings.Contains(rel, "/") && strings.HasPrefix(rel, "log-") && (strings.HasSuffix(rel, ".jsonl") || strings.HasSuffix(rel, ".quarantine")) {
		return true
	}
	parts := strings.Split(rel, "/")
	return len(parts) >= 3 && parts[0] == "delegations" && component(parts[1]) && (len(parts) == 3 && parts[2] == "prompt.md" || len(parts) >= 4 && parts[2] == "artifacts")
}

func (r Repository) AgentAt(ctx context.Context, ref string, a Association) (string, error) {
	if !component(a.Mission) || !component(a.Delegation) {
		return "", fmt.Errorf("invalid mission/delegation tuple")
	}
	data, err := r.record(ctx, ref, pairPath(a))
	if err != nil {
		return "", err
	}
	var d mission.Delegation
	if err := yaml.Unmarshal(data, &d); err != nil {
		return "", err
	}
	if d.ID != a.Delegation || d.Mission != a.Mission || d.AgentType == "" {
		return "", fmt.Errorf("delegation identity/agent mismatch at %s", pairPath(a))
	}
	return d.AgentType, nil
}

// AtLine uses porcelain's original coordinates and reads snapshots at the
// blamed commit. Legacy attribution requires one trailer pair: no ticket or
// first-delegation heuristics and no today's-mutable-record fallback.
func (r Repository) AtLine(ctx context.Context, commit, p string, line int) ([]Association, error) {
	body, err := r.git(ctx, "log", "-1", "--format=%B", commit)
	if err != nil {
		return nil, err
	}
	names := trailerValues(string(body), "Ethos-Provenance")
	if len(names) == 0 {
		mids, dids := trailerValues(string(body), "Mission"), trailerValues(string(body), "Delegation")
		if len(mids) == 1 && len(dids) == 1 {
			return []Association{{Mission: mids[0], Delegation: dids[0]}}, nil
		}
		return nil, nil
	}
	var result []Association
	seen := map[string]bool{}
	for _, name := range names {
		m, err := r.LoadManifest(ctx, commit, name)
		if err != nil {
			return nil, err
		}
		if err := r.verifyManifest(ctx, commit, name, m); err != nil {
			return nil, err
		}
		blob, err := r.git(ctx, "rev-parse", treePath(commit, p))
		if err != nil {
			return nil, err
		}
		pairs, err := m.Lookup(p, strings.TrimSpace(string(blob)), line)
		if err != nil {
			return nil, err
		}
		for _, a := range pairs {
			if !seen[pairKey(a)] {
				seen[pairKey(a)] = true
				result = append(result, a)
			}
		}
	}
	return result, nil
}
