package vendor

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// readExtKeys returns the key NAMES in one extension base file, sorted.
// Values are decoded because YAML requires it, and are then discarded
// unread — the lint never inspects them (DES-008).
func readExtKeys(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var m map[string]string
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// Verdict is the credential lint's classification of an ext key NAME.
type Verdict int

const (
	// Clean: the name does not suggest a secret.
	Clean Verdict = iota
	// Warn: the name is ambiguous — it may be a secret, may be a public
	// reference. Reported, does not block.
	Warn
	// Block: the name says "secret". Vendor refuses to write it.
	Block
)

func (v Verdict) String() string {
	switch v {
	case Block:
		return "BLOCK"
	case Warn:
		return "WARN"
	default:
		return "CLEAN"
	}
}

// The lint reads key NAMES only, never values. Reading values would make
// ethos interpret extension data, which DES-008 forbids; the name is
// enough to catch the case that matters (a credential about to be
// written into git) without ethos ever learning what a consumer's keys
// mean.
//
// Classification is underscore-token membership, in this order:
// EXCLUDE, then BLOCK, then WARN. Order is the whole design. `gpg_key_id`
// holds the WARN token "key" but is a public reference, so the EXCLUDE
// token "id" must win; testing WARN first would make every published GPG
// binding a false positive and train users to ignore the lint.
//
// The token lists are curated and djb-owned. They are deliberately
// short: a long list of near-misses produces noise, and noise is how a
// fail-closed guard gets disabled.
var (
	// excludeTokens mark a name as a reference TO a secret, or as plainly
	// public configuration. Any one of them clears the key.
	excludeTokens = map[string]bool{
		"id": true, "ids": true, "keyid": true, "fingerprint": true,
		"pubkey": true, "public": true, "url": true, "uri": true,
		"host": true, "hostname": true, "port": true, "server": true,
		"email": true, "username": true, "handle": true, "name": true,
		"path": true, "dir": true, "file": true, "collection": true,
		"context": true, "provider": true, "model": true, "version": true,
		"enabled": true, "mode": true, "format": true,
	}

	// blockTokens name a secret outright. Vendor refuses the write.
	blockTokens = map[string]bool{
		"password": true, "passwd": true, "passphrase": true,
		"token": true, "secret": true, "credential": true,
		"credentials": true, "private": true, "apikey": true,
		"auth": true,
	}

	// blockPairs name a secret only in combination. "key" alone is
	// ambiguous — gpg_key_id is public — but "api_key" is not, so the
	// pair is what blocks rather than the bare token.
	blockPairs = [][2]string{
		{"api", "key"},
		{"access", "key"},
		{"signing", "key"},
		{"session", "key"},
	}

	// warnTokens are ambiguous. Reported so a real secret under an
	// unusual name is still visible, but never blocking — a blocking
	// guard that fires on `voice_key` would be turned off.
	warnTokens = map[string]bool{
		"key": true, "salt": true, "seed": true, "pin": true,
		"dsn": true, "cert": true, "signature": true, "nonce": true,
	}
)

// Classify reports the credential verdict for an ext key name.
func Classify(key string) Verdict {
	tokens := strings.Split(strings.ToLower(key), "_")
	for _, t := range tokens {
		if excludeTokens[t] {
			return Clean
		}
	}
	for _, t := range tokens {
		if blockTokens[t] {
			return Block
		}
	}
	for i := 0; i+1 < len(tokens); i++ {
		for _, p := range blockPairs {
			if tokens[i] == p[0] && tokens[i+1] == p[1] {
				return Block
			}
		}
	}
	for _, t := range tokens {
		if warnTokens[t] {
			return Warn
		}
	}
	return Clean
}

// Finding is one classified ext key, named the way the user must address
// it: by handle, namespace, and key.
type Finding struct {
	Handle    string  `json:"handle" yaml:"handle"`
	Namespace string  `json:"namespace" yaml:"namespace"`
	Key       string  `json:"key" yaml:"key"`
	Verdict   Verdict `json:"-" yaml:"-"`
}

// Ref is the `<namespace>/<key>` form --allow-ext-key takes.
func (f Finding) Ref() string {
	return f.Namespace + "/" + f.Key
}

func (f Finding) String() string {
	return fmt.Sprintf("%s %s", f.Handle, f.Ref())
}

// allowSet is the parsed --allow-ext-key list: per-key overrides, never
// blanket. A single --force would be used once and then always, which is
// how a fail-closed guard becomes decoration.
type allowSet map[string]bool

// parseAllowExtKeys validates and indexes the --allow-ext-key values. A
// malformed entry is an error rather than a silently ignored override —
// a user who typed `quarry.api_token` must not believe they granted an
// exemption they did not.
func parseAllowExtKeys(refs []string) (allowSet, error) {
	set := make(allowSet, len(refs))
	for _, r := range refs {
		ns, key, ok := strings.Cut(strings.TrimSpace(r), "/")
		if !ok || ns == "" || key == "" {
			return nil, fmt.Errorf("--allow-ext-key %q: want <namespace>/<key>", r)
		}
		set[ns+"/"+key] = true
	}
	return set, nil
}

func (a allowSet) allows(f Finding) bool {
	return a[f.Ref()]
}

// blockError renders the refusal: every blocked key at once (so one
// round of --allow-ext-key decisions settles it), sorted for a stable
// message, with the exact flag to grant each exemption.
func blockError(blocked []Finding) error {
	refs := make([]string, 0, len(blocked))
	flags := make([]string, 0, len(blocked))
	for _, f := range sortFindings(blocked) {
		refs = append(refs, f.String())
		flags = append(flags, "--allow-ext-key "+f.Ref())
	}
	return fmt.Errorf(
		"refusing to vendor credential-named extension keys into git-tracked space: %s\n"+
			"move each value to the identity's <namespace>.local.yaml (`ethos ext set <handle> <ns> <key> <value> --local`), "+
			"or allow a specific key with %s",
		strings.Join(refs, ", "), strings.Join(flags, " "))
}

// sortFindings orders findings by handle, namespace, then key.
func sortFindings(f []Finding) []Finding {
	out := append([]Finding(nil), f...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Handle != out[j].Handle {
			return out[i].Handle < out[j].Handle
		}
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Key < out[j].Key
	})
	return out
}
