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
// Limitation: a path component made only of whitespace followed by .. (for
// example "/home/ /../bob") is not normalised, so a home hidden behind it is
// not seen. Collapsing across whitespace would redact shell text such as
// "cd <repo>/docs && make; cd ../../..", where "docs && make; cd .." would be
// taken for one component.
//
// The package depends on the standard library only. The audit path
// (internal/hook) and the mission writers (internal/mission) both import
// it; neither imports the other's policy.
package redact

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Replacement tokens. EmailToken and the audit package's
// "[redacted]" match the hand-redacted lines from the public-website
// PR #259 cleanup, so one grep finds both the hand-redacted history and
// everything written since.
const (
	EmailToken  = "[redacted-email]"
	SecretToken = "[redacted-secret]"
	SlugToken   = "[redacted-slug]"
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
	// person as surely as this one does. HomePaths keeps a match only where
	// pathStart says a path begins, the rule Leaks applies, so a match inside
	// another path component (/etc/home/x) is left alone.
	//
	// <name> is any run of characters but a slash, whitespace or a quote.
	// Case is folded, and a doubled slash or /./ between the parts is
	// skipped, as the filesystem would.
	homePath = regexp.MustCompile("(?i:/home|/Users)/+(?:\\./+)*[^/\\s\"'`]+")

	// homeSlug matches the dash-encoded form Claude Code uses for project
	// directories: /home/alice/work becomes -home-alice-work. A leading
	// '-' before the boundary excludes flags like --home-dir. The username
	// may itself contain dashes, so the match takes the whole component, up
	// to a slash, whitespace or a quote.
	homeSlug = regexp.MustCompile("(^|[/\\s\"'=:(])-(?i:home|Users)-[^\\s/\"'`]+")
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
// access_key, case-insensitively, or is authorization or proxy-authorization.
func IsSecretKey(name string) bool {
	return (secretKey.MatchString(name) || authorizationKey.MatchString(name)) && !placeholderKey.MatchString(name)
}

// authorizationKey matches the whole name of a structured authorization header,
// authorization or proxy-authorization in any case: its value is a credential
// (Basic dXNlcjpwYXNz) whatever it looks like. A name that only holds the word
// (authorization_url) is not one.
var authorizationKey = regexp.MustCompile(`(?i)^(?:proxy-)?authorization$`)

// placeholderKey matches the names a pass gives a renamed key, with a #N
// suffix when the name was taken. They hold the word secret and are not
// credential names, or a second pass would replace their values.
var placeholderKey = regexp.MustCompile(`^\[redacted-(?:secret|key)\](?:#[0-9]+)?$`)

// HomePaths rewrites /home/<name> and /Users/<name> to ~ for any name,
// and a foreign dash-encoded -home-<name>... / -Users-<name>... Claude
// project slug to -~-[redacted-slug]. A slug does not say where the
// username ends, so the whole component goes; the current user's own slug
// is handled exactly by PathRedactor, which knows the prefix.
// It is the one pass that is safe over sealed history, where addresses
// are left alone.
//
// A match is rewritten only where pathStartFrom says a path begins, the rule
// Leaks uses, so the pass never leaves what its checker would refuse. Every
// match is another user's home: this machine's own is rewritten to ~ by the
// PathRedactor before this pass runs, so what is left becomes
// ~[redacted-user], never a ~ that would read as the current user's. Where the
// match follows a glued flag, a .. prefix or a placeholder, the whole prefix
// and the match become [redacted-path], never a ~ glued onto them. A path
// directly after a <repo> that begins a path is a top-level directory of the
// repository and is left as it is.
func HomePaths(s string) string {
	// A rewrite can make the next match a path start (/home/0/home/0 becomes
	// ~[redacted-user]/home/0 and then something shorter), so the pass runs
	// to a fixed point: a second call changes nothing.
	for {
		t := homePathsOnce(s)
		if t == s {
			return s
		}
		s = t
	}
}

// climbOut matches a ~, <repo> or redacted-user placeholder followed by .. steps and
// the next component: a path under the parent of the home or repo.
var climbOut = regexp.MustCompile("(^|[^A-Za-z0-9_.\\-/])(~\\[redacted-user\\]|~|<repo>)((?:/+(?:\\./+)*\\.\\.)+)(?:(/+(?:\\./+)*[^/\\s\"'`]+)|($|[^A-Za-z0-9_.\\-]))")

func homePathsOnce(s string) string {
	s = climbOut.ReplaceAllString(s, "${1}[redacted-path]${5}")
	spans := homeSpans(s)
	// A JSON-escaped path (\/home\/bob) is found in its plain spelling, which
	// Leaks checks as well, and only the spans it finds are rewritten: other
	// \/ in the value stay as written.
	if strings.Contains(s, `\/`) {
		u, back := unescapeSlashes(s)
		if us := homeSpans(u); len(us) > len(spans) {
			spans = spans[:0]
			for _, sp := range us {
				spans = append(spans, span{back[sp.from], back[sp.end], sp.tok})
			}
		}
	}
	var b strings.Builder
	last := 0
	for _, sp := range spans {
		b.WriteString(s[last:sp.from])
		b.WriteString(sp.tok)
		last = sp.end
	}
	b.WriteString(s[last:])
	return homeSlug.ReplaceAllString(b.String(), "${1}-~-"+SlugToken)
}

// span is a stretch of a string, s[from:end], to replace with tok.
type span struct {
	from, end int
	tok       string
}

// homeSpans returns the home paths of s to rewrite, in order and without
// overlap.
func homeSpans(s string) []span {
	var out []span
	last := 0
	for _, m := range homePath.FindAllStringIndex(s, -1) {
		i := m[0]
		from, ok := pathStartFrom(s, i)
		if !ok || afterRepo(s, i) || from < last {
			continue
		}
		tok := "~[redacted-user]"
		switch {
		case from < i:
			tok = "[redacted-path]"
		case strings.HasSuffix(s[:i], "~[redacted-user]"):
			from, tok = i-len("~[redacted-user]"), "[redacted-path]"
		case strings.HasSuffix(s[:i], "~"):
			from, tok = i-1, "[redacted-path]"
		}
		if from < last {
			continue
		}
		out = append(out, span{from, m[1], tok})
		last = m[1]
	}
	return out
}

// unescapeSlashes replaces each \/ in s with /. back maps a byte offset of
// the result (len+1 entries) to the offset in s it came from.
func unescapeSlashes(s string) (string, []int) {
	var b strings.Builder
	back := make([]int, 0, len(s)+1)
	for i := 0; i < len(s); i++ {
		back = append(back, i)
		if s[i] == '\\' && i+1 < len(s) && s[i+1] == '/' {
			i++
			b.WriteByte('/')
			continue
		}
		b.WriteByte(s[i])
	}
	back = append(back, len(s))
	return b.String(), back
}

// RenameKeys returns a copy of m whose keys are renamed by rename, with value
// applied to each value (given the original key), and whether any key was
// renamed. It is the one way a pass renames map keys, so that no two keys
// ever merge and nothing is lost or overwritten:
//
//   - keys rename leaves alone go first, sorted, under their own names;
//   - the others follow in sorted order of their original names, each under
//     its new name, or the first free name+"#N" (N from 2) if that is taken;
//   - a name is taken exactly, or, with foldCase, under strings.EqualFold, as
//     encoding/json folds the keys of a struct-decoded object.
func RenameKeys(m map[string]any, rename func(string) string, foldCase bool, value func(key string, v any) any) (map[string]any, bool) {
	names, renamed := PlanKeys(sortedKeys(m), rename, foldCase)
	out := make(map[string]any, len(m))
	for k, n := range names {
		out[n] = value(k, m[k])
	}
	return out, renamed
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// PlanKeys decides the output name of each key by the rules of RenameKeys and
// returns a map from each original key to its output name, and whether any key
// was renamed.
func PlanKeys(keys []string, rename func(string) string, foldCase bool) (map[string]string, bool) {
	names, st := planKeys(keys, rename, foldCase)
	return names, st.Renamed
}

// RenameStatus says whether any key was renamed and whether any renamed key
// took a name#N suffix because its new name was taken.
type RenameStatus struct{ Renamed, Suffixed bool }

func (a RenameStatus) or(b RenameStatus) RenameStatus {
	return RenameStatus{a.Renamed || b.Renamed, a.Suffixed || b.Suffixed}
}

func planKeys(keys []string, rename func(string) string, foldCase bool) (map[string]string, RenameStatus) {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	canon := func(s string) string { return s }
	if foldCase {
		canon = foldKey
	}
	names := make(map[string]string, len(sorted))
	taken := make(map[string]bool, len(sorted))
	renamedTo := make(map[string]string, len(sorted)) // original -> rename(original)
	for _, k := range sorted {
		nk := rename(k)
		renamedTo[k] = nk
		if nk == k {
			names[k] = k
			taken[canon(k)] = true
		}
	}
	var st RenameStatus
	next := map[string]int{} // base name -> next suffix to try
	for _, k := range sorted {
		nk := renamedTo[k]
		if nk == k {
			continue
		}
		st.Renamed = true
		name := nk
		if taken[canon(name)] {
			st.Suffixed = true
			n := next[nk]
			if n < 2 {
				n = 2
			}
			for ; taken[canon(fmt.Sprintf("%s#%d", nk, n))]; n++ {
			}
			name = fmt.Sprintf("%s#%d", nk, n)
			next[nk] = n + 1
		}
		names[k] = name
		taken[canon(name)] = true
	}
	return names, st
}

// foldKey is a canonical form of s under Unicode simple folding: two strings
// are strings.EqualFold exactly when their foldKeys are equal.
func foldKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		min := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < min {
				min = f
			}
		}
		b.WriteRune(min)
	}
	return b.String()
}

// DuplicateJSONKey scans the tokens of one JSON value for an object that holds
// the same key twice, exactly, at any depth, and returns the path of the first
// such key (name, items[0].name) and the key. A reader keeps the last of two,
// so a record holding a duplicate is not the record it looks like.
func DuplicateJSONKey(data []byte) (path, key string, found bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	type frame struct {
		isObj     bool
		seen      map[string]bool
		expectKey bool
		key       string // the key whose value is being read (objects)
		idx       int    // the index of the element being read (arrays)
		path      string // the path of this container
	}
	join := func(base, seg string) string {
		if base == "" {
			return seg
		}
		return base + "." + seg
	}
	var stack []frame
	// valueDone records that the value in the top frame has been read.
	valueDone := func() {
		if n := len(stack); n > 0 {
			if stack[n-1].isObj {
				stack[n-1].expectKey = true
			} else {
				stack[n-1].idx++
			}
		}
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", "", false
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '{' || d == '[' {
				childPath := ""
				if n := len(stack); n > 0 {
					f := stack[n-1]
					if f.isObj {
						childPath = join(f.path, f.key)
					} else {
						childPath = fmt.Sprintf("%s[%d]", f.path, f.idx)
					}
				}
				stack = append(stack, frame{isObj: d == '{', seen: map[string]bool{}, expectKey: d == '{', path: childPath})
				continue
			}
			stack = stack[:len(stack)-1]
			valueDone()
			continue
		}
		n := len(stack)
		if n == 0 {
			continue
		}
		f := &stack[n-1]
		if f.isObj && f.expectKey {
			k, _ := tok.(string)
			if f.seen[k] {
				return join(f.path, k), k, true
			}
			f.seen[k] = true
			f.key = k
			f.expectKey = false
			continue
		}
		valueDone()
	}
}

// HomePathsValue applies HomePaths to every string reachable from v (a
// JSON-shaped value: strings, []any, map[string]any), keys included. It never
// mutates v.
func HomePathsValue(v any) any {
	out, _ := HomePathsValueFlag(v)
	return out
}

// HomePathsValueFlag is HomePathsValue also reporting whether any map key was
// renamed.
func HomePathsValueFlag(v any) (any, bool) {
	switch x := v.(type) {
	case string:
		return HomePaths(x), false
	case map[string]any:
		if x == nil {
			return x, false
		}
		any1 := false
		out, renamed := RenameKeys(x, HomePaths, false, func(_ string, vv any) any {
			r, f := HomePathsValueFlag(vv)
			any1 = any1 || f
			return r
		})
		return out, renamed || any1
	case []any:
		out := make([]any, len(x))
		changed := false
		for i, vv := range x {
			var f bool
			out[i], f = HomePathsValueFlag(vv)
			changed = changed || f
		}
		return out, changed
	default:
		return v, false
	}
}

// SecretsValue applies SecretsString to every string reachable from v (a
// JSON-shaped value: strings, []any, map[string]any) and to every map key, and
// replaces the string value of any map key whose name is credential-bearing.
// It reports whether anything changed.
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
		// A credential used as a key is redacted too. A key the credential
		// pass changes at all is replaced whole: a partly redacted key could
		// keep part of the credential, or change again on a second pass. The
		// renaming is the shared collision-safe one, so two keys that redact to
		// the same name do not merge.
		for k := range x {
			if SecretsString(k) != k {
				out, _ := RenameKeys(x, wholeSecretKey, false, func(key string, vv any) any {
					if isSecretEntry(key, vv) {
						return SecretToken
					}
					swept, _ := SecretsValue(vv)
					return swept
				})
				return out, true
			}
		}
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

// wholeSecretKey is SecretToken for a key the credential pass changes, and the
// key itself otherwise.
func wholeSecretKey(k string) string {
	if SecretsString(k) != k {
		return SecretToken
	}
	return k
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

var handleRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// IsHandle reports whether s is an identity handle: lowercase letters, digits
// and hyphens, starting and ending with a letter or digit (the rule of
// internal/identity, which this package cannot import), and not a credential:
// sk- and glpat- followed by letters have the shape of a handle and are secrets,
// so a value the credential pass would change is not one.
func IsHandle(s string) bool { return handleRE.MatchString(s) && SecretsString(s) == s }

var sessionIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// IsSessionID reports whether s has the shape of a session ID: a file-name-safe
// token (letters, digits, . _ -), as the UUID a harness reports or an
// ETHOS_SESSION value is, and not a credential the credential pass would change.
// ethos generates none itself, so this is the class a session directory name and
// a chunk name are built from; an address (@) is outside it.
func IsSessionID(s string) bool { return sessionIDRE.MatchString(s) && SecretsString(s) == s }

var eventNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// IsEventName reports whether s has the shape of a mission event name as ethos
// writes them (create, round_advanced, write_set_released, disclaim_delegation):
// lower-case words joined by underscores, not a credential the pass would change.
func IsEventName(s string) bool { return eventNameRE.MatchString(s) && SecretsString(s) == s }

// ValueTag reports whether tag names a field kept only when its value has the
// tag's shape: "handle", "session" or "event".
func ValueTag(tag string) bool { return tag == "handle" || tag == "session" || tag == "event" }

// keepsValue reports whether a field with redact tag tag keeps value v as it is:
// "handle" keeps an identity handle, "session" a session ID shape, "event" an
// event name shape.
func keepsValue(tag, v string) bool {
	switch tag {
	case "handle":
		return IsHandle(v)
	case "session":
		return IsSessionID(v)
	case "event":
		return IsEventName(v)
	}
	return false
}

// KeepsValue is keepsValue for the walkers outside this package.
func KeepsValue(tag, v string) bool { return keepsValue(tag, v) }

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
// byte-for-byte: IDs, hashes, enums, timestamps. A field tagged
// `redact:"handle"` holds an identity handle: it is kept when it is one
// (IsHandle) and otherwise is redacted like any other field; a field tagged
// `redact:"session"` is kept when it has a session ID's shape (IsSessionID) and
// otherwise redacted the same way.
// Record treats
// `redact:"path"` like an untagged field; see RecordPaths. A field added later is
// redacted the day it appears rather than the day someone remembers to
// extend a list.
//
// A map value under a credential-named key (see SecretsString for the
// names) is replaced with SecretToken whole. String map keys go through f
// as values do, except that a key the credential pass changes becomes
// SecretToken whole; unexported fields are copied unchanged.
func Record[T any](v T, f func(string) string) T {
	return RecordPaths(v, f, f)
}

// RecordPaths is Record with a second function for fields tagged
// `redact:"path"`. Those fields hold values the system acts on, not prose:
// a tool grant like "Bash(gh auth token:*)" or a write-set entry. The
// credential and email passes would rewrite them and silently change what
// they enforce, so they get paths only.
func RecordPaths[T any](v T, f, paths func(string) string) T {
	out, _ := RecordPathsFlag(v, f, paths)
	return out
}

// RecordPathsFlag is RecordPaths also reporting whether any map key, at any
// depth, was renamed.
func RecordPathsFlag[T any](v T, f, paths func(string) string) (T, bool) {
	out, st := RecordPathsStatus(v, f, paths)
	return out, st.Renamed
}

// RecordPathsStatus is RecordPaths also reporting the status of any key
// renames at any depth.
func RecordPathsStatus[T any](v T, f, paths func(string) string) (T, RenameStatus) {
	rv := reflect.ValueOf(&v).Elem()
	var st RenameStatus
	out, _ := scrub(rv, f, paths, &st).Interface().(T)
	return out, st
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
	if !IsSecretKey(key) || v == nil {
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
func scrub(v reflect.Value, f, paths func(string) string, renamed *RenameStatus) reflect.Value {
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
			if ValueTag(tag) {
				// An identity handle is kept as it is; anything else in the
				// field is text like any other and gets the caller's pass.
				if fv := v.Field(i); fv.Kind() == reflect.String && !keepsValue(tag, fv.String()) {
					out.Field(i).SetString(f(fv.String()))
				}
				continue
			}
			// An untagged field keeps the caller's pair so a path tag
			// further down still applies; a path tag selects paths for
			// the whole subtree.
			ff := f
			if tag == "path" {
				ff = paths
			}
			out.Field(i).Set(scrub(v.Field(i), ff, paths, renamed))
		}
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(scrub(v.Elem(), f, paths, renamed))
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(scrub(v.Index(i), f, paths, renamed))
		}
		return out
	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(scrub(v.Index(i), f, paths, renamed))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		if v.Type().Key().Kind() != reflect.String {
			for it := v.MapRange(); it.Next(); {
				out.SetMapIndex(it.Key(), scrub(it.Value(), f, paths, renamed))
			}
			return out
		}
		// String keys pass through f by the one rule that never merges two:
		// see PlanKeys.
		byName := make(map[string]reflect.Value, v.Len())
		keys := make([]string, 0, v.Len())
		for it := v.MapRange(); it.Next(); {
			byName[it.Key().String()] = it.Key()
			keys = append(keys, it.Key().String())
		}
		// A key the credential pass changes is replaced whole, as
		// SecretsValue does, whatever f would make of it.
		names, r := planKeys(keys, func(k string) string {
			if SecretsString(k) != k {
				return SecretToken
			}
			return f(k)
		}, false)
		*renamed = renamed.or(r)
		for _, k := range keys {
			key := byName[k]
			nk := reflect.New(key.Type()).Elem()
			nk.SetString(names[k])
			val := v.MapIndex(key)
			if secretValue(key, val) {
				out.SetMapIndex(nk, reflect.ValueOf(SecretToken).Convert(val.Type()))
				continue
			}
			out.SetMapIndex(nk, scrub(val, f, paths, renamed))
		}
		return out
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(scrub(v.Elem(), f, paths, renamed))
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
			if !sf.IsExported() || tag == "keep" || ValueTag(tag) {
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

// FieldString is a string held in a record field that is not tagged
// `redact:"keep"` (nor `redact:"handle"` while it holds a handle), with the field
// path that names it, such as "Tools[0]".
type FieldString struct {
	Name  string
	Value string
	// Path is true for a value inside a `redact:"path"` field.
	Path bool
}

// FieldStrings returns every string of v outside a `redact:"keep"` field, and
// outside a `redact:"handle"` field that holds a handle, in field order. It lets a writer check what the passes left behind, which
// RecordPaths applies but cannot judge.
func FieldStrings[T any](v T) []FieldString {
	var out []FieldString
	collectStrings(reflect.ValueOf(&v).Elem(), "", false, &out)
	return out
}

func collectStrings(v reflect.Value, name string, path bool, out *[]FieldString) {
	switch v.Kind() {
	case reflect.String:
		*out = append(*out, FieldString{Name: name, Value: v.String(), Path: path})
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			if !sf.IsExported() || sf.Tag.Get("redact") == "keep" {
				continue
			}
			if fv := v.Field(i); fv.Kind() == reflect.String && keepsValue(sf.Tag.Get("redact"), fv.String()) {
				continue
			}
			sub := sf.Name
			if name != "" {
				sub = name + "." + sf.Name
			}
			collectStrings(v.Field(i), sub, path || sf.Tag.Get("redact") == "path", out)
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			collectStrings(v.Elem(), name, path, out)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			collectStrings(v.Index(i), fmt.Sprintf("%s[%d]", name, i), path, out)
		}
	case reflect.Map:
		keys := v.MapKeys()
		sort.Slice(keys, func(a, b int) bool { return fmt.Sprint(keys[a]) < fmt.Sprint(keys[b]) })
		// Elements are named by position: a key can itself be the leak.
		for i, k := range keys {
			if k.Kind() == reflect.String {
				*out = append(*out, FieldString{Name: fmt.Sprintf("%s{key %d}", name, i), Value: k.String(), Path: path})
			}
			collectStrings(v.MapIndex(k), fmt.Sprintf("%s{%d}", name, i), path, out)
		}
	}
}

var (
	leakPath = regexp.MustCompile("/(?:home|users)/[^/\\s\"'`]+")
	leakSlug = regexp.MustCompile("-(?:home|users)-[^\\s/\"'`-]+-")
	// leakSlugEnd is a slug that ends its component with the name, no dash
	// after it (-var-home-bob/f, C--Users-al, x/-home-bob), and only inside a
	// component that is dash-encoded: it starts with a dash, or a letter and two
	// dashes. The text between the leading dash and the slug holds no white
	// space, quote or slash, so prose ("- add the get-users-list endpoint") is
	// not read as a slug. Matched lowercased. Known costs: a one-letter BEM block
	// (a/b--home-page.scss) and a component that is only two dashes and a word
	// (--home-x) have that shape and match; nav--home-page.scss and
	// btn--users-list.css do not start that way.
	leakSlugEnd = regexp.MustCompile("(?:^|/)(?:[a-z]-)?-(?:[^/\\s\"'\\x60]*-)?(?:home|users)-[^\\s/\"'\\x60-]+(?:[/\\s\"'\\x60]|$)")
	// leakSlugEndTilde is the slug-end rule for a value that names a file of the
	// checkout or of this machine's home (InsideRule, HomeRelativeRule), where a
	// file such as a/b--home-page.scss is likely: a component that starts with a
	// dash, or a letter and two dashes with a name that has no dot (C--Users-al,
	// a--home-bob; a file's name has a dot). Known cost: a dotless one-letter
	// block (a/b--home-page) has the shape of a slug and matches.
	leakSlugEndTilde = regexp.MustCompile("(?:^|/)(?:-(?:[^/\\s\"'\\x60]*-)?(?:home|users)-[^\\s/\"'\\x60-]+|[a-z]--(?:[^/\\s\"'\\x60]*-)?(?:home|users)-[^\\s/\"'\\x60.-]+)(?:[/\\s\"'\\x60]|$)")
)

// Leaks reports whether s names a home directory. See LeakRule.
func Leaks(s, home string) bool { return LeakRule(s, home) != "" }

// LeakRule names the rule s breaks, in words fit for a refusal message: it
// never quotes the value, which is the leak. It returns "" when s names no
// home. The rules are plain substring tests, with no notion of where a path
// begins; over-refusing a harmless value costs less than storing a leak:
//
//  1. this machine's home path, ending at the end of a path component (so
//     HOME=/root does not match /rootfs);
//  2. this machine's dash-encoded home (-home-<me>), ending at the end of a
//     component or before a -;
//  3. /home/<x> or /Users/<x> for any non-empty <x> (anything but a slash,
//     whitespace or a quote), anywhere in the string;
//  4. -home-<x>- or -Users-<x>-, a foreign dash-encoded home, anywhere;
//  5. a ~ or <repo> placeholder that climbs out of itself with ..
//
// Matching is case-insensitive and runs on every spelling a reader or a
// filesystem would resolve: the string as written, lower-cased, with slash
// runs collapsed, /./ removed and /x/../ resolved, JSON-unescaped (\/) and
// percent-decoded. Every spelling is checked in addition to the original,
// never instead of it. A username alone is not searched for: a directory
// named like the account (internal/runner/) is not a home.
func LeakRule(s, home string) string { return leakRule(s, home, ruleFull) }

// HoldsOwnDashHome reports whether s holds this machine's dash-encoded home
// (-home-zed for /home/zed) ending at a path component, in any spelling
// LeakRule looks at. A contract path entry that is new or changed is refused
// when it does (mission.refuseOwnDashHome): readers compare those entries
// literally, so the writer cannot rewrite the slug to -~. Every other
// path-tagged value is rewritten.
func HoldsOwnDashHome(s, home string) bool {
	if home == "" || home == "/" {
		return false
	}
	dash := dashOf(home)
	if dash == "-" {
		return false
	}
	unesc := strings.ReplaceAll(s, `\/`, "/")
	for _, c := range []string{s, unesc, pctDecode(s), pctDecode(unesc)} {
		for _, v := range [][2]string{{c, dash}, {strings.ToLower(c), strings.ToLower(dash)}, {normalizePath(c), normalizePath(dash)}} {
			if containsEnd(v[0], v[1], true) {
				return true
			}
		}
	}
	return false
}

// OwnHomeRule is LeakRule without rules 3 to 5: it looks only for this
// machine's own home and its dash-encoded form anywhere, and has no foreign-slug
// rule. It judges a repo-relative value (RepoRelative) in a mission path field, where
// app/home/page.tsx and nav--home-page.scss are files of the repository, not
// somebody's home. (The audit log's path fields use InsideRule inside the
// checkout, HomeRelativeRule under ~/, and LeakRule elsewhere.)
func OwnHomeRule(s, home string) string { return leakRule(s, home, ruleOwn) }

// InsideRule is LeakRule without the rule that reads /home/<name> or
// /Users/<name> in the middle of a path as another user's home, and with
// leakSlugEndTilde as its slug-end rule. It judges a path field value of the
// audit log that names a file of the checkout (see
// mission.PathRedactor.AuditLeakRule), where app/home/page.tsx is a file. Still
// refused: this machine's home and slug, a foreign dash slug with a trailing
// dash (leakSlug), a slug that ends at its name in a dash-encoded component
// (-var-home-bob, C--Users-al, src/a--home-bob; a dotless a/b--home-page too)
// and a climb out of a placeholder.
func InsideRule(s, home string) string { return leakRule(s, home, ruleInside) }

// HomeRelativeRule is InsideRule for a value under ~/ (this machine's own
// home); the rules are the same, so ~/.claude/projects/C--Users-al is caught and
// ~/x-wt/a/b--home-page.scss is a file.
func HomeRelativeRule(s, home string) string { return leakRule(s, home, ruleHomeRel) }

// The sets of rules leakRule applies.
const (
	ruleFull    = iota // all of them
	ruleOwn            // this machine's own home only
	ruleInside         // all but the mid-path /home/<name> and /Users/<name> rule; slug-end by leakSlugEndTilde
	ruleHomeRel        // as ruleInside, for a ~/ value: same rules, leakSlugEndTilde
)

// relPath is the only shape a path-field value may have to be judged as a
// file of the repository: segments of name characters (and the * and @ of a
// glob or a scoped package) joined by single slashes, with an optional trailing
// slash. Anything else, a tool grant like Read(app/x) included, is judged in
// full.
var relPath = regexp.MustCompile(`^[A-Za-z0-9_.@+*-]+(?:/[A-Za-z0-9_.@+*-]+)*/?$`)

// RepoRelative reports whether v is a plain repo-relative path: it matches
// relPath and has no .. segment. No escape, scheme, separator, whitespace or
// placeholder can appear in such a value, so no other spelling of it exists.
func RepoRelative(v string) bool {
	if !relPath.MatchString(v) {
		return false
	}
	for _, seg := range strings.Split(strings.TrimSuffix(v, "/"), "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

func leakRule(s, home string, mode int) string {
	unesc := strings.ReplaceAll(s, `\/`, "/")
	for _, c := range []string{s, unesc, pctDecode(s), pctDecode(unesc)} {
		for _, v := range [][2]string{
			{c, home},
			{strings.ToLower(c), strings.ToLower(home)},
			{normalizePath(c), normalizePath(home)},
		} {
			if r := leakRuleOf(v[0], v[1], mode); r != "" {
				return r
			}
		}
	}
	return ""
}

// leakRuleOf applies the rules to one spelling.
func leakRuleOf(n, home string, mode int) string {
	if home != "" && home != "/" {
		if containsEnd(n, home, false) {
			return "names this machine's home directory"
		}
		if dash := dashOf(home); dash != "-" && containsEnd(n, dash, true) {
			return "holds the dash-encoded home directory"
		}
	}
	if mode == ruleOwn {
		return ""
	}
	if mode != ruleInside && mode != ruleHomeRel && leakPath.MatchString(n) {
		return "contains a /home/ or /Users/ path"
	}
	if leakSlug.MatchString(n) || ((mode == ruleInside || mode == ruleHomeRel) && leakSlugEndTilde.MatchString(n)) || (mode != ruleInside && mode != ruleHomeRel && leakSlugEnd.MatchString(n)) {
		return "contains a home-directory slug"
	}
	if climbOut.MatchString(n) {
		return "climbs out of a ~ or <repo> placeholder with .."
	}
	return ""
}

// normalizePath folds case, collapses runs of slashes, removes /./ and
// resolves /x/../, until nothing changes.
func normalizePath(s string) string {
	s = strings.ToLower(s)
	for {
		t := strings.ReplaceAll(s, "//", "/")
		t = strings.ReplaceAll(t, "/./", "/")
		t = upDir.ReplaceAllStringFunc(t, func(m string) string {
			if seg := m[1 : len(m)-4]; seg == ".." || seg == "." {
				return m
			}
			return "/"
		})
		if t == s {
			return s
		}
		s = t
	}
}

// upDir matches /x/../, a component and the step back out of it. A component
// holding whitespace is a run of shell words, not a path component, so
// `cd <repo>/docs && make; cd ../../..` is not collapsed across the spaces
// (TestLeaks_ShellTextIsNotCollapsed).
var upDir = regexp.MustCompile(`/[^/\s]+/\.\./`)

// containsEnd reports whether sub occurs in n ending at the end of a path
// component: at the end of n or before a byte that is no part of a name. With
// dash, a following - also ends it, as in the dash-encoded home -root-proj.
func containsEnd(n, sub string, dash bool) bool {
	for i := 0; ; {
		j := strings.Index(n[i:], sub)
		if j < 0 {
			return false
		}
		at := i + j
		end := at + len(sub)
		if end == len(n) || !isNameByte(n[end]) || (dash && n[end] == '-') {
			return true
		}
		i = at + 1
	}
}

// pctDecode decodes every well-formed %XX in s and leaves the rest.
func pctDecode(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s)+0 && isHex(s[i+1]) && isHex(s[i+2]) {
			b.WriteByte(unhex(s[i+1])<<4 | unhex(s[i+2]))
			i += 2
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c >= 'a':
		return c - 'a' + 10
	case c >= 'A':
		return c - 'A' + 10
	}
	return c - '0'
}

// isNameByte reports whether c can be part of a path component's name.
func isNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '_' || c == '.' || c == '-'
}

// pathStartFrom reports whether a path begins at s[i], a slash, and where the
// text that belongs to it begins: i itself, or earlier when a flag or a
// relative prefix is glued on.
//
// A path begins at the start of s, after a byte that is no part of a name,
// and after a run of slashes that itself begins one (file:///home). It
// begins after a chain of .. components that itself begins a path (cd
// ../../home/bob), and after components that .. steps cancel when what is
// left begins one (/tmp/../home/bob), and after /. components of an absolute
// path. It begins right after a short flag, as in gcc -I/home/bob/inc: the
// flag letter is not part of a name. A relative ./home/x, home/x or
// testdata/../home/x is not a path start.
func pathStartFrom(s string, i int) (int, bool) {
	j := i
	for j > 0 && s[j-1] == '/' {
		j--
	}
	if j == 0 || !isNameByte(s[j-1]) {
		return i, true
	}
	k := j
	for k > 0 && isNameByte(s[k-1]) {
		k--
	}
	comp := s[k:j]
	switch {
	case len(comp) == 2 && comp[0] == '-' && isLetter(comp[1]):
		return k, true
	case comp == ".":
		if k > 0 && s[k-1] == '/' {
			return pathStartFrom(s, k-1)
		}
		return 0, false
	case comp == "..":
		return climbStart(s, k)
	}
	return 0, false
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// climbStart handles a chain of .. components whose rightmost one starts at
// s[k]. If the chain begins a path the match is a path start; otherwise each
// .. cancels the component to its left, and the path is a start if what is
// left of those begins one.
func climbStart(s string, k int) (int, bool) {
	pos, n := k, 0
	for {
		n++
		q := pos
		for q > 0 && s[q-1] == '/' {
			q--
		}
		if q == pos || q == 0 {
			break
		}
		kk := q
		for kk > 0 && isNameByte(s[kk-1]) {
			kk--
		}
		if s[kk:q] != ".." {
			break
		}
		pos = kk
	}
	if pos == 0 || (!isNameByte(s[pos-1]) && s[pos-1] != '/') {
		return pos, true
	}
	if q := pos; true {
		for q > 0 && s[q-1] == '/' {
			q--
		}
		if q == 0 {
			// /../home: the steps climb out of the root.
			return 0, true
		}
	}
	p := pos
	for t := 0; t < n; t++ {
		q := p
		for q > 0 && s[q-1] == '/' {
			q--
		}
		if q == p {
			return 0, false
		}
		kk := q
		for kk > 0 && isCompByte(s[kk-1]) {
			kk--
		}
		if kk == q {
			return 0, false
		}
		p = kk
	}
	q := p
	for q > 0 && s[q-1] == '/' {
		q--
	}
	if q == p {
		return 0, false
	}
	return pathStartFrom(s, q)
}

// isCompByte reports whether c can be part of a path component for the
// purpose of resolving .. steps: anything but a slash or whitespace, as
// normalizePath's /x/../ pattern reads it.
func isCompByte(c byte) bool {
	return c != '/' && c != ' ' && c != '\t' && c != '\n' && c != '\r'
}

// dashOf is the dash-encoded form of a directory: /home/alice is
// -home-alice.
func dashOf(dir string) string {
	return strings.ReplaceAll(dir, "/", "-")
}

// afterRepo reports whether a <repo> that begins a path ends at s[i].
func afterRepo(s string, i int) bool {
	const repo = "<repo>"
	if !strings.HasSuffix(s[:i], repo) {
		return false
	}
	k := i - len(repo)
	return k == 0 || !(isNameByte(s[k-1]) || s[k-1] == '/')
}

var absPath = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.\-/])/[A-Za-z0-9_.\-~]`)

// AbsolutePath reports whether s holds something that reads as an absolute
// path. A writer that cannot resolve the home directory has no way to tell a
// home from any other path, so it treats every one as a leak.
func AbsolutePath(s string) bool { return absPath.MatchString(s) }
