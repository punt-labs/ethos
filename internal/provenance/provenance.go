// Package provenance publishes complete selected blame records and preserves
// recorded branch associations across squash merges, without remote services.
package provenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/redact"
)

const Directory = ".punt-labs/ethos/provenance/"
const SummaryPath = ".punt-labs/ethos/missions.jsonl"

// Association is a tuple, not parallel lists. It records a trailer claim,
// not authenticated authorship. Agent is always resolved from record.yaml.
type Association struct {
	Mission    string `yaml:"mission" json:"mission"`
	Delegation string `yaml:"delegation" json:"delegation"`
}

type Record struct {
	Path   string `yaml:"path" json:"path"`
	SHA256 string `yaml:"sha256" json:"sha256"`
}

type Summary struct {
	Mission string `yaml:"mission" json:"mission"`
	SHA256  string `yaml:"sha256" json:"sha256"`
}

// Evidence is inlined: original branch commits need not survive a fresh clone.
type Evidence struct {
	Commit      string   `yaml:"commit" json:"commit"`
	Path        string   `yaml:"path" json:"path"`
	Line        int      `yaml:"line" json:"line"`
	Missions    []string `yaml:"mission_trailers,omitempty" json:"mission_trailers,omitempty"`
	Delegations []string `yaml:"delegation_trailers,omitempty" json:"delegation_trailers,omitempty"`
}

type Range struct {
	Start      int           `yaml:"start" json:"start"`
	End        int           `yaml:"end" json:"end"`
	Candidates []Association `yaml:"candidates" json:"candidates"`
	Evidence   Evidence      `yaml:"evidence" json:"evidence"`
	Unknown    string        `yaml:"unknown,omitempty" json:"unknown,omitempty"`
}

type Source struct {
	Path   string  `yaml:"path" json:"path"`
	Blob   string  `yaml:"blob" json:"blob"`
	Ranges []Range `yaml:"ranges" json:"ranges"`
}

type Manifest struct {
	SchemaVersion int           `yaml:"schema_version" json:"schema_version"`
	BranchHead    string        `yaml:"branch_head" json:"branch_head"`
	Base          string        `yaml:"base" json:"base"`
	Missions      []string      `yaml:"missions" json:"missions"`
	Associations  []Association `yaml:"associations" json:"associations"`
	Records       []Record      `yaml:"records" json:"records"`
	Summaries     []Summary     `yaml:"summaries" json:"summaries"`
	Sources       []Source      `yaml:"sources" json:"sources"`
}

func Digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func Encode(m Manifest) (string, []byte, error) {
	data, err := yaml.Marshal(m)
	if err != nil {
		return "", nil, fmt.Errorf("encoding manifest: %w", err)
	}
	return Directory + Digest(data) + ".yaml", data, nil
}

func Decode(name string, data []byte) (*Manifest, error) {
	if name != Directory+Digest(data)+".yaml" {
		return nil, fmt.Errorf("manifest content hash mismatch: %s", name)
	}
	var m Manifest
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("decoding manifest: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("manifest must contain exactly one document")
	}
	if err := validate(m); err != nil {
		return nil, err
	}
	return &m, nil
}

func safePath(p string) bool {
	return p != "" && path.Clean(p) == p && !strings.HasPrefix(p, "/") && p != "." && p != ".." && !strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\\\x00\r\n:")
}
func component(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
func pairPath(a Association) string {
	return ".punt-labs/ethos/missions/" + a.Mission + "/delegations/" + a.Delegation + "/record.yaml"
}
func pairKey(a Association) string { return a.Mission + "/" + a.Delegation }

func validate(m Manifest) error {
	if m.SchemaVersion != 1 || len(m.Missions) == 0 || !objectID(m.BranchHead) || !objectID(m.Base) {
		return fmt.Errorf("unsupported or incomplete provenance manifest")
	}
	mids := map[string]bool{}
	for _, id := range m.Missions {
		if !component(id) || mids[id] {
			return fmt.Errorf("invalid/duplicate mission %q", id)
		}
		mids[id] = true
	}
	records := map[string]bool{}
	for _, r := range m.Records {
		if !safePath(r.Path) || !strings.HasPrefix(r.Path, ".punt-labs/ethos/") || !digestID(r.SHA256) || records[r.Path] {
			return fmt.Errorf("invalid/duplicate record %s", r.Path)
		}
		records[r.Path] = true
	}
	for id := range mids {
		for _, leaf := range []string{"contract.yaml", "corrections.yaml"} {
			if !records[".punt-labs/ethos/missions/"+id+"/"+leaf] {
				return fmt.Errorf("incomplete inventory for %s: %s", id, leaf)
			}
		}
	}
	pairs := map[string]bool{}
	for _, a := range m.Associations {
		if !mids[a.Mission] || !component(a.Delegation) || !records[pairPath(a)] || pairs[pairKey(a)] {
			return fmt.Errorf("invalid association %v", a)
		}
		pairs[pairKey(a)] = true
	}
	for p := range records {
		if strings.Contains(p, "/delegations/") {
			parts := strings.Split(p, "/")
			if len(parts) != 7 || !pairs[parts[3]+"/"+parts[5]] {
				return fmt.Errorf("delegation missing explicit association: %s", p)
			}
		}
	}
	summaries := map[string]bool{}
	for _, row := range m.Summaries {
		if !mids[row.Mission] || !digestID(row.SHA256) || summaries[row.Mission] {
			return fmt.Errorf("invalid/duplicate selected summary %s", row.Mission)
		}
		summaries[row.Mission] = true
	}
	files := map[string]bool{}
	for _, f := range m.Sources {
		if !safePath(f.Path) || mission.LeakRuleFor(f.Path, true) != "" || mission.InvalidText([]byte(f.Path)) != "" || strings.HasPrefix(f.Path, ".punt-labs/") || !objectID(f.Blob) || files[f.Path] {
			return fmt.Errorf("invalid, unsafe or duplicate source path")
		}
		files[f.Path] = true
		ranges := append([]Range(nil), f.Ranges...)
		sort.Slice(ranges, func(i, j int) bool { return ranges[i].Start < ranges[j].Start })
		for i := 1; i < len(ranges); i++ {
			if ranges[i].Start <= ranges[i-1].End {
				return fmt.Errorf("overlapping source ranges in %s; use explicit candidates in one range", f.Path)
			}
		}
		for _, r := range f.Ranges {
			if r.Start < 1 || r.End < r.Start || !objectID(r.Evidence.Commit) || !safePath(r.Evidence.Path) || r.Evidence.Line < 1 {
				return fmt.Errorf("invalid source range in %s", f.Path)
			}
			// Evidence is copied from branch history, not an already-redacted
			// authority. Refuse unsafe values rather than redact an identity or
			// path into a different attribution claim.
			if mission.LeakRuleFor(r.Evidence.Path, true) != "" || mission.InvalidText([]byte(r.Evidence.Path)) != "" || mission.LeakRule(r.Unknown) != "" || mission.InvalidText([]byte(r.Unknown)) != "" {
				return fmt.Errorf("unsafe branch evidence in %s", f.Path)
			}
			for _, ids := range [][]string{r.Evidence.Missions, r.Evidence.Delegations} {
				for _, id := range ids {
					if !component(id) || mission.LeakRule(id) != "" {
						return fmt.Errorf("invalid or unsafe branch trailer evidence in %s", f.Path)
					}
				}
			}
			if len(r.Candidates) == 0 && r.Unknown == "" {
				return fmt.Errorf("range has neither candidates nor explicit unknown")
			}
			if len(r.Candidates) == 1 && r.Unknown != "" {
				return fmt.Errorf("range has one candidate but is marked unknown")
			}
			expected := associations(r.Evidence.Missions, r.Evidence.Delegations, m.Associations)
			if len(expected) != len(r.Candidates) {
				return fmt.Errorf("range candidates conflict with branch trailer evidence in %s", f.Path)
			}
			seen := map[string]bool{}
			for _, a := range r.Candidates {
				if !pairs[pairKey(a)] || seen[pairKey(a)] {
					return fmt.Errorf("range references unpublished association %v", a)
				}
				seen[pairKey(a)] = true
			}
			for _, a := range expected {
				if !seen[pairKey(a)] {
					return fmt.Errorf("range candidates conflict with branch trailer evidence in %s", f.Path)
				}
			}
		}
	}
	return nil
}

func digestID(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func objectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// Repository runs only bounded managed Git starts. Env is captured at operation
// entry; publication supplies ActualIndexEnv, historical readers supply Parsed.
type Repository struct {
	Root string
	Env  []string
}

func (r Repository) git(ctx context.Context, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out, _, err := gitenv.Read(gitenv.Spec{Git: "git", Dir: r.Root, Env: r.Env, Args: args}, 0)
	return out, err
}

func (r Repository) Resolve(ctx context.Context, ref string) (string, error) {
	if ref == "" || strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\x00\r\n") {
		return "", fmt.Errorf("invalid commit reference")
	}
	out, err := r.git(ctx, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(out))
	if !objectID(sha) {
		return "", fmt.Errorf("git returned invalid commit identity")
	}
	return sha, nil
}

func (r Repository) read(ctx context.Context, ref, p string) ([]byte, error) {
	if !safePath(p) {
		return nil, fmt.Errorf("unsafe record path %q", p)
	}
	return r.git(ctx, "show", treePath(ref, p))
}

func treePath(ref, p string) string {
	if ref == ":" {
		return ":" + p
	}
	return ref + ":" + p
}

// BlameLine retains both coordinate systems, including historical rename paths.
type BlameLine struct {
	Commit, Path, Text, Author string
	Original, Final            int
}

func ParseBlame(data []byte) ([]BlameLine, error) {
	var lines []BlameLine
	var cur BlameLine
	for _, line := range strings.Split(string(data), "\n") {
		// Porcelain content is arbitrary text, including SHA-like headers.
		// Recognize its tab prefix before interpreting any metadata fields.
		if strings.HasPrefix(line, "\t") {
			if cur.Commit == "" {
				return nil, fmt.Errorf("blame content without header")
			}
			cur.Text = line[1:]
			lines = append(lines, cur)
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 3 && objectID(parts[0]) {
			orig, e1 := strconv.Atoi(parts[1])
			final, e2 := strconv.Atoi(parts[2])
			if e1 != nil || e2 != nil || orig < 1 || final < 1 {
				return nil, fmt.Errorf("malformed blame coordinates")
			}
			cur = BlameLine{Commit: parts[0], Original: orig, Final: final}
		} else if strings.HasPrefix(line, "author ") {
			cur.Author = strings.TrimPrefix(line, "author ")
		} else if strings.HasPrefix(line, "filename ") {
			p := strings.TrimPrefix(line, "filename ")
			if strings.HasPrefix(p, "\"") {
				var err error
				p, err = strconv.Unquote(p)
				if err != nil {
					return nil, err
				}
			}
			cur.Path = p
		}
	}
	return lines, nil
}

// trailerValues reads the portable colon-delimited references generated by
// SquashMessage and the legacy attribution fallback. Source publication uses
// sourceCommitTrailers instead, with the committing repository's Git syntax.
func trailerValues(body, key string) []string {
	var out []string
	// Only the final paragraph is a trailer block. A mention in narrative
	// prose must not become an attribution claim or manifest reference.
	body = strings.TrimRight(body, "\r\n")
	i := strings.LastIndex(body, "\n\n")
	if i < 0 {
		return nil // a subject alone is not a trailer block
	}
	body = body[i+2:]
	for _, line := range strings.Split(body, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok || k == "" || strings.TrimSpace(v) == "" {
			return nil // do not extract a claim from a narrative paragraph
		}
		for _, c := range k {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return nil
			}
		}
		if strings.EqualFold(k, key) {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}

type sourceTrailers struct {
	missions, delegations []string
}

type sourceTrailerConfig struct {
	mission, delegation, separators string
}

func (c sourceTrailerConfig) key(key string) string {
	if i := strings.IndexAny(key, c.separators); i >= 0 {
		key = key[:i]
	}
	return strings.TrimSpace(key)
}

// sourceTrailerKeys asks Git to canonicalize the keys the commit-message helper
// passes to it. --parse never executes configured trailer commands or adds
// configured trailers. Matching aliases ourselves would drift from Git's rules.
func (r Repository) sourceTrailerKeys(ctx context.Context) (sourceTrailerConfig, error) {
	keys := sourceTrailerConfig{mission: "Mission", delegation: "Delegation", separators: ":"}
	// Let Git supply the default too; an absent-query classifier must not
	// recapture process-global configuration instead of using r.Env.
	out, err := r.git(ctx, "config", "--default", ":", "--get", "trailer.separators")
	if err != nil {
		return keys, fmt.Errorf("reading source trailer configuration: %w", err)
	}
	keys.separators = strings.TrimSuffix(string(out), "\n")
	if keys.separators == "" || strings.ContainsAny(keys.separators, "\x00\r\n") {
		return keys, fmt.Errorf("unsupported source trailer separators")
	}
	const marker = "ethos-provenance-key"
	input := "subject\n\nMission" + keys.separators[:1] + " " + marker + "\nDelegation" + keys.separators[:1] + " " + marker + "\n"
	if err := ctx.Err(); err != nil {
		return keys, err
	}
	out, _, err = gitenv.Read(gitenv.Spec{Git: "git", Dir: r.Root, Env: r.Env,
		Args: []string{"interpret-trailers", "--parse"}, Stdin: strings.NewReader(input)}, 0)
	if err != nil {
		return keys, fmt.Errorf("normalizing source trailer keys: %w", err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 2 {
		return keys, fmt.Errorf("git did not preserve both source trailer keys")
	}
	for i, target := range []*string{&keys.mission, &keys.delegation} {
		key, ok := strings.CutSuffix(lines[i], marker)
		if !ok {
			return keys, fmt.Errorf("git did not preserve a source trailer marker")
		}
		*target = keys.key(key)
	}
	if keys.mission == "" || keys.delegation == "" || strings.EqualFold(keys.mission, keys.delegation) {
		return keys, fmt.Errorf("source trailer keys do not distinguish mission and delegation")
	}
	return keys, nil
}

func (r Repository) sourceCommitTrailers(ctx context.Context, commit string, keys sourceTrailerConfig) (sourceTrailers, error) {
	var trailers sourceTrailers
	// Pretty-format trailers use the interpret-trailers parser and canonicalize
	// aliases. Fix the output delimiter independently of trailer.separators, and
	// unfold continuation lines before copying values into manifest fields.
	// Signature display is human output, not part of the trailer stream.
	format := "%(trailers:unfold,key_value_separator=%x00,only="
	all, err := r.git(ctx, "log", "--no-show-signature", "-1", "--format="+format+"false)", commit)
	if err != nil {
		return trailers, fmt.Errorf("reading source trailer block: %w", err)
	}
	only, err := r.git(ctx, "log", "--no-show-signature", "-1", "--format="+format+"true)", commit)
	if err != nil {
		return trailers, fmt.Errorf("reading source trailers: %w", err)
	}
	// Git may recognize a mixed paragraph when a configured trailer is present.
	// Keep the stronger attribution rule: prose in that block is not authority.
	if !bytes.Equal(all, only) {
		return trailers, nil
	}
	for _, line := range strings.Split(strings.TrimRight(string(only), "\n"), "\n") {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "\x00")
		if !ok || strings.ContainsRune(value, '\x00') {
			return trailers, fmt.Errorf("malformed parsed source trailer")
		}
		// Git retains a configured key's explicit separator and spacing in
		// its canonical key, even with a custom key/value output delimiter.
		key = keys.key(key)
		switch {
		case strings.EqualFold(key, keys.mission):
			trailers.missions = append(trailers.missions, value)
		case strings.EqualFold(key, keys.delegation):
			trailers.delegations = append(trailers.delegations, value)
		}
	}
	return trailers, nil
}

func associations(mids, dids []string, selected []Association) []Association {
	// Multiple parallel values provide no tuple pairing. With one mission,
	// multiple delegations are honest candidates, never first-agent selection.
	if len(mids) != 1 {
		return nil
	}
	var out []Association
	matched := map[string]bool{}
	for _, a := range selected {
		if a.Mission == mids[0] {
			for _, d := range dids {
				if a.Delegation == d {
					out = append(out, a)
					matched[d] = true
					break
				}
			}
		}
	}
	for _, d := range dids {
		if !matched[d] {
			// A missing candidate is not evidence for the remaining one.
			return nil
		}
	}
	return out
}

// BuildSources binds all changed text-source lines to the exact branch/index
// blobs. Staged source changes must first be committed; ranges are not guessed
// onto a different candidate blob. A source with no trailer remains unknown.
func (r Repository) BuildSources(ctx context.Context, m *Manifest) error {
	out, err := r.git(ctx, "diff", "--name-only", "-z", "--diff-filter=ACMR", m.Base, m.BranchHead, "--")
	if err != nil {
		return err
	}
	commits, err := r.git(ctx, "rev-list", m.Base+".."+m.BranchHead)
	if err != nil {
		return err
	}
	branch := map[string]bool{}
	for _, sha := range strings.Fields(string(commits)) {
		branch[sha] = true
	}
	keys, err := r.sourceTrailerKeys(ctx)
	if err != nil {
		return err
	}
	trailers := map[string]sourceTrailers{}
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" || strings.HasPrefix(p, ".punt-labs/") {
			continue
		}
		if !safePath(p) {
			return fmt.Errorf("unsupported source path %q", p)
		}
		blob, err := r.git(ctx, "rev-parse", m.BranchHead+":"+p)
		if err != nil {
			return err
		}
		indexed, err := r.git(ctx, "rev-parse", ":"+p)
		if err != nil {
			return err
		}
		if !bytes.Equal(blob, indexed) {
			return fmt.Errorf("source blob mismatch for %s; commit source and regenerate publication", p)
		}
		content, err := r.read(ctx, m.BranchHead, p)
		if err != nil {
			return err
		}
		if bytes.IndexByte(content, 0) >= 0 {
			return fmt.Errorf("changed binary source %s has no supported line provenance", p)
		}
		blame, err := r.git(ctx, "blame", "--line-porcelain", m.BranchHead, "--", p)
		if err != nil {
			return err
		}
		lines, err := ParseBlame(blame)
		if err != nil {
			return err
		}
		f := Source{Path: p, Blob: strings.TrimSpace(string(blob))}
		for _, line := range lines {
			if !branch[line.Commit] {
				continue
			}
			values, ok := trailers[line.Commit]
			if !ok {
				values, err = r.sourceCommitTrailers(ctx, line.Commit, keys)
				if err != nil {
					return err
				}
				trailers[line.Commit] = values
			}
			mids, dids := values.missions, values.delegations
			candidates := associations(mids, dids, m.Associations)
			rng := Range{Start: line.Final, End: line.Final, Candidates: candidates, Evidence: Evidence{Commit: line.Commit, Path: line.Path, Line: line.Original, Missions: mids, Delegations: dids}}
			if len(candidates) != 1 {
				rng.Unknown = "branch trailers do not establish one selected mission/delegation tuple"
			}
			f.Ranges = append(f.Ranges, rng)
		}
		m.Sources = append(m.Sources, f)
	}
	return validate(*m)
}

// SummaryRows validates and canonicalizes JSONL by mission identity. Unknown
// fields survive; conflicting duplicates and malformed rows are errors.
func SummaryRows(data []byte) (map[string][]byte, []string, error) {
	rows := map[string][]byte{}
	var order []string
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var fields map[string]json.RawMessage
		if _, key, duplicate := redact.DuplicateJSONKey(line); duplicate {
			return nil, nil, fmt.Errorf("duplicate summary key %q", key)
		}
		if err := json.Unmarshal(line, &fields); err != nil {
			return nil, nil, fmt.Errorf("malformed summary row: %w", err)
		}
		var id string
		if err := json.Unmarshal(fields["id"], &id); err != nil || !component(id) {
			return nil, nil, fmt.Errorf("summary row has invalid mission identity")
		}
		canonical, err := json.Marshal(fields)
		if err != nil {
			return nil, nil, err
		}
		if old, ok := rows[id]; ok {
			if !bytes.Equal(old, canonical) {
				return nil, nil, fmt.Errorf("conflicting summary rows for %s", id)
			}
			continue
		}
		rows[id] = canonical
		order = append(order, id)
	}
	return rows, order, nil
}

func MergeSummary(existing, selected []byte) ([]byte, error) {
	rows, _, err := SummaryRows(existing)
	if err != nil {
		return nil, err
	}
	newRows, newOrder, err := SummaryRows(selected)
	if err != nil {
		return nil, err
	}
	out := bytes.Clone(existing)
	for _, id := range newOrder {
		if old, ok := rows[id]; ok {
			if !bytes.Equal(old, newRows[id]) {
				return nil, fmt.Errorf("selected summary %s diverges", id)
			}
		} else {
			rows[id] = newRows[id]
			if len(out) > 0 && out[len(out)-1] != '\n' {
				out = append(out, '\n')
			}
			out = append(out, newRows[id]...)
			out = append(out, '\n')
		}
	}
	return out, nil
}

// Lookup returns every overlapping recorded candidate, never an arbitrary one.
func (m Manifest) Lookup(p, blob string, line int) ([]Association, error) {
	seen := map[string]bool{}
	var result []Association
	for _, f := range m.Sources {
		if f.Path != p {
			continue
		}
		if f.Blob != blob {
			return nil, fmt.Errorf("source blob mismatch for %s", p)
		}
		for _, r := range f.Ranges {
			if line >= r.Start && line <= r.End {
				for _, a := range r.Candidates {
					if !seen[pairKey(a)] {
						seen[pairKey(a)] = true
						result = append(result, a)
					}
				}
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return pairKey(result[i]) < pairKey(result[j]) })
	return result, nil
}
