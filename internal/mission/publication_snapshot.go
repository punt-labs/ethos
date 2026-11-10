package mission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/adr"
	"github.com/punt-labs/ethos/v4/internal/redact"
)

// PublicationRecord is immutable caller-owned data captured from the authority.
// Path is slash-separated and relative to the checkout, never an absolute path.
type PublicationRecord struct {
	Path string
	Data []byte
}

// PublicationSnapshot records per-mission cuts, not a repository-wide instant.
// Summary contains selected rows only; the publisher must merge unrelated rows.
type PublicationSnapshot struct {
	Missions []string
	Records  []PublicationRecord
	Summary  []byte
}

// WithSummaryLock serializes a checkout summary replacement with mission close
// and scrub writers. Callers must hold it from the read used for the merge
// through installation, and must not acquire mission or delegation locks inside
// fn: those precede the summary lock in the writer lock order.
func WithSummaryLock(repoRoot string, fn func() error) error {
	return withTraceLock(RepoStatePath(repoRoot), fn)
}

// SnapshotSelection takes the existing writer locks in their established order:
// Store mutation -> repo mission -> individual delegations -> summary. All
// results and inline contract decisions travel; forensic trace never does.
// Global-only and legacy flat missions require migration before publication.
func (s *Store) SnapshotSelection(ctx context.Context, ids, explicitADRs []string) (PublicationSnapshot, error) {
	var snapshot PublicationSnapshot
	if s.repoRoot == "" || !s.twoTreeStorage || len(ids) == 0 {
		return snapshot, fmt.Errorf("publication requires repository missions and at least one mission ID")
	}
	selected := map[string]bool{}
	for _, id := range ids {
		if !missionIDPattern.MatchString(id) || selected[id] {
			return snapshot, fmt.Errorf("invalid or duplicate publication mission %q", id)
		}
		selected[id] = true
		snapshot.Missions = append(snapshot.Missions, id)
	}
	sort.Strings(snapshot.Missions)
	for _, id := range snapshot.Missions {
		if err := ctx.Err(); err != nil {
			return PublicationSnapshot{}, err
		}
		err := s.withLock(id, func() error {
			release, err := AcquireMissionLockExclusive(s.repoRoot, id)
			if err != nil {
				return err
			}
			defer release()
			return s.snapshotMission(ctx, id, &snapshot)
		})
		if err != nil {
			return PublicationSnapshot{}, fmt.Errorf("snapshot %s: %w", id, err)
		}
	}
	// ADR writers use atomic replacement, not mission locks. Each ADR is a
	// separate immutable byte cut. Links are explicit mission_id equality only.
	wanted := map[string]bool{}
	for _, id := range explicitADRs {
		if filepath.Base(id) != id || !strings.HasPrefix(id, "DES-") {
			return PublicationSnapshot{}, fmt.Errorf("invalid ADR ID %q", id)
		}
		wanted[id] = true
	}
	dir := RepoStatePath(s.repoRoot, "adrs")
	if err := rejectSymlinkedPath(s.repoRoot, ".punt-labs/ethos/adrs"); err != nil {
		return PublicationSnapshot{}, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return PublicationSnapshot{}, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			return PublicationSnapshot{}, fmt.Errorf("unknown ADR record shape %s", entry.Name())
		}
		data, err := publicationRead(filepath.Join(dir, entry.Name()))
		if err != nil {
			return PublicationSnapshot{}, err
		}
		var a adr.ADR
		if err := publicationDecode(data, &a); err != nil {
			return PublicationSnapshot{}, err
		}
		if err := a.Validate(); err != nil {
			return PublicationSnapshot{}, err
		}
		if entry.Name() != a.ID+".yaml" {
			return PublicationSnapshot{}, fmt.Errorf("ADR identity mismatch: %s", entry.Name())
		}
		if wanted[a.ID] || selected[a.MissionID] {
			snapshot.Records = append(snapshot.Records, PublicationRecord{Path: ".punt-labs/ethos/adrs/" + entry.Name(), Data: data})
			delete(wanted, a.ID)
		}
	}
	if len(wanted) != 0 {
		return PublicationSnapshot{}, fmt.Errorf("explicit ADR record missing: %v", wanted)
	}
	sort.Slice(snapshot.Records, func(i, j int) bool { return snapshot.Records[i].Path < snapshot.Records[j].Path })
	return RedactPublicationSnapshot(s.repoRoot, snapshot)
}

func (s *Store) snapshotMission(ctx context.Context, id string, snapshot *PublicationSnapshot) error {
	dir := RepoStatePath(s.repoRoot, "missions", filepath.Base(id))
	if err := rejectSymlinkedPath(s.repoRoot, ".punt-labs/ethos/missions/"+id); err != nil {
		return err
	}
	data, err := publicationRead(filepath.Join(dir, "contract.yaml"))
	if err != nil {
		return err
	}
	// Unknown contract fields must fail closed.
	c, err := DecodeContractStrict(data, id)
	if err != nil {
		return err
	}
	if c.MissionID != id {
		return fmt.Errorf("contract identity mismatch")
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if c.RoundDecisions == nil {
		return fmt.Errorf("shared round decisions unavailable: the mission's legacy history is incomplete and it is fenced from publication")
	}
	add := func(rel string, data []byte) {
		snapshot.Records = append(snapshot.Records, PublicationRecord{Path: ".punt-labs/ethos/missions/" + id + "/" + rel, Data: data})
	}
	add("contract.yaml", data)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		switch name {
		case "contract.yaml", "results.yaml", "corrections.yaml", "delegations":
		case "artifacts", "reflections.yaml", "log.jsonl", ".lock":
			continue // supported legacy trace/lock, never selected
		default:
			if strings.HasPrefix(name, "log-") && (strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".quarantine")) {
				continue
			}
			return fmt.Errorf("unknown mission record shape %s; upgrade or repair before publication", name)
		}
	}
	resultBytes, err := publicationRead(filepath.Join(dir, "results.yaml"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var rf resultsFile
	if err == nil {
		if err := publicationDecode(resultBytes, &rf); err != nil {
			return err
		}
	}
	seen := map[int]bool{}
	for _, result := range rf.Results {
		if result.Mission != id || seen[result.Round] || result.Round > c.CurrentRound {
			return fmt.Errorf("inconsistent result round %d", result.Round)
		}
		seen[result.Round] = true
	}
	if c.Status != StatusOpen && c.Status != StatusAbandoned && !seen[c.CurrentRound] {
		return fmt.Errorf("terminal mission missing current-round result")
	}
	if resultBytes != nil {
		add("results.yaml", resultBytes)
	}
	// Empty correction collections are explicit completeness evidence. A legacy
	// mission missing the shared projection must be upgraded, not guessed empty.
	corrections, err := publicationRead(filepath.Join(dir, "corrections.yaml"))
	if err != nil {
		return fmt.Errorf("shared corrections projection unavailable: the mission's legacy history is incomplete and it is fenced from publication: %w", err)
	}
	if err := validatePublicationCorrections(c, corrections); err != nil {
		return err
	}
	add("corrections.yaml", corrections)
	delegations, err := os.ReadDir(filepath.Join(dir, "delegations"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// Hold every contributing delegation lock through the cut. Releasing each
	// after reading it could combine an old first record with a new second
	// record when close-only writers do not hold the repo mission lock.
	var releases []func()
	defer func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}()
	for _, entry := range delegations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() || !validPublicationComponent(entry.Name()) {
			return fmt.Errorf("unknown delegation shape %s", entry.Name())
		}
		release, err := s.AcquireDelegationLock(entry.Name())
		if err != nil {
			return err
		}
		releases = append(releases, release)
	}
	for _, entry := range delegations {
		err = func() error {
			rel := "delegations/" + entry.Name() + "/record.yaml"
			data, err := publicationRead(filepath.Join(dir, filepath.FromSlash(rel)))
			if err != nil {
				return err
			}
			var d Delegation
			if err := publicationDecode(data, &d); err != nil {
				return err
			}
			if d.ID != entry.Name() || d.Mission != id || d.Tier != TierB || d.AgentType == "" {
				return fmt.Errorf("inconsistent delegation %s", entry.Name())
			}
			children, err := os.ReadDir(filepath.Dir(filepath.Join(dir, filepath.FromSlash(rel))))
			if err != nil {
				return err
			}
			for _, child := range children {
				if child.Name() != "record.yaml" && child.Name() != "prompt.md" && child.Name() != "artifacts" {
					return fmt.Errorf("unknown delegation record %s", child.Name())
				}
			}
			add(rel, data)
			return nil
		}()
		if err != nil {
			return err
		}
	}
	return withTraceLock(RepoStatePath(s.repoRoot), func() error {
		data, err := publicationRead(RepoStatePath(s.repoRoot, "missions.jsonl"))
		if errors.Is(err, fs.ErrNotExist) && c.Status == StatusOpen {
			return nil
		}
		if err != nil {
			return err
		}
		count := 0
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			row, err := publicationSummary(line)
			if err != nil {
				return fmt.Errorf("invalid mission summary: %w", err)
			}
			if row.ID == id {
				if row.Status != c.Status || row.ClosedAt != c.ClosedAt || row.RoundsUsed != c.CurrentRound {
					return fmt.Errorf("summary conflicts with contract %s", id)
				}
				count++
				snapshot.Summary = append(snapshot.Summary, append(bytes.Clone(line), '\n')...)
			}
		}
		if count > 1 || count == 0 && c.Status != StatusOpen {
			return fmt.Errorf("missing or duplicate terminal summary for %s", id)
		}
		return nil
	})
}

func publicationRead(path string) ([]byte, error) {
	if err := rejectSymlink(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("publication record is not regular: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// RedactPublicationSnapshot strictly validates the original inventory before
// canonicalizing it through the typed redactor. Checking the original first
// prevents canonicalization from silently dropping unknown fields. Checking
// only decoded values without using the canonical bytes would leak comments
// and other discarded syntax. The returned snapshot owns its record bytes.
func RedactPublicationSnapshot(repoRoot string, snapshot PublicationSnapshot) (PublicationSnapshot, error) {
	if err := ValidatePublicationSnapshot(snapshot); err != nil {
		return PublicationSnapshot{}, err
	}
	out := PublicationSnapshot{Missions: append([]string(nil), snapshot.Missions...)}
	for _, record := range snapshot.Records {
		var data []byte
		var err error
		switch path.Base(record.Path) {
		case "contract.yaml":
			data, err = publicationRedact[Contract](repoRoot, record)
		case "results.yaml":
			data, err = publicationRedact[resultsFile](repoRoot, record)
		case "corrections.yaml":
			data, err = publicationRedact[correctionsAuthority](repoRoot, record)
		case "record.yaml":
			data, err = publicationRedact[Delegation](repoRoot, record)
		default: // ValidatePublicationSnapshot permits only ADRs here.
			data, err = publicationRedact[adr.ADR](repoRoot, record)
		}
		if err != nil {
			return PublicationSnapshot{}, fmt.Errorf("redacting publication %s: %w", record.Path, err)
		}
		out.Records = append(out.Records, PublicationRecord{Path: record.Path, Data: data})
	}
	for _, line := range bytes.Split(snapshot.Summary, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var row TraceSummary
		if err := json.Unmarshal(line, &row); err != nil {
			return PublicationSnapshot{}, err
		}
		row, err := RedactRecord(repoRoot, row)
		if err != nil {
			return PublicationSnapshot{}, err
		}
		data, err := json.Marshal(row)
		if err != nil {
			return PublicationSnapshot{}, err
		}
		out.Summary = append(out.Summary, append(data, '\n')...)
	}
	if _, err := guardWrite("missions.jsonl", out.Summary); err != nil {
		return PublicationSnapshot{}, err
	}
	if err := ValidatePublicationSnapshot(out); err != nil {
		return PublicationSnapshot{}, err
	}
	return out, nil
}

func publicationRedact[T any](repoRoot string, record PublicationRecord) ([]byte, error) {
	var value T
	if err := publicationDecode(record.Data, &value); err != nil {
		return nil, err
	}
	data, err := RedactedYAML(repoRoot, value)
	if err != nil {
		return nil, err
	}
	return guardWrite(record.Path, data)
}

// GuardPublicationSummary refuses unsafe merged summary bytes without changing
// them. Unrelated rows may have fields unknown to this version and must retain
// their exact encoding. Known fields retain TraceSummary's path-only rules;
// unknown fields get the full content checks. No canonical bytes are installed.
func GuardPublicationSummary(repoRoot string, data []byte) error {
	if rule := InvalidText(data); rule != "" {
		return fmt.Errorf("refusing to publish missions.jsonl: content %s", rule)
	}
	c := newLeakChecker()
	typ := reflect.TypeOf(TraceSummary{})
	known := structFields(typ, "json")
	for i, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if _, _, duplicate := redact.DuplicateJSONKey(line); duplicate {
			return fmt.Errorf("refusing to publish missions.jsonl: line %d holds a duplicate key", i+1)
		}
		value, err := decodeJSONGeneric(line)
		if err != nil {
			return fmt.Errorf("refusing to publish missions.jsonl: line %d cannot be checked", i+1)
		}
		// A generic object retains unknown fields, keys and nested values. The
		// shared guard uses the known schema only to classify path-only fields.
		fields, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("refusing to publish missions.jsonl: line %d is not an object", i+1)
		}
		if _, rule := guardValue(c, fields, typ, false); rule != "" {
			return fmt.Errorf("refusing to publish missions.jsonl: line %d %s", i+1, rule)
		}
		var row TraceSummary
		if err := json.Unmarshal(line, &row); err != nil {
			return fmt.Errorf("refusing to publish missions.jsonl: line %d has malformed summary fields", i+1)
		}
		redacted, err := RedactRecord(repoRoot, row)
		if err != nil {
			return fmt.Errorf("refusing to publish missions.jsonl: line %d cannot be redacted", i+1)
		}
		if !reflect.DeepEqual(row, redacted) {
			return fmt.Errorf("refusing to publish missions.jsonl: line %d requires redaction; repair it explicitly", i+1)
		}
		// Path-only values have been checked above with their actual tags. Do
		// not run the generic prose/credential pass on enforcement fields.
		for key := range fields {
			if sf, ok := known[key]; ok && sf.Tag.Get("redact") == "path" {
				delete(fields, key)
			}
		}
		checked, err := RedactRecord(repoRoot, fields)
		if err != nil {
			return fmt.Errorf("refusing to publish missions.jsonl: line %d cannot be redacted", i+1)
		}
		if !reflect.DeepEqual(fields, checked) {
			return fmt.Errorf("refusing to publish missions.jsonl: line %d requires redaction; repair it explicitly", i+1)
		}
	}
	return nil
}

func publicationDecode[T any](data []byte, out *T) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if err := rejectDuplicateYAMLKeys(&doc); err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("publication record: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("publication record must contain exactly one YAML document")
	}
	return nil
}

// ValidatePublicationSnapshot checks the portable inventory, independently of
// the mutable store. Historical/index verifiers use this same contract. Missing
// legacy authorities are not guessed empty. It cannot detect a record deleted
// before the snapshot cut; the manifest subsequently binds the captured bytes.
func ValidatePublicationSnapshot(snapshot PublicationSnapshot) error {
	if len(snapshot.Missions) == 0 {
		return fmt.Errorf("publication requires selected missions")
	}
	selected := map[string]bool{}
	for _, id := range snapshot.Missions {
		if !missionIDPattern.MatchString(id) || selected[id] {
			return fmt.Errorf("invalid/duplicate publication mission %q", id)
		}
		selected[id] = true
	}
	records := map[string][]byte{}
	for _, record := range snapshot.Records {
		if _, ok := records[record.Path]; ok {
			return fmt.Errorf("duplicate publication record %s", record.Path)
		}
		parts := strings.Split(record.Path, "/")
		if path.Clean(record.Path) != record.Path || strings.ContainsAny(record.Path, "\\\x00\r\n:") || len(parts) < 4 || parts[0] != ".punt-labs" || parts[1] != "ethos" {
			return fmt.Errorf("invalid publication path %q", record.Path)
		}
		switch parts[2] {
		case "adrs":
			var a adr.ADR
			if err := publicationDecode(record.Data, &a); err != nil {
				return err
			}
			if len(parts) != 4 || parts[3] != a.ID+".yaml" {
				return fmt.Errorf("ADR identity/path mismatch: %s", record.Path)
			}
			if err := adr.ValidateRepositoryMetadata(&a); err != nil {
				return err
			}
			if err := a.Validate(); err != nil {
				return err
			}
		case "missions":
			if len(parts) < 5 || !selected[parts[3]] {
				return fmt.Errorf("record outside selected missions: %s", record.Path)
			}
			if len(parts) == 5 && (parts[4] == "contract.yaml" || parts[4] == "results.yaml" || parts[4] == "corrections.yaml") {
				break
			}
			if len(parts) != 7 || parts[4] != "delegations" || parts[6] != "record.yaml" || !validPublicationComponent(parts[5]) {
				return fmt.Errorf("unknown publication record shape: %s", record.Path)
			}
			var d Delegation
			if err := publicationDecode(record.Data, &d); err != nil {
				return err
			}
			if d.ID != parts[5] || d.Mission != parts[3] || d.Tier != TierB || strings.TrimSpace(d.AgentType) == "" || containsControlChar(d.AgentType) {
				return fmt.Errorf("delegation identity/agent mismatch: %s", record.Path)
			}
		default:
			return fmt.Errorf("unknown publication record shape: %s", record.Path)
		}
		records[record.Path] = record.Data
	}
	contracts := map[string]*Contract{}
	for _, id := range snapshot.Missions {
		prefix := ".punt-labs/ethos/missions/" + id + "/"
		c, err := DecodeContractStrict(records[prefix+"contract.yaml"], id)
		if err != nil {
			return err
		}
		if c.MissionID != id || c.RoundDecisions == nil {
			return fmt.Errorf("incomplete contract/shared decisions for %s", id)
		}
		if err := c.Validate(); err != nil {
			return err
		}
		contracts[id] = c
		seen := map[int]bool{}
		if data, ok := records[prefix+"results.yaml"]; ok {
			var rf resultsFile
			if err := publicationDecode(data, &rf); err != nil {
				return err
			}
			if rf.Results == nil {
				return fmt.Errorf("results collection missing for %s", id)
			}
			for _, result := range rf.Results {
				if err := result.Validate(); err != nil {
					return err
				}
				if result.Mission != id || seen[result.Round] || result.Round > c.CurrentRound {
					return fmt.Errorf("inconsistent result round for %s", id)
				}
				seen[result.Round] = true
			}
		}
		if c.Status != StatusOpen && c.Status != StatusAbandoned && !seen[c.CurrentRound] {
			return fmt.Errorf("terminal mission missing current-round result: %s", id)
		}
		if err := validatePublicationCorrections(c, records[prefix+"corrections.yaml"]); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, line := range bytes.Split(snapshot.Summary, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		row, err := publicationSummary(line)
		if err != nil {
			return err
		}
		c := contracts[row.ID]
		if c == nil || seen[row.ID] || row.Status != c.Status || row.ClosedAt != c.ClosedAt || row.RoundsUsed != c.CurrentRound {
			return fmt.Errorf("summary identity/state mismatch: %s", row.ID)
		}
		seen[row.ID] = true
	}
	for id, c := range contracts {
		if c.Status != StatusOpen && !seen[id] {
			return fmt.Errorf("terminal mission missing summary: %s", id)
		}
	}
	return nil
}

func publicationSummary(data []byte) (TraceSummary, error) {
	var row TraceSummary
	if _, key, duplicate := redact.DuplicateJSONKey(data); duplicate {
		return row, fmt.Errorf("duplicate summary key %q", key)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&row); err != nil {
		return row, err
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return row, fmt.Errorf("summary row must contain one JSON value")
	}
	return row, nil
}

func validPublicationComponent(s string) bool {
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

func validatePublicationCorrections(c *Contract, data []byte) error {
	var a correctionsAuthority
	if err := publicationDecode(data, &a); err != nil {
		return err
	}
	if a.Version != 1 || a.Mission != c.MissionID || a.Entries == nil {
		return fmt.Errorf("incomplete/unsupported corrections projection: %s", c.MissionID)
	}
	seen := map[string]bool{}
	for _, entry := range a.Entries {
		if !operationIDPattern.MatchString(entry.ID) || seen[entry.ID] {
			return fmt.Errorf("invalid/duplicate correction ID")
		}
		seen[entry.ID] = true
		if _, err := time.Parse(time.RFC3339, entry.CreatedAt); err != nil {
			return err
		}
		if err := entry.Correction.Validate(); err != nil {
			return err
		}
		if entry.Correction.Mission != c.MissionID || entry.Correction.Round > c.CurrentRound {
			return fmt.Errorf("correction identity/round mismatch: %s", c.MissionID)
		}
	}
	return nil
}
