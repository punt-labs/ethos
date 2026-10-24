// Package redact holds the content passes that keep personal data and
// credentials out of the files ethos writes into git-tracked trees:
// the email sweep, the credential sweep, the home-path sweep, and a
// reflective walker that applies a string function to every field of a
// record.
//
// Ethos writes mission records and audit lines under .punt-labs/ethos/,
// and those trees are pushed to public repos. Every writer that reaches
// such a tree uses this package, so there is one definition of what
// counts as an address or a credential.
//
// The package depends on the standard library only. The audit path
// (internal/hook) and the mission writers (internal/mission) both import
// it; neither imports the other's policy.
package redact

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

// Replacement tokens. EmailToken and the audit package's
// "[redacted]" match the hand-redacted lines from the public-website
// PR #259 cleanup, so one grep finds both the hand-redacted history and
// everything written since.
const (
	EmailToken  = "[redacted-email]"
	SecretToken = "[redacted-secret]"
)

// EmailPattern matches an address with a lettered top-level domain.
// Requiring the TLD keeps some common false positives out: a short Go
// module version (gopkg.in/yaml.v3@v3.0.1), a digest (image@sha256:...),
// and a bare user@host all fail the final \.[A-Za-z]{2,} anchor.
//
// It does not keep all of them out: `git@github.com` is a legal address
// by shape, and a Go toolchain module path
// (golang.org/toolchain@v0.0.1-go1.26.5.linux-amd64) satisfies the
// anchor at `.linux`. Both are pinned in
// TestEmailPattern_AddressShapedNonAddresses.
//
// Deliberately not RFC 5322, and deliberately not narrowed to exempt
// those two. An address-shaped run of characters is what leaks; a parser
// that accepts every legal address and nothing else would be larger,
// slower, and no better at this job. One pattern serves every caller, so
// an exemption that keeps a clone line legible also lets the same run
// through a mail subject.
var EmailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// secretName is the case-insensitive fragment that makes a name
// credential-bearing. Matching on the name, not the value, is the only
// way to catch a password: it has no shape of its own.
const secretName = `(?:token|secret|password|passwd|api[_-]?key|private[_-]?key|access[_-]?key)`

const (
	// nameRE is any identifier-ish run that contains a credential name.
	nameRE = `[A-Za-z0-9_.\-]*` + secretName + `[A-Za-z0-9_.\-]*`

	// valueRE is the value of an assignment, longest-form first: a quoted
	// string (the quotes may themselves be backslash-escaped, as in JSON
	// pasted into a shell argument), a shell substitution, or a bare run.
	valueRE = `\\"(?:[^\\]|\\[^"])*?\\"` + // \"a b\"
		`|\\'(?:[^\\]|\\[^'])*?\\'` + // \'a b\'
		`|"(?:[^"\\]|\\.)*"` + // "a b"
		`|'[^']*'` + // 'a b'
		`|\$\((?:[^()]|\([^()]*\))*\)` + // $(cmd)
		`|\$\{[^}]*\}` + // ${VAR}
		`|["'][^\n]*` + // an opening quote with no close (a truncated preview): to line end
		`|[^\s"']*[^\s"',;.)]` // bare; trailing punctuation is prose, not value

	// flagValueRE is valueRE for a --flag: a bare value that starts with
	// '-' is the next flag, not a value.
	flagValueRE = `\\"(?:[^\\]|\\[^"])*?\\"` +
		`|\\'(?:[^\\]|\\[^'])*?\\'` +
		`|"(?:[^"\\]|\\.)*"` +
		`|'[^']*'` +
		`|\$\((?:[^()]|\([^()]*\))*\)` +
		`|\$\{[^}]*\}` +
		`|["'][^\n]*` +
		`|[^\s\-"'](?:[^\s"']*[^\s"',;.)])?`
)

var (
	// secretKey tests a whole map key.
	secretKey = regexp.MustCompile(`(?i)` + secretName)

	// pemRE matches a private-key block (including PGP's "PRIVATE KEY
	// BLOCK"), or the unterminated head of one: audit previews truncate
	// at 200 bytes, so the END line may be missing.
	pemRE = regexp.MustCompile(
		`(?s)-----BEGIN [A-Z ]*PRIVATE KEY(?: BLOCK)?-----.*?(?:-----END [A-Z ]*PRIVATE KEY(?: BLOCK)?-----|\z)`)

	// tokenShapes are credentials with a fixed, recognizable shape. Group 1
	// is the boundary character, kept: Go's \b treats '_' as a word
	// character, so "x_ghp_..." needs an explicit non-alphanumeric
	// boundary.
	tokenShapes = regexp.MustCompile(`(?m)(^|[^A-Za-z0-9])(?:` +
		`gh[pousr]_[A-Za-z0-9]{20,}` + // GitHub tokens
		`|github_pat_[A-Za-z0-9_]{20,}` + // GitHub fine-grained tokens
		`|sk-(?:ant-)?[A-Za-z0-9_\-]{20,}` + // sk- and sk-ant- API keys
		`|[sr]k_(?:live|test)_[A-Za-z0-9]{16,}` + // Stripe
		`|xox[abposr]-[A-Za-z0-9\-]{10,}` + // Slack
		`|glpat-[A-Za-z0-9_\-]{20,}` + // GitLab
		`|AIza[0-9A-Za-z_\-]{35}` + // Google API keys
		`|npm_[A-Za-z0-9]{30,}` + // npm
		`|hf_[A-Za-z0-9]{30,}` + // Hugging Face
		`|eyJ[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]{5,}` + // JWT
		`|(?:AKIA|ASIA)[0-9A-Z]{16}\b)`) // AWS access key IDs

	// urlUserinfo matches scheme://user:password@; the password runs to the
	// last '@' before the next '/' or space, so a password holding '@', '#'
	// or '!' is taken whole. It runs before the email pass, which would
	// otherwise take only "pass@host".
	urlUserinfo = regexp.MustCompile(`(\b[A-Za-z][A-Za-z0-9+.\-]*://[^\s/@:"']+:)([^\s/"']*)@`)

	// authHeader keeps the header name and scheme and drops the
	// credential, so the log still shows that a request was authorized.
	authHeader = regexp.MustCompile(
		`(?i)(\bauthorization\s*[:=]\s*(?:bearer|basic|token)\s+)[^\s"']+`)

	// bearer needs 16 characters of token so ordinary prose
	// ("the bearer of bad news") passes.
	bearer = regexp.MustCompile(`(?i)(\bbearer\s+)[A-Za-z0-9._~+/=\-]{16,}`)

	// envAssign matches NAME=value and NAME = value; colonAssign matches
	// name: value (YAML, HTTP headers). Blanks only around the separator,
	// so "API_KEY=" at a line end does not reach into the next line.
	envAssign   = regexp.MustCompile(`(?i)(` + nameRE + `[ \t]*=[ \t]*)(` + valueRE + `)`)
	colonAssign = regexp.MustCompile(`(?i)(\b` + nameRE + `[ \t]*:[ \t]*)(` + valueRE + `)`)

	// flagValue matches --name value.
	flagValue = regexp.MustCompile(`(?i)(--[A-Za-z0-9_\-]*` + secretName + `[A-Za-z0-9_\-]*\s+)(` + flagValueRE + `)`)

	// jsonPair matches "name": "value", the form a JSON document or a
	// tool argument takes; jsonPairEsc is the same with every quote
	// backslash-escaped, JSON inside a shell string.
	jsonPair = regexp.MustCompile(
		`(?i)("[A-Za-z0-9_.\-]*` + secretName + `[A-Za-z0-9_.\-]*"\s*:\s*)"(?:[^"\\]|\\.)*"`)
	jsonPairEsc = regexp.MustCompile(
		`(?i)(\\"[A-Za-z0-9_.\-]*` + secretName + `[A-Za-z0-9_.\-]*\\"\s*:\s*)\\"(?:[^\\]|\\[^"])*?\\"`)

	// varRef is a shell variable reference or substitution, which fetches
	// a secret rather than embedding one.
	varRef = regexp.MustCompile(`^\$(?:[A-Za-z_]|\{|\()`)

	// homePath matches /home/<name> and /Users/<name> for any name, not
	// only the current user: a path pasted from another machine names a
	// person as surely as this one does. The leading class stops a match
	// inside another path component (/etc/home/x).
	homePath = regexp.MustCompile(`(^|[^A-Za-z0-9_.\-])(?:/home|/Users)/[A-Za-z0-9_.\-]+`)

	// homeSlug matches the dash-encoded form Claude Code uses for project
	// directories: /home/alice/work becomes -home-alice-work. A leading
	// '-' before the boundary excludes flags like --home-dir.
	homeSlug = regexp.MustCompile(`(^|[^A-Za-z0-9_.\-])-(?:home|Users)-[A-Za-z0-9_.]+`)
)

// SecretsString replaces credentials in s with SecretToken: the token
// shapes, URL userinfo passwords, authorization headers, bearer tokens,
// PEM and PGP private-key blocks, and the value of any NAME=value,
// NAME = value, name: value, --name value, or "name": "value" whose name
// contains token, secret, password, passwd, api_key, apikey, private_key,
// or access_key (case-insensitive). Quotes may be backslash-escaped.
//
// A value that is a variable reference ($TOKEN, ${TOKEN}, $(pass show x))
// or is already redacted is left alone, so the pass is idempotent and does
// not mangle shell that fetches a secret instead of embedding one. A
// '$'-prefixed value that is not a reference ($3cret) is a literal and is
// redacted.
//
// Matching is by name, so it over-matches: max_tokens=4096 and
// secret_manager: vault are redacted. The known false positives are
// pinned in TestSecrets_KnownFalsePositives and
// TestSecrets_ColonFormFalsePositives.
func SecretsString(s string) string {
	s = pemRE.ReplaceAllString(s, SecretToken)
	s = tokenShapes.ReplaceAllString(s, "${1}"+SecretToken)
	s = urlUserinfo.ReplaceAllStringFunc(s, func(m string) string {
		g := urlUserinfo.FindStringSubmatch(m)
		if keepValue(g[2]) {
			return m
		}
		return g[1] + SecretToken + "@"
	})
	s = authHeader.ReplaceAllString(s, "${1}"+SecretToken)
	s = bearer.ReplaceAllString(s, "${1}"+SecretToken)
	s = jsonPair.ReplaceAllString(s, `${1}"`+SecretToken+`"`)
	s = jsonPairEsc.ReplaceAllString(s, `${1}\"`+SecretToken+`\"`)
	s = replaceValue(envAssign, s)
	s = replaceValue(colonAssign, s)
	return replaceValue(flagValue, s)
}

// keepValue reports whether a credential-position value is left alone: a
// variable reference, or text already redacted.
func keepValue(v string) bool {
	return varRef.MatchString(v) || strings.HasPrefix(v, SecretToken)
}

// replaceValue rewrites the value (group 2) of each match of re to
// SecretToken, unless keepValue.
func replaceValue(re *regexp.Regexp, s string) string {
	return re.ReplaceAllStringFunc(s, func(m string) string {
		g := re.FindStringSubmatch(m)
		if keepValue(g[2]) {
			return m
		}
		return g[1] + SecretToken
	})
}

// IsSecretKey reports whether name is credential-bearing: it contains
// token, secret, password, passwd, api_key, apikey, private_key or
// access_key, case-insensitively.
func IsSecretKey(name string) bool { return secretKey.MatchString(name) }

// HomePaths rewrites /home/<name> and /Users/<name> to ~ for any name,
// and the dash-encoded -home-<name> / -Users-<name> of a Claude project
// slug to -~.
// It is the one pass that is safe over sealed history, where addresses
// are left alone.
func HomePaths(s string) string {
	s = homePath.ReplaceAllString(s, "${1}~")
	return homeSlug.ReplaceAllString(s, "${1}-~")
}

// HomePathsValue applies HomePaths to every string reachable from v (a
// JSON-shaped value: strings, []any, map[string]any). It never mutates v.
func HomePathsValue(v any) any {
	switch x := v.(type) {
	case string:
		return HomePaths(x)
	case map[string]any:
		if x == nil {
			return x
		}
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = HomePathsValue(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = HomePathsValue(vv)
		}
		return out
	default:
		return v
	}
}

// SecretsValue applies SecretsString to every string reachable from v (a
// JSON-shaped value: strings, []any, map[string]any) and replaces the
// string value of any map key whose name is credential-bearing. It
// reports whether anything changed.
//
// A container is cloned only once the first change inside it is found, so
// an unaffected value comes back as the value that went in and marshals to
// the bytes it did before this pass existed.
func SecretsValue(v any) (any, bool) {
	switch x := v.(type) {
	case string:
		r := SecretsString(x)
		return r, r != x
	case map[string]any:
		out, copied := x, false
		for k, vv := range x {
			var swept any
			var c bool
			if isSecretEntry(k, vv) {
				swept, c = SecretToken, vv != any(SecretToken)
			} else {
				swept, c = SecretsValue(vv)
			}
			if !c {
				continue
			}
			if !copied {
				out, copied = cloneMap(x), true
			}
			out[k] = swept
		}
		return out, copied
	case []any:
		out, copied := x, false
		for i, vv := range x {
			swept, c := SecretsValue(vv)
			if !c {
				continue
			}
			if !copied {
				out, copied = append([]any(nil), x...), true
			}
			out[i] = swept
		}
		return out, copied
	default:
		return v, false
	}
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Prose applies every content pass to free text bound for a tracked
// file: credentials, then addresses, then home paths of any user.
// Idempotent.
func Prose(s string) string {
	s = SecretsString(s)
	s = EmailPattern.ReplaceAllString(s, EmailToken)
	return HomePaths(s)
}

// Record returns a copy of v with f applied to every string reachable
// from it: struct fields, slice and array elements, pointees, map values,
// and interface contents. v is never mutated.
//
// Redaction is the default. A field is exempt only if it carries the tag
// `redact:"keep"`, which marks values the rest of the system relies on
// byte-for-byte: IDs, hashes, enums, timestamps. Record treats
// `redact:"path"` like an untagged field; see RecordPaths. A field added later is
// redacted the day it appears rather than the day someone remembers to
// extend a list.
//
// A map value under a credential-named key (see SecretsString for the
// names) is replaced with SecretToken whole. Other map keys and
// unexported fields are copied unchanged.
func Record[T any](v T, f func(string) string) T {
	return RecordPaths(v, f, f)
}

// RecordPaths is Record with a second function for fields tagged
// `redact:"path"`. Those fields hold values the system acts on, not prose:
// a tool grant like "Bash(gh auth token:*)" or a write-set entry. The
// credential and email passes would rewrite them and silently change what
// they enforce, so they get paths only.
func RecordPaths[T any](v T, f, paths func(string) string) T {
	rv := reflect.ValueOf(&v).Elem()
	out, _ := scrub(rv, f, paths).Interface().(T)
	return out
}

// secretValue is isSecretEntry for reflected map entries. Such a value
// has no shape of its own, so the name is the only signal; it is replaced
// whole.
func secretValue(key, val reflect.Value) bool {
	if key.Kind() != reflect.String {
		return false
	}
	// Only a value type that can hold the token is replaced whole.
	if k := val.Type().Kind(); k != reflect.Interface && k != reflect.String {
		return false
	}
	if !val.CanInterface() {
		return false
	}
	return isSecretEntry(key.String(), val.Interface())
}

// isSecretEntry reports whether a map entry is a credential by its key:
// a credential-named key holding a non-empty string, a number, or a
// non-empty list. Booleans, maps and empty values are not replaced; a
// map under such a key is walked and its own keys judged.
func isSecretEntry(key string, v any) bool {
	if !secretKey.MatchString(key) || v == nil {
		return false
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String:
		return rv.Len() > 0
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	case reflect.Slice, reflect.Array:
		return rv.Len() > 0
	}
	return false
}

// scrub returns a rewritten copy of v. The result never shares a slice,
// map, or pointee with v when v held a string anywhere beneath it.
func scrub(v reflect.Value, f, paths func(string) string) reflect.Value {
	switch v.Kind() {
	case reflect.String:
		out := reflect.New(v.Type()).Elem()
		out.SetString(f(v.String()))
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			tag := sf.Tag.Get("redact")
			if !sf.IsExported() || tag == "keep" {
				continue
			}
			// An untagged field keeps the caller's pair so a path tag
			// further down still applies; a path tag selects paths for
			// the whole subtree.
			ff := f
			if tag == "path" {
				ff = paths
			}
			out.Field(i).Set(scrub(v.Field(i), ff, paths))
		}
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(scrub(v.Elem(), f, paths))
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(scrub(v.Index(i), f, paths))
		}
		return out
	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(scrub(v.Index(i), f, paths))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		for it := v.MapRange(); it.Next(); {
			if secretValue(it.Key(), it.Value()) {
				out.SetMapIndex(it.Key(), reflect.ValueOf(SecretToken).Convert(it.Value().Type()))
				continue
			}
			out.SetMapIndex(it.Key(), scrub(it.Value(), f, paths))
		}
		return out
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(scrub(v.Elem(), f, paths))
		return out
	default:
		return v
	}
}

// PathDiffs returns the field paths, such as "Items[0].Grants[1]", at which
// a and b, two values of one type, differ inside a `redact:"path"` field.
// Fields outside a path tag are ignored. It lets a writer check that
// redaction touched enforcement fields only through the path function.
func PathDiffs[T any](a, b T) []string {
	var out []string
	diffPaths(reflect.ValueOf(&a).Elem(), reflect.ValueOf(&b).Elem(), false, "", &out)
	return out
}

func diffPaths(a, b reflect.Value, inPath bool, name string, out *[]string) {
	switch a.Kind() {
	case reflect.String:
		if inPath && a.String() != b.String() {
			*out = append(*out, name)
		}
	case reflect.Struct:
		t := a.Type()
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			tag := sf.Tag.Get("redact")
			if !sf.IsExported() || tag == "keep" {
				continue
			}
			sub := sf.Name
			if name != "" {
				sub = name + "." + sf.Name
			}
			diffPaths(a.Field(i), b.Field(i), inPath || tag == "path", sub, out)
		}
	case reflect.Pointer, reflect.Interface:
		if !a.IsNil() && !b.IsNil() {
			diffPaths(a.Elem(), b.Elem(), inPath, name, out)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < a.Len() && i < b.Len(); i++ {
			diffPaths(a.Index(i), b.Index(i), inPath, fmt.Sprintf("%s[%d]", name, i), out)
		}
	case reflect.Map:
		for it := a.MapRange(); it.Next(); {
			if bv := b.MapIndex(it.Key()); bv.IsValid() {
				diffPaths(it.Value(), bv, inPath, fmt.Sprintf("%s[%v]", name, it.Key()), out)
			}
		}
	}
}
