package redact

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fixtures are assembled at run time so this file does not itself trip a
// secret scanner or this package's own sweep when it is committed.
var (
	ghToken  = "ghp_" + strings.Repeat("a1B2", 9)
	awsKeyID = "AKIA" + "IOSFODNN7EXAMPLE"
	skKey    = "sk-" + strings.Repeat("x9Y8", 8)
	skAnt    = "sk-ant-api03-" + strings.Repeat("Zz09", 6)
	pemBlock = "-----BEGIN " + "RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA7\nabc\n-----END " + "RSA PRIVATE KEY-----"
)

func TestSecrets_Shapes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"ghp", "token is " + ghToken + " ok", "token is [redacted-secret] ok"},
		{"gho", "x gho_" + strings.Repeat("a", 36), "x [redacted-secret]"},
		{"ghu", "ghu_" + strings.Repeat("b", 36), "[redacted-secret]"},
		{"ghs", "(ghs_" + strings.Repeat("c", 36) + ")", "([redacted-secret])"},
		{"ghr", "ghr_" + strings.Repeat("d", 36), "[redacted-secret]"},
		{"github_pat", "github_pat_" + strings.Repeat("E1_", 12), "[redacted-secret]"},
		{"sk key", "key " + skKey, "key [redacted-secret]"},
		{"sk-ant key", "key " + skAnt, "key [redacted-secret]"},
		{"aws AKIA", "id " + awsKeyID + " end", "id [redacted-secret] end"},
		{"aws ASIA", "id ASIA" + "ABCDEFGHIJKLMNOP", "id [redacted-secret]"},
		{"authorization header", "Authorization: Bearer abc.def", "Authorization: Bearer [redacted-secret]"},
		{"authorization basic", "authorization: Basic dXNlcjpwYXNz", "authorization: Basic [redacted-secret]"},
		{"bare bearer", "curl -H 'Bearer " + strings.Repeat("q7", 10) + "'", "curl -H 'Bearer [redacted-secret]'"},
		{"pem block", "before\n" + pemBlock + "\nafter", "before\n[redacted-secret]\nafter"},
		{"pem truncated", "k " + "-----BEGIN " + "PRIVATE KEY-----\nMIIE", "k [redacted-secret]"},
		{"env assign", "GITHUB_TOKEN=hunter2 go test", "GITHUB_TOKEN=[redacted-secret] go test"},
		{"env assign quoted", `export API_KEY="a b c"`, `export API_KEY=[redacted-secret]`},
		{"env lower", "db_password=s3cret", "db_password=[redacted-secret]"},
		{"apikey", "APIKEY=zzz", "APIKEY=[redacted-secret]"},
		{"private_key", "PRIVATE_KEY=zzz", "PRIVATE_KEY=[redacted-secret]"},
		{"access_key", "aws_access_key=zzz", "aws_access_key=[redacted-secret]"},
		{"passwd", "passwd=zzz", "passwd=[redacted-secret]"},
		{"secret", "CLIENT_SECRET=zzz", "CLIENT_SECRET=[redacted-secret]"},
		{"flag value", "tool --password hunter2 --verbose", "tool --password [redacted-secret] --verbose"},
		{"flag quoted value", `tool --api-key "a b" run`, `tool --api-key [redacted-secret] run`},
		{"json pair", `{"password": "hunter2", "user": "bob"}`, `{"password": "[redacted-secret]", "user": "bob"}`},
		{"json escaped quote", `{"client_secret":"a\"b"}`, `{"client_secret":"[redacted-secret]"}`},
		{"json upper", `{"Access_Key": "k"}`, `{"Access_Key": "[redacted-secret]"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SecretsString(tt.in))
		})
	}
}

// TestSecrets_Untouched pins strings the sweep must leave alone: variable
// references, already-redacted values, and ordinary prose and code.
func TestSecrets_Untouched(t *testing.T) {
	for _, in := range []string{
		"GH_TOKEN=$GH_TOKEN go test",
		"GH_TOKEN=$(pass show gh)",
		"TOKEN=[redacted-secret]",
		"the bearer of bad news",
		"task-force-planning-document-title",
		"risk-assessment-for-the-whole-system",
		"AKIA is a prefix",
		"desk-status-and-data-standard",
		"see feedback-desk-status-and-data-standard.md",
		"--password --verbose",
		"password policy is documented",
		"/etc/hosts internal/redact/redact.go",
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
	} {
		t.Run(in, func(t *testing.T) {
			assert.Equal(t, in, SecretsString(in))
		})
	}
}

// TestSecrets_KnownFalsePositives pins shapes that are not credentials but
// are redacted anyway. The patterns are not narrowed to dodge them: a name
// that contains "token" or "key" is the signal the pattern exists for, and
// an exemption that keeps one legible also lets a real credential through.
func TestSecrets_KnownFalsePositives(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"max_tokens setting", "max_tokens=4096", "max_tokens=[redacted-secret]"},
		{"token-file flag names a path", "run --token-file ./t.txt", "run --token-file [redacted-secret]"},
		{"long kebab word after sk-", "--sk-some-very-long-option-name-here", "--[redacted-secret]"},
		{"bearer followed by a long word", "Bearer authentication-scheme-description", "Bearer [redacted-secret]"},
		{"secret manager json key", `{"secret_manager": "vault"}`, `{"secret_manager": "[redacted-secret]"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SecretsString(tt.in))
		})
	}
}

func TestSecrets_Idempotent(t *testing.T) {
	in := "a " + ghToken + " TOKEN=x " + pemBlock + ` {"password":"p"} ` + awsKeyID
	once := SecretsString(in)
	assert.Equal(t, once, SecretsString(once))
	assert.NotContains(t, once, ghToken)
}

func TestSecretsValue(t *testing.T) {
	in := map[string]any{
		"command": "GITHUB_TOKEN=abc go run .",
		"nested":  []any{"fine", "key " + awsKeyID},
		"token":   "plain-value",
		"count":   3,
		"env":     map[string]any{"MY_SECRET": "v", "PATH": "/bin"},
	}
	out, changed := SecretsValue(in)
	require.True(t, changed)
	m := out.(map[string]any)
	assert.Equal(t, "GITHUB_TOKEN=[redacted-secret] go run .", m["command"])
	assert.Equal(t, []any{"fine", "key [redacted-secret]"}, m["nested"])
	assert.Equal(t, SecretToken, m["token"], "a secret-named key redacts its string value")
	assert.Equal(t, 3, m["count"])
	assert.Equal(t, map[string]any{"MY_SECRET": SecretToken, "PATH": "/bin"}, m["env"])
	assert.Equal(t, "GITHUB_TOKEN=abc go run .", in["command"], "input must not be mutated")

	clean := map[string]any{"file_path": "/x/y.go", "n": 1}
	same, changed := SecretsValue(clean)
	assert.False(t, changed)
	assert.Equal(t, clean, same)
}

// TestEmailPattern_AddressShapedNonAddresses is the shared pattern's pin.
// The audit sweep and the mission writers use one definition, so the
// shapes it over-matches are recorded once, here.
func TestEmailPattern_AddressShapedNonAddresses(t *testing.T) {
	assert.Equal(t, EmailToken, EmailPattern.ReplaceAllString("git@github.com", EmailToken))
	assert.Equal(t, "golang.org/[redacted-email]-amd64/f.go",
		EmailPattern.ReplaceAllString("golang.org/toolchain@v0.0.1-go1.26.5.linux-amd64/f.go", EmailToken))
	assert.Equal(t, "gopkg.in/yaml.v3@v3.0.1",
		EmailPattern.ReplaceAllString("gopkg.in/yaml.v3@v3.0.1", EmailToken))
}

func TestProse(t *testing.T) {
	in := "mail jane.doe@example.com with GITHUB_TOKEN=abc from /home/alice/work and /Users/bob/x"
	want := "mail [redacted-email] with GITHUB_TOKEN=[redacted-secret] from ~/work and ~/x"
	assert.Equal(t, want, Prose(in))
	assert.Equal(t, want, Prose(Prose(in)))
}

func TestHomePaths_KnownFalsePositive(t *testing.T) {
	// /home/runner is a CI home, not a person's, and is redacted anyway.
	assert.Equal(t, "~/work", Prose("/home/runner/work"))
	assert.Equal(t, "/homework/x", Prose("/homework/x"))
	assert.Equal(t, "/etc/home/x", Prose("/etc/home/x"))
}

type rec struct {
	ID    string `redact:"keep"`
	Note  string
	List  []string
	Sub   sub
	Ptr   *sub
	Any   map[string]any
	Plain map[string]string
	Ifc   any
	Num   int
	priv  string
}

type sub struct {
	Hash string `redact:"keep"`
	Text string
}

func TestRecord_RedactsEveryStringExceptKeep(t *testing.T) {
	tag := "see a@b.org"
	in := rec{
		ID: tag, Note: tag, List: []string{tag, "ok"},
		Sub: sub{Hash: tag, Text: tag}, Ptr: &sub{Hash: tag, Text: tag},
		Any: map[string]any{"k": tag, "n": []any{tag}}, Plain: map[string]string{"k": tag},
		Ifc: tag, Num: 7, priv: tag,
	}
	out := Record(in, func(s string) string { return strings.ReplaceAll(s, "a@b.org", "X") })

	assert.Equal(t, tag, out.ID, "keep-tagged field is untouched")
	assert.Equal(t, "see X", out.Note)
	assert.Equal(t, []string{"see X", "ok"}, out.List)
	assert.Equal(t, sub{Hash: tag, Text: "see X"}, out.Sub)
	assert.Equal(t, &sub{Hash: tag, Text: "see X"}, out.Ptr)
	assert.Equal(t, map[string]any{"k": "see X", "n": []any{"see X"}}, out.Any)
	assert.Equal(t, map[string]string{"k": "see X"}, out.Plain)
	assert.Equal(t, "see X", out.Ifc)
	assert.Equal(t, 7, out.Num)

	// The input is not mutated, including through shared slices, maps
	// and pointers.
	assert.Equal(t, tag, in.Note)
	assert.Equal(t, []string{tag, "ok"}, in.List)
	assert.Equal(t, tag, in.Ptr.Text)
	assert.Equal(t, tag, in.Any["k"])
	assert.Equal(t, tag, in.Plain["k"])
}

func TestRecord_NilStaysNil(t *testing.T) {
	out := Record(rec{}, func(s string) string { return "X" })
	assert.Nil(t, out.List)
	assert.Nil(t, out.Ptr)
	assert.Nil(t, out.Any)
	assert.Nil(t, out.Ifc)
	assert.Equal(t, "X", out.Note)
}

func TestRecord_CredentialNamedMapKeys(t *testing.T) {
	type doc struct {
		Details map[string]any
		Env     map[string]string
	}
	in := doc{
		Details: map[string]any{"password": "hunter2", "api_key": "k", "reason": "fine", "count": 3, "token": ""},
		Env:     map[string]string{"GITHUB_TOKEN": "abc", "PATH": "/bin"},
	}
	out := Record(in, func(s string) string { return s })
	assert.Equal(t, map[string]any{"password": SecretToken, "api_key": SecretToken, "reason": "fine", "count": 3, "token": ""}, out.Details)
	assert.Equal(t, map[string]string{"GITHUB_TOKEN": SecretToken, "PATH": "/bin"}, out.Env)
	assert.Equal(t, "hunter2", in.Details["password"], "input must not be mutated")
}

func TestHomePaths_LeavesAddressesAlone(t *testing.T) {
	assert.Equal(t, "~/x and a@b.org", HomePaths("/home/alice/x and a@b.org"))
	assert.Equal(t, "see ~ here", HomePaths("see /Users/jf... here"))
}

func TestRecordPaths_PathTaggedFieldsGetOnlyThePathFunction(t *testing.T) {
	type doc struct {
		Grants []string `redact:"path"`
		Note   string
		ID     string `redact:"keep"`
	}
	in := doc{Grants: []string{"x"}, Note: "x", ID: "x"}
	out := RecordPaths(in, func(string) string { return "content" }, func(string) string { return "path" })
	assert.Equal(t, doc{Grants: []string{"path"}, Note: "content", ID: "x"}, out)
}

// TestRecordPaths_PathTagReachesNestedFields: a path-tagged field keeps its
// function when it sits inside an untagged struct or a slice of structs.
func TestRecordPaths_PathTagReachesNestedFields(t *testing.T) {
	type inner struct {
		Grants []string `redact:"path"`
		Note   string
	}
	type doc struct {
		Box   inner
		Items []inner
		Ptr   *inner
	}
	in := doc{
		Box:   inner{Grants: []string{"x"}, Note: "x"},
		Items: []inner{{Grants: []string{"x"}, Note: "x"}},
		Ptr:   &inner{Grants: []string{"x"}, Note: "x"},
	}
	out := RecordPaths(in, func(string) string { return "content" }, func(string) string { return "path" })
	want := inner{Grants: []string{"path"}, Note: "content"}
	assert.Equal(t, want, out.Box)
	assert.Equal(t, []inner{want}, out.Items)
	assert.Equal(t, &want, out.Ptr)
}

// TestSecrets_TrailingPunctuationIsNotValue: an unquoted value stops
// before a trailing , ; . or ), so the prose around it survives.
func TestSecrets_TrailingPunctuationIsNotValue(t *testing.T) {
	cases := []struct{ in, want string }{
		{"token_count: 3, export", "token_count: " + SecretToken + ", export"},
		{"PASSWORD=hunter2.", "PASSWORD=" + SecretToken + "."},
		{"set PASSWORD=hunter2; run", "set PASSWORD=" + SecretToken + "; run"},
		{"(API_KEY=abc123)", "(API_KEY=" + SecretToken + ")"},
		{"--token abc123, then", "--token " + SecretToken + ", then"},
		{"--password hunter2.", "--password " + SecretToken + "."},
		{"password: a.b,c", "password: " + SecretToken},
		{"PASSWORD=hunter2", "PASSWORD=" + SecretToken},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, SecretsString(c.in), c.in)
	}
}

// TestSecrets_BareValueKeepsTrailingPunctuation pins a deliberate
// trade-off: an unquoted value stops before a final ) , ; or . so prose
// such as "(API_KEY=abc)" keeps its shape. A real value that itself ends in
// one of those characters therefore keeps that last character in the output.
// Leaking one punctuation character is accepted over mangling the sentence.
func TestSecrets_BareValueKeepsTrailingPunctuation(t *testing.T) {
	for _, end := range []string{")", ",", ";", "."} {
		assert.Equal(t, "password="+SecretToken+end, SecretsString("password=hunter2"+end), end)
	}
}

func TestHomePathsValue(t *testing.T) {
	in := map[string]any{"a": "/home/al/x", "l": []any{"/Users/bo", 3}, "n": 7}
	out := HomePathsValue(in)
	assert.Equal(t, map[string]any{"a": "~/x", "l": []any{"~", 3}, "n": 7}, out)
	assert.Equal(t, "/home/al/x", in["a"], "input is not mutated")
}

func TestPathDiffs(t *testing.T) {
	type inner struct {
		Grants []string `redact:"path"`
		Note   string
	}
	type doc struct {
		Items []inner
		ID    string `redact:"keep"`
	}
	a := doc{Items: []inner{{Grants: []string{"x", "y"}, Note: "n"}}, ID: "1"}
	b := doc{Items: []inner{{Grants: []string{"x", "z"}, Note: "other"}}, ID: "2"}
	assert.Equal(t, []string{"Items[0].Grants[1]"}, PathDiffs(a, b),
		"only path-tagged values are compared")
	assert.Empty(t, PathDiffs(a, a))
}
