package attribute

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Store provides CRUD for one category of attribute .md files.
type Store struct {
	root string // ethos root, e.g. ~/.punt-labs/ethos
	kind Kind
}

// NewStore creates a Store for the given kind under root.
func NewStore(root string, kind Kind) *Store {
	return &Store{root: root, kind: kind}
}

// DefaultStore returns a Store using the default global directory.
func DefaultStore(kind Kind) (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}
	return &Store{root: filepath.Join(home, ".punt-labs", "ethos"), kind: kind}, nil
}

// Dir returns the directory for this attribute kind.
func (s *Store) Dir() string {
	return filepath.Join(s.root, s.kind.DirName)
}

// Path returns the filesystem path for the given slug.
// Returns an error if the slug would escape the attribute directory.
func (s *Store) Path(slug string) (string, error) {
	if err := ValidateSlug(slug); err != nil {
		return "", err
	}
	dir := s.Dir()
	candidate := filepath.Join(dir, slug+".md")
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolving path: %w", err)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolving directory: %w", err)
	}
	rel, err := filepath.Rel(absDir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s %q escapes directory", s.kind.DisplayName, slug)
	}
	return candidate, nil
}

// Exists checks whether an attribute file exists for the given slug.
func (s *Store) Exists(slug string) bool {
	p, err := s.Path(slug)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// Save writes a new attribute .md file. Returns an error if it already exists.
func (s *Store) Save(a *Attribute) error {
	if err := ValidateSlug(a.Slug); err != nil {
		return err
	}
	if a.Content == "" {
		return &ValidationError{Field: "content", Message: "required"}
	}

	p, err := s.Path(a.Slug)
	if err != nil {
		return err
	}

	dir := s.Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s directory: %w", s.kind.DisplayName, err)
	}

	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s %q already exists", s.kind.DisplayName, a.Slug)
		}
		return fmt.Errorf("creating %s file: %w", s.kind.DisplayName, err)
	}

	content := a.Content
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if _, err = f.WriteString(content); err != nil {
		f.Close()
		os.Remove(p)
		return fmt.Errorf("writing %s file: %w", s.kind.DisplayName, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(p)
		return fmt.Errorf("writing %s file: %w", s.kind.DisplayName, err)
	}
	return nil
}

// Load reads an attribute .md file by slug.
func (s *Store) Load(slug string) (*Attribute, error) {
	p, err := s.Path(slug)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s %q not found", s.kind.DisplayName, slug)
		}
		return nil, fmt.Errorf("reading %s: %w", s.kind.DisplayName, err)
	}
	return &Attribute{Slug: slug, Content: string(data)}, nil
}

// List returns all attributes in this store's directory.
// Files that cannot be read are reported as warnings.
func (s *Store) List() (*ListResult, error) {
	dir := s.Dir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return &ListResult{}, nil
		}
		return nil, fmt.Errorf("reading %s directory: %w", s.kind.DisplayName, err)
	}

	result := &ListResult{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		if entry.Name() == "README.md" {
			continue
		}
		slug := strings.TrimSuffix(entry.Name(), ".md")
		a, err := s.Load(slug)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("skipping %s: %v", entry.Name(), err))
			continue
		}
		result.Attributes = append(result.Attributes, a)
	}
	return result, nil
}

// Delete removes an attribute .md file.
func (s *Store) Delete(slug string) error {
	p, err := s.Path(slug)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s %q not found", s.kind.DisplayName, slug)
		}
		return fmt.Errorf("deleting %s: %w", s.kind.DisplayName, err)
	}
	return nil
}
