package layout

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
)

// RecordMode selects publication eligibility for new trace records, not local
// retention or historical reads. The zero value is invalid; configuration
// defaults belong to the canonical config reader in resolve.
type RecordMode uint8

const (
	RecordBlame RecordMode = iota + 1
	RecordFull
)

// ValidationError reports an invalid policy field or path component.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

// Policy is an immutable, comparable repository-layout snapshot. Store and
// checkout roots have distinct roles: all repository-local state is owned by
// the canonical store, so removing a linked checkout cannot remove its traces.
// Policy does no discovery, configuration reads, or filesystem inspection.
// Its zero value has empty paths and cannot be used by the local-write helpers.
type Policy struct {
	storeRoot    string
	checkoutRoot string
	mode         RecordMode
}

// NewRepository constructs a repository policy from already located absolute
// roots and a validated mode. Roots need not exist yet; physical locality is
// checked separately at write time. This never selects global storage.
func NewRepository(storeRoot, checkoutRoot string, mode RecordMode) (Policy, error) {
	for _, root := range []struct{ name, value string }{
		{"store root", storeRoot}, {"checkout root", checkoutRoot},
	} {
		if root.value == "" || !filepath.IsAbs(root.value) || strings.ContainsRune(root.value, 0) {
			return Policy{}, &ValidationError{root.name, "must be a nonempty absolute path without NUL bytes"}
		}
	}
	if mode != RecordBlame && mode != RecordFull {
		return Policy{}, &ValidationError{"record mode", fmt.Sprintf("unknown value %d", mode)}
	}
	return Policy{filepath.Clean(storeRoot), filepath.Clean(checkoutRoot), mode}, nil
}

// RecordMode returns the snapshot's mode. Zero means an uninitialized policy.
func (p Policy) RecordMode() RecordMode { return p.mode }

// RecordRoot holds shared source and blame records. Trace archives stay local in
// both modes; eligible publication copies target CheckoutRecordRoot separately.
func (p Policy) RecordRoot() string {
	if p.storeRoot == "" {
		return ""
	}
	return filepath.Join(p.storeRoot, ".punt-labs", "ethos")
}

// LocalRoot owns all repository machine-local state in every mode: live files,
// retained trace archives, overlays, locks, staging, and agent scratch.
func (p Policy) LocalRoot() string {
	if p.storeRoot == "" {
		return ""
	}
	return filepath.Join(p.storeRoot, ".punt-labs", "local", "ethos")
}

// TraceArchiveRoot holds retained trace archives in both modes. Compatibility
// reads and publication into CheckoutRecordRoot have separate scopes.
func (p Policy) TraceArchiveRoot() string {
	if p.storeRoot == "" {
		return ""
	}
	return filepath.Join(p.LocalRoot(), "trace")
}

// CheckoutRecordRoot is a publication destination, not a second store or
// permission to copy/stage the entire shared record tree.
func (p Policy) CheckoutRecordRoot() string {
	if p.checkoutRoot == "" {
		return ""
	}
	return filepath.Join(p.checkoutRoot, ".punt-labs", "ethos")
}

// TempDir holds unique staging files, including in full mode. A cross-device
// rename/link must fail without falling back to staging in the record tree.
func (p Policy) TempDir() string {
	if p.storeRoot == "" {
		return ""
	}
	return filepath.Join(p.LocalRoot(), "tmp")
}

// LockPath maps an opaque, nonempty resource key to a domain-qualified local
// lock. Domain names are lower-case ASCII slugs. The digest avoids traversal,
// and platform filename restrictions without combining distinct lock domains.
// Callers must use the same canonical key and domain for the same resource.
// Neither mode nor invoking checkout participates in lock identity. This only
// names new locks: migration of active old locks requires caller coordination.
func (p Policy) LockPath(domain, key string) (string, error) {
	if err := p.validate(); err != nil {
		return "", err
	}
	if !slug(domain) || domain[0] < 'a' || domain[0] > 'z' {
		return "", &ValidationError{"lock domain", "must be a lower-case ASCII slug starting with a letter"}
	}
	if key == "" {
		return "", &ValidationError{"lock key", "must not be empty"}
	}
	return filepath.Join(p.LocalRoot(), "locks", filepath.Base(domain), fmt.Sprintf("%x.lock", sha256.Sum256([]byte(key)))), nil
}

// LocalExtensionPath maps a repository identity's local namespace overlay to
// the shared-local tree. It does not select an identity resolution layer or
// change global/bundle storage. The base extension stays in its source layer.
func (p Policy) LocalExtensionPath(handle, namespace string) (string, error) {
	if err := p.validate(); err != nil {
		return "", err
	}
	if !slug(handle) || strings.HasSuffix(handle, "-") {
		return "", &ValidationError{"identity handle", "must be a lower-case ASCII slug"}
	}
	if !slug(namespace) || namespace[0] < 'a' || namespace[0] > 'z' {
		return "", &ValidationError{"extension namespace", "must be a lower-case ASCII slug starting with a letter"}
	}
	return filepath.Join(p.LocalRoot(), "identities", filepath.Base(handle)+".ext", filepath.Base(namespace)+".local.yaml"), nil
}

func (p Policy) validate() error {
	_, err := NewRepository(p.storeRoot, p.checkoutRoot, p.mode)
	return err
}

func slug(s string) bool {
	if s == "" || s[0] == '-' {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
