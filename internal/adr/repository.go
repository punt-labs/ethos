package adr

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/redact"
)

// NewRepositoryStore constructs a repository writer with a read-only legacy
// global store. p is the operation's immutable layout snapshot. An empty legacy
// directory disables fallback. No repository discovery or config reread occurs.
// NewStore remains the explicit global/standalone constructor.
func NewRepositoryStore(p layout.Policy, legacyDir string) (*Store, error) {
	if _, err := p.LockPath("adr", "store"); err != nil {
		return nil, fmt.Errorf("ADR repository policy: %w", err)
	}
	s := &Store{dir: filepath.Join(p.RecordRoot(), "adrs"), policy: p, repository: true}
	if legacyDir != "" {
		if !filepath.IsAbs(legacyDir) || filepath.Clean(legacyDir) == s.dir {
			return nil, fmt.Errorf("legacy ADR directory must be absolute and distinct from the repository store")
		}
		s.legacy = NewStore(filepath.Clean(legacyDir))
	}
	return s, nil
}

func clone(a *ADR) *ADR {
	out := *a
	if a.Alternatives != nil {
		out.Alternatives = append([]string{}, a.Alternatives...)
	}
	return &out
}

// ValidationError identifies a malformed repository-bound semantic field without
// including its value: that value may be a credential or personal address.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

// Mission references use the same grammar as internal/mission (which imports
// adr, so cannot be imported here). Bead references are at most 128 ASCII bytes:
// a lowercase project prefix and alphanumeric ID separated by hyphens, followed
// by optional numeric child components, e.g. ethos-07m.5 or punt-kit-abc.1.2.
var (
	missionIDPattern = regexp.MustCompile(`^m-\d{4}-\d{2}-\d{2}-\d{3}$`)
	beadIDPattern    = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)+(?:\.[0-9]+)*$`)
)

// validateMetadata applies only at repository sinks. Global/standalone reads and
// writes retain compatibility with historical authors and references, but those
// values cannot be copied into repository records without this validation.
func validateMetadata(a *ADR) error {
	if a.Author != "" && !redact.IsHandle(a.Author) {
		return &ValidationError{Field: "author", Message: "must be an identity handle, not a credential"}
	}
	if a.MissionID != "" && !missionIDPattern.MatchString(a.MissionID) {
		return &ValidationError{Field: "mission_id", Message: "must match m-YYYY-MM-DD-NNN"}
	}
	if a.BeadID != "" && (len(a.BeadID) > 128 || !beadIDPattern.MatchString(a.BeadID) || redact.SecretsString(a.BeadID) != a.BeadID) {
		return &ValidationError{Field: "bead_id", Message: "must be a lowercase project-ID with optional numeric child components, at most 128 bytes, not a credential"}
	}
	return nil
}

// ValidateRepositoryMetadata checks semantic fields preserved by redaction before
// an ADR is copied into repository records, including publication snapshots.
// Global/standalone compatibility remains governed by ADR.Validate instead.
// Errors identify the field without including its potentially sensitive value.
func ValidateRepositoryMetadata(a *ADR) error {
	if a == nil {
		return &ValidationError{Field: "adr", Message: "is required"}
	}
	return validateMetadata(a)
}

// prepare copies the candidate before redaction, then validates the stored
// form. The shared writer redactor includes all three DES-080 passes and leak
// checks; semantic fields are tagged keep, never changed by these passes.
func (s *Store) prepare(a *ADR) (*ADR, announcement, error) {
	out := clone(a)
	var say announcement
	if s.repository {
		if err := validateMetadata(out); err != nil {
			return nil, nil, err
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, fmt.Errorf("resolving home for ADR redaction: %w", err)
		}
		if !filepath.IsAbs(home) || filepath.Clean(home) == string(filepath.Separator) {
			return nil, nil, fmt.Errorf("home directory is unusable for ADR redaction")
		}
		home = filepath.Clean(home)
		// Both roots can occur in free text from a linked checkout. Redact the
		// longer one first so a nested checkout does not expose its suffix.
		store := filepath.Dir(filepath.Dir(s.policy.RecordRoot()))
		checkout := filepath.Dir(filepath.Dir(s.policy.CheckoutRecordRoot()))
		roots := []string{checkout, store}
		if len(store) > len(checkout) {
			roots[0], roots[1] = store, checkout
		}
		for _, field := range redact.FieldStrings(*out) {
			if !utf8.ValidString(field.Value) {
				return nil, nil, fmt.Errorf("redacting ADR: field %s is not valid UTF-8", field.Name)
			}
		}
		redacted := redact.Record(*out, func(value string) string {
			// Prefix substitution is regex-free. All content recognition and
			// leak checking stay in the shared redactor, not an ADR fork.
			for i := 0; i < 8; i++ {
				next := value
				for _, root := range roots {
					next = replacePrefix(next, root, "<repo>")
				}
				next = replacePrefix(next, home, "~")
				dashHome := strings.ReplaceAll(home, string(filepath.Separator), "-")
				next = strings.ReplaceAll(next, dashHome+"-", "-~-")
				next = replacePrefix(next, dashHome, "-~")
				next = redact.Prose(next)
				if next == value {
					return value
				}
				value = next
			}
			return "[redacted-path]"
		})
		for _, field := range redact.FieldStrings(redacted) {
			if rule := redact.LeakRule(field.Value, home); rule != "" {
				return nil, nil, fmt.Errorf("redacting ADR: field %s %s; remove or rephrase it", field.Name, rule)
			}
		}
		// Semantic values receive no rewrite, but a malformed identifier must
		// not smuggle a home path into an otherwise redacted record either.
		semantic := map[string]string{
			"id": redacted.ID, "status": redacted.Status, "created_at": redacted.CreatedAt,
			"updated_at": redacted.UpdatedAt, "author": redacted.Author,
			"mission_id": redacted.MissionID, "bead_id": redacted.BeadID,
		}
		for _, field := range redact.FieldStrings(semantic) {
			if !utf8.ValidString(field.Value) {
				return nil, nil, fmt.Errorf("redacting ADR: semantic field is not valid UTF-8")
			}
			if rule := redact.LeakRule(field.Value, home); rule != "" {
				return nil, nil, fmt.Errorf("redacting ADR: semantic field %s; remove or rephrase it", rule)
			}
		}
		if !reflect.DeepEqual(*out, redacted) {
			say = func() {
				fmt.Fprintln(os.Stderr, "ethos: redact: "+rewriteNotice)
			}
		}
		out = &redacted
	}
	if err := out.Validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid ADR: %w", err)
	}
	return out, say, nil
}

type announcement func()

func (a announcement) Say() {
	if a != nil {
		a()
	}
}

// replacePrefix preserves sibling names such as /work/repo2 when replacing
// /work/repo. Free text can embed a path anywhere; remaining obscured spellings
// are rejected by the shared leak checker rather than persisted partially.
func replacePrefix(value, prefix, token string) string {
	var out strings.Builder
	for {
		i := strings.Index(value, prefix)
		if i < 0 {
			out.WriteString(value)
			return out.String()
		}
		end := i + len(prefix)
		if end < len(value) {
			c := value[end]
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c >= utf8.RuneSelf {
				out.WriteString(value[:end])
				value = value[end:]
				continue
			}
		}
		out.WriteString(value[:i])
		out.WriteString(token)
		value = value[end:]
	}
}

// recordDir rejects redirected repository storage even on reads: a symlink is
// not absence and must never cause a fallback to the global ADR. Global store
// placement and its existing symlink-parent behavior remain unchanged.
func (s *Store) recordDir(create bool) error {
	if !s.repository {
		if create {
			if err := os.MkdirAll(s.dir, 0o700); err != nil {
				return fmt.Errorf("creating ADR directory: %w", err)
			}
		}
		return nil
	}
	var ancestors []string
	for d := s.dir; ; d = filepath.Dir(d) {
		ancestors = append(ancestors, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	for i := len(ancestors) - 1; i >= 0; i-- {
		d := ancestors[i]
		info, err := os.Lstat(d)
		if errors.Is(err, os.ErrNotExist) && create {
			if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("creating ADR directory: %w", err)
			}
			info, err = os.Lstat(d)
		}
		if errors.Is(err, os.ErrNotExist) && !create {
			return nil
		}
		if err != nil {
			return fmt.Errorf("examining ADR directory: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("ADR directory ancestor %s is a symlink or not a directory", d)
		}
	}
	return nil
}
