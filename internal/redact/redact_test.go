package redact

import (
	"fmt"
	"strings"
	"testing"
	"time"

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
	want := "mail [redacted-email] with GITHUB_TOKEN=[redacted-secret] from ~[redacted-user]/work and ~[redacted-user]/x"
	assert.Equal(t, want, Prose(in))
	assert.Equal(t, want, Prose(Prose(in)))
}

func TestHomePaths_KnownFalsePositive(t *testing.T) {
	// /home/runner is a CI home, not a person's, and is redacted anyway.
	assert.Equal(t, "~[redacted-user]/work", Prose("/home/runner/work"))
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
	assert.Equal(t, "~[redacted-user]/x and a@b.org", HomePaths("/home/alice/x and a@b.org"))
	assert.Equal(t, "see ~[redacted-user] here", HomePaths("see /Users/jf... here"))
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
	assert.Equal(t, map[string]any{"a": "~[redacted-user]/x", "l": []any{"~[redacted-user]", 3}, "n": 7}, out)
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

func TestLeaks(t *testing.T) {
	tests := []struct {
		name string
		home string
		in   string
		want bool
	}{
		{"clean", "/var/home/zed", "internal/mission/store.go", false},
		{"placeholders", "/var/home/zed", "~/x <repo>/y ~[redacted-user]/z -~-Coding", false},
		{"home string", "/var/home/zed", "cd /var/home/zed/x", true},
		{"home glued to a flag", "/var/home/zed", "gcc -I/var/home/zed/inc", true},
		{"any home dir", "/var/home/zed", "/home/bob/x", true},
		{"macOS home", "/var/home/zed", "/Users/bob/x", true},
		{"triple slash", "/var/home/zed", "///home/bob/x", true},
		{"scheme", "/var/home/zed", "file:///home/bob/x", true},
		{"glued to a flag", "/var/home/zed", "-I/home/bob/inc", true},
		{"upper case", "/home/zed", "/HOME/Bob/x", true},
		{"dot segment", "/home/zed", "/home/./bob/x", true},
		{"slash run inside", "/home/zed", "/home//bob/x", true},
		{"percent encoded", "/home/zed", "x=%2Fhome%2Fbob%2Fx", true},
		{"json escaped", "/home/zed", `"\/home\/bob"`, true},
		{"unicode name", "/home/zed", "/home/b\u00f3b/x", true},
		{"zero width name", "/home/zed", "/home/\u200b/x", true},
		{"own home case folded", "/Users/zed", "/users/ZED/x", true},
		{"dotdot before a slash", "/home/zed", "cat /tmp/../home/bob/.ssh/config", true},
		// Limitation, pinned: a whitespace-only component followed by .. is not
		// normalised, so these are not caught. Collapsing across whitespace
		// would redact ordinary shell text (see TestLeaks_ShellTextIsNotCollapsed).
		{"limitation: tab component", "/home/zed", "cd /home/\t/../jfreeman/x", false},
		{"limitation: newline component", "/home/zed", "/home/\n/../bob/x", false},
		{"limitation: space component", "/home/zed", "/home/ /../bob/x", false},
		{"own home behind a space component", "/home/zed", "/a /../home/zed", true},
		{"leading dotdot", "/home/zed", "cd ../../home/bob", true},
		{"home after a resolved climb", "/home/alice", "/home/alice/../", true},
		{"tilde climbs out", "/home/zed", "cd ~/../bob", true},
		{"repo climbs out", "/home/zed", "<repo>/../x", true},
		{"slug", "/var/home/zed", "-home-bob-proj", true},
		{"foreign slug in a name", "/var/home/zed", "x-home-bob-proj", true},
		{"foreign slug after a backtick", "/var/home/zed", "`-home-bob-p`", true},
		{"own dash form after a name byte", "/home/zed", `a\n-home-zed-proj`, true},
		{"own dash form after a flag", "/home/zed", "see -home-zed-proj", true},
		{"mid-word tilde", "/home/zed", "x~/home/zed/secret", true},
		{"home dir after the home placeholder", "/home/zed", "~/home/bob/.ssh/id", true},
		{"own home after repo", "/var/lib/jenkins", "<repo>/var/lib/jenkins/x", true},
		{"runner home", "/home/runner", "/home/runner/x", true},
		{"system dash home", "/var/lib/jenkins", "see -var-lib-jenkins-proj", true},
		{"system home string", "/var/lib/jenkins", "cd /var/lib/jenkins/x", true},
		{"no home known", "", "/home/bob", true},

		// Accepted false refusals: the rules know nothing of where a path
		// begins, so these are refused (and replaced in audit lines).
		{"web app route", "/home/zed", "app/home/page.tsx", true},
		{"controller dir", "/home/zed", "src/Controllers/Users/Index.cs", true},
		{"url path", "/home/zed", "https://example.com/home/docs", true},
		{"branch name", "/var/home/zed", "fix-home-page-layout", true},
		{"relative home dir", "/var/home/zed", "testdata/home/bob/x", true},
		{"repo-top home dir", "/var/home/zed", "<repo>/home/bob/x", true},
		{"relative dot route", "/home/zed", "edit ./home/page.tsx", true},
		{"root: a component of the home name", "/root", "cmd/root/x", true},

		{"root: flag with the name", "/root", "--rootdir=.", false},
		{"root: a longer component", "/root", "/rootfs/x", false},
		{"root: upper case longer", "/root", "/ROOTFS", false},
		{"root: its own tree", "/root", "/root/x", true},
		{"root: the dash form", "/root", "-root-proj-", true},
		{"root: the dash form at the end", "/root", "x -root", true},
		{"root: the home itself", "/root", "see /root", true},
		{"slug without a tail is not one", "/var/home/zed", "my-home-page", false},
		{"user as a segment is not a home", "/var/home/zed", "a/zed/b", false},
		{"short user is not searched", "/var/home/zed", "a/ab/b", false},
		{"runner dir is not a home", "/home/runner", "/work/repo/internal/runner/x.go", false},
		{"runner write set", "/home/runner", "internal/runner/", false},
		{"unrelated under a system home", "/var/lib/jenkins", "var/lib/other", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Leaks(tt.in, tt.home))
		})
	}
}

// TestOwnHomeRule_JudgesARepoRelativeValue: rules 1 and 2 only, so a write set
// naming app/home/page.tsx works while a value holding the home does not.
func TestOwnHomeRule_JudgesARepoRelativeValue(t *testing.T) {
	assert.Empty(t, OwnHomeRule("app/home/page.tsx", "/home/zed"))
	assert.Empty(t, OwnHomeRule("src/Users/Index.cs", "/home/zed"))
	assert.NotEmpty(t, OwnHomeRule("app/home/zed/x", "/home/zed"))
	assert.NotEmpty(t, OwnHomeRule("see -home-zed-p", "/home/zed"))
	for in, want := range map[string]bool{
		"app/home/page.tsx": true, "internal/": true, "a/b.go": true,
		"": false, "/abs/x": false, "~/x": false, "<repo>/x": false, "a/../b": false, "../b": false, `a\b`: false,
		"a/b/": true, "x/*.go": true, "a+b@c/d": true, "a//b": false, "a/%68ome": false, "a b": false,
		"Read(/.//home/bob/**)": false, "Read(%2Fhome%2Fbob/**)": false, "Read(/%68ome/bob)": false,
		"file:%2F%2F%2Fhome%2Fbob": false, "Read(%2Dhome-bob-proj)": false, " /home/bob/x": false,
		"x=/home/bob/x": false, "a:/home/bob/x": false, "Read(app/home/x)": false,
	} {
		assert.Equal(t, want, RepoRelative(in), in)
	}
}

func TestRecord_MapKeysArePassed(t *testing.T) {
	type doc struct {
		Any   map[string]any
		Plain map[string]string
	}
	in := doc{
		Any:   map[string]any{"a@b.org": 1, "ok": 2},
		Plain: map[string]string{"a@b.org": "v"},
	}
	out := Record(in, func(s string) string { return strings.ReplaceAll(s, "a@b.org", "X") })
	assert.Equal(t, map[string]any{"X": 1, "ok": 2}, out.Any)
	assert.Equal(t, map[string]string{"X": "v"}, out.Plain)
	_, still := in.Any["a@b.org"]
	assert.True(t, still, "input must not be mutated")
}

func TestFieldStrings_IncludesMapKeys(t *testing.T) {
	type doc struct{ M map[string]string }
	var got []string
	for _, fs := range FieldStrings(doc{M: map[string]string{"/home/bob": "v"}}) {
		got = append(got, fs.Value)
	}
	assert.ElementsMatch(t, []string{"/home/bob", "v"}, got)
}

func TestHomePathsValue_Keys(t *testing.T) {
	got := HomePathsValue(map[string]any{"/home/bob/a": "v"})
	assert.Equal(t, map[string]any{"~[redacted-user]/a": "v"}, got)
}

// TestHomePaths_Idempotent: a second pass changes nothing. It never turns its
// own ~ into ~~ or a repo's top-level home directory into <repo>~.
func TestHomePaths_Idempotent(t *testing.T) {
	for _, in := range []string{
		"/home/0/home/0", "cat <repo>/home/bob/x", "~/home/bob", "x~/home/zed/secret",
		"~[redacted-user]/home/bob", "/HOME/Bob/x", "/home/./bob/x", "/home//bob/x", "/home/bób/x",
		"-home-bob-proj", "a /home/bob/x b /Users/al/y",
	} {
		once := HomePaths(in)
		assert.Equal(t, once, HomePaths(once), in)
	}
	assert.Equal(t, "cat <repo>/home/bob/x", HomePaths("cat <repo>/home/bob/x"))
	assert.Equal(t, "~[redacted-user]/x", HomePaths("/home/./bob/x"))
	assert.Equal(t, "~[redacted-user]/x", HomePaths("/HOME/Bob/x"))
}

// TestHomePaths_Rewrites: what the prose rewriter places. It is best effort:
// the writers' leak check is the authority, and it refuses what is left.
func TestHomePaths_Rewrites(t *testing.T) {
	tests := []struct{ in, want string }{
		{"-I/home/bob/inc", "[redacted-path]/inc"},
		{"gcc -I/home/bob/inc", "gcc [redacted-path]/inc"},
		{"git -C /home/bob/repo", "git -C ~[redacted-user]/repo"},
		{"edit ./home/page.tsx", "edit ./home/page.tsx"},
		{"testdata/../home/alice/x", "testdata/../home/alice/x"},
		{"cd ~/../bob", "cd [redacted-path]"},
		{"/home/me/../bob/x", "[redacted-path]/x"},
		{"cd ../../home/bob", "cd [redacted-path]"},
		{"cat /tmp/../home/bob/x", "cat [redacted-path]/x"},
		{"~/home/bob", "[redacted-path]"},
		{"x~/home/zed/secret", "x[redacted-path]/secret"},
		{"app/home/page.tsx", "app/home/page.tsx"},
		{"<repo>/home/bob/x", "<repo>/home/bob/x"},
	}
	for _, tt := range tests {
		got := HomePaths(tt.in)
		assert.Equal(t, tt.want, got, tt.in)
		assert.Equal(t, got, HomePaths(got), "idempotent: "+tt.in)
	}
}

// seeds the monotone and agreement properties run over.
var leakSeeds = []string{
	"/home/alice/../", "cat /home/bob/../../etc/hosts", "/home/alice/../../tmp", "/Users/bob/../../etc",
	"/var/lib/jenkins/../", "/home/zed/x", "-I/home/bob/inc", "../../home/bob", "/tmp/../home/bob/x",
	"-home-bob-proj", "a=-home-bob-x", "/w/-home-bob-p/x", "fix-home-page-layout", "x-home-alice-proj",
	`\/home\/bob\/x`, "~/home/bob", "x~/home/zed/y", "/home/0/home/0", "file:///home/bob/x",
	"/HOME/Bob/x", "/home//bob/x", "/home/./bob/x", "<repo>/home/bob/x", "https://example.com/home/docs",
	"--rootdir=.", "/rootfs/x", "/root/x", "-root-proj-",
	"cd ~/../bob", "/home/me/../bob/x", "git -C /home/bob/repo", "edit ./home/page.tsx", "testdata/../home/alice/x",
	"cmd/root/x", "--root-dir x", "pre-root-x", "/x/../home/zed/y",
}

var leakHomes = []string{"", "/home/alice", "/home/zed", "/var/lib/jenkins", "/root", "/Users/bob"}

// TestLeakRule_NormalizationNeverErasesEvidence: a rule that matches the
// string as written, or its lower-cased form, matches in LeakRule too:
// normalizing only adds spellings to check, it never replaces the original.
func TestLeakRule_NormalizationNeverErasesEvidence(t *testing.T) {
	for _, h := range leakHomes {
		for _, s := range leakSeeds {
			if leakRuleOf(s, h, true) != "" || leakRuleOf(strings.ToLower(s), strings.ToLower(h), true) != "" {
				assert.NotEmpty(t, LeakRule(s, h), "home %q input %q", h, s)
			}
		}
	}
	assert.NotEmpty(t, LeakRule("/home/alice/../", "/home/alice"))
	assert.NotEmpty(t, LeakRule("cat /home/bob/../../etc/hosts", "/home/zed"))
	assert.NotEmpty(t, LeakRule("/var/lib/jenkins/../", "/var/lib/jenkins"))
}

// TestHomePaths_IdempotentOverTheSeeds: a second pass changes nothing.
func TestHomePaths_IdempotentOverTheSeeds(t *testing.T) {
	for _, s := range leakSeeds {
		got := HomePaths(s)
		assert.Equal(t, got, HomePaths(got), "idempotent: "+s)
	}
	assert.Equal(t, "-~-[redacted-slug]", HomePaths("-home-bob-proj"))
	assert.Equal(t, `~[redacted-user]\/x`, HomePaths(`\/home\/bob\/x`))
}

// FuzzHomePathsIdempotent: the rewriter is idempotent.
func FuzzHomePathsIdempotent(f *testing.F) {
	for _, s := range leakSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := HomePaths(s)
		if HomePaths(got) != got {
			t.Fatalf("not idempotent: %q -> %q -> %q", s, got, HomePaths(got))
		}
	})
}

// TestHomePaths_EscapedSlashesOutsideTheMatchStayEscaped: a JSON-escaped home
// is rewritten, and only that span: other \/ in the value are left as written.
func TestHomePaths_EscapedSlashesOutsideTheMatchStayEscaped(t *testing.T) {
	in := `a\/b \/home\/bob\/x c\/d`
	got := HomePaths(in)
	assert.Equal(t, `a\/b ~[redacted-user]\/x c\/d`, got)
	assert.Equal(t, got, HomePaths(got))
	assert.Empty(t, LeakRule(got, ""))
	assert.Equal(t, `x\/y`, HomePaths(`x\/y`))
}

// TestRenameKeys: clean keys keep their names, renamed keys follow in sorted
// order of their originals and take the first free #N, no two keys merge, and
// a rename is reported.
func TestRenameKeys(t *testing.T) {
	rename := func(k string) string { return strings.ReplaceAll(k, "BAD", "~") }
	m := map[string]any{"BAD/x": "LEAKY", "~/x": "CLEAN", "BAD/y": 1, "z": 2}
	for i := 0; i < 200; i++ {
		out, renamed := RenameKeys(m, rename, false, func(_ string, v any) any { return v })
		assert.True(t, renamed)
		assert.Equal(t, map[string]any{"~/x": "CLEAN", "~/x#2": "LEAKY", "~/y": 1, "z": 2}, out)
	}
	out, renamed := RenameKeys(map[string]any{"a": 1, "b": 2}, func(k string) string { return k }, false, func(_ string, v any) any { return v })
	assert.False(t, renamed)
	assert.Len(t, out, 2)

	// At the top level of an audit line a name is taken under EqualFold.
	out, _ = RenameKeys(map[string]any{"[REDACTED-KEY]": 1, "BAD": 2}, func(k string) string {
		if k == "BAD" {
			return "[redacted-key]"
		}
		return k
	}, true, func(_ string, v any) any { return v })
	assert.Equal(t, map[string]any{"[REDACTED-KEY]": 1, "[redacted-key]#2": 2}, out)
}

// TestHomePathsValue_KeysNeverMerge: three foreign homes that rewrite to ~
// keep all their values, and the result does not depend on map order.
func TestHomePathsValue_KeysNeverMerge(t *testing.T) {
	in := map[string]any{"/home/bob/x": "BOB", "/home/alice/x": "ALICE", "/Users/carol/x": "CAROL"}
	first, renamed := HomePathsValueFlag(in)
	assert.True(t, renamed)
	vals := map[any]bool{}
	for _, v := range first.(map[string]any) {
		vals[v] = true
	}
	assert.Equal(t, map[any]bool{"BOB": true, "ALICE": true, "CAROL": true}, vals)
	for i := 0; i < 200; i++ {
		again, _ := HomePathsValueFlag(in)
		assert.Equal(t, first, again)
	}
}

// TestRecord_MapKeysNeverMerge: a reflected map keeps both values when two keys
// pass to one name.
func TestRecord_MapKeysNeverMerge(t *testing.T) {
	type doc struct{ M map[string]string }
	out := Record(doc{M: map[string]string{"a@b.org": "1", "X": "2"}}, func(s string) string { return strings.ReplaceAll(s, "a@b.org", "X") })
	assert.Equal(t, map[string]string{"X": "2", "X#2": "1"}, out.M)
}

// TestDuplicateJSONKey: the first exact duplicate at any depth, by path.
func TestDuplicateJSONKey(t *testing.T) {
	tests := []struct{ in, path, key string }{
		{`{"a":1,"a":2}`, "a", "a"},
		{`{"x":{"k":1,"k":2}}`, "x.k", "k"},
		{`{"x":[{"k":1},{"k":1,"k":2}]}`, "x[1].k", "k"},
		{`[{"a":1,"a":2}]`, "[0].a", "a"},
		{`{"ab":1,"ab":2}`, "ab", "ab"},
		{`{"":1,"":2}`, "", ""},
		{`{"a":{"b":1},"c":{"b":2}}`, "", ""},
		{`{"a":1,"A":2}`, "", ""},
	}
	for _, tt := range tests {
		p, k, ok := DuplicateJSONKey([]byte(tt.in))
		wantOK := tt.path != "" || tt.key != "" || strings.Contains(tt.in, `"":1,"":2`)
		assert.Equal(t, wantOK, ok, tt.in)
		assert.Equal(t, tt.path, p, tt.in)
		assert.Equal(t, tt.key, k, tt.in)
	}
}

// TestPlanKeys_IsLinear: thousands of keys that rename to one name finish
// quickly and each gets its own name.
func TestPlanKeys_IsLinear(t *testing.T) {
	keys := make([]string, 5000)
	for i := range keys {
		keys[i] = fmt.Sprintf("bad%04d", i)
	}
	start := time.Now()
	names, renamed := PlanKeys(keys, func(string) string { return "x" }, true)
	assert.True(t, renamed)
	assert.Less(t, time.Since(start), 200*time.Millisecond)
	seen := map[string]bool{}
	for _, n := range names {
		assert.False(t, seen[strings.ToLower(n)], n)
		seen[strings.ToLower(n)] = true
	}
	assert.Len(t, seen, 5000)
	assert.Equal(t, "x", names["bad0000"])
	assert.Equal(t, "x#2", names["bad0001"])
}

// TestRecordPathsFlag_ReportsARename: a rename in a reflected map is reported.
func TestRecordPathsFlag_ReportsARename(t *testing.T) {
	type doc struct{ M map[string]string }
	f := func(s string) string { return strings.ReplaceAll(s, "BAD", "ok") }
	_, renamed := RecordPathsFlag(doc{M: map[string]string{"BADk": "1"}}, f, f)
	assert.True(t, renamed)
	_, renamed = RecordPathsFlag(doc{M: map[string]string{"k": "1"}}, f, f)
	assert.False(t, renamed)
}

// TestIsSecretKey_PlaceholdersAreNotSecretKeys: the names a pass gives a
// renamed key hold the word secret, and a second pass must not treat them as
// credential names or it would replace their values.
func TestIsSecretKey_PlaceholdersAreNotSecretKeys(t *testing.T) {
	for _, k := range []string{"[redacted-secret]", "[redacted-secret]#2", "[redacted-key]", "[redacted-key]#12"} {
		if IsSecretKey(k) {
			t.Errorf("IsSecretKey(%q) = true", k)
		}
	}
	for _, k := range []string{"api_key", "my-secret", "[redacted-secret]x", "x[redacted-secret]"} {
		if !IsSecretKey(k) {
			t.Errorf("IsSecretKey(%q) = false", k)
		}
	}
}

// TestSecretsValue_PlaceholderKeyIsAFixedPoint: a second pass over a map
// whose key is the placeholder changes nothing.
func TestSecretsValue_PlaceholderKeyIsAFixedPoint(t *testing.T) {
	in := map[string]any{"[redacted-secret]": "v", "[redacted-secret]#2": "w"}
	out, changed := SecretsValue(in)
	if changed {
		t.Errorf("SecretsValue changed %v to %v", in, out)
	}
}

// TestRecordPathsStatus_SuffixOnlyOnACollision: a key that is renamed without
// taking a suffix is renamed only; a rename onto a taken name takes name#N.
func TestRecordPathsStatus_SuffixOnlyOnACollision(t *testing.T) {
	type rec struct{ M map[string]any }
	up := func(s string) string { return strings.ReplaceAll(s, "bad", "ok") }
	_, st := RecordPathsStatus(rec{M: map[string]any{"bad": 1}}, up, up)
	if !st.Renamed || st.Suffixed {
		t.Errorf("plain rename: %+v", st)
	}
	_, st = RecordPathsStatus(rec{M: map[string]any{"bad": 1, "ok": 2}}, up, up)
	if !st.Renamed || !st.Suffixed {
		t.Errorf("collision: %+v", st)
	}
	_, st = RecordPathsStatus(rec{M: map[string]any{"fine": 1}}, up, up)
	if st.Renamed || st.Suffixed {
		t.Errorf("no rename: %+v", st)
	}
}

// TestLeaks_ShellTextIsNotCollapsed: a word and a space before a .. is shell
// text, not a path component, so a command that changes directory is neither
// a leak nor rewritten.
func TestLeaks_ShellTextIsNotCollapsed(t *testing.T) {
	for _, in := range []string{
		"cd <repo>/docs && make; cd ../../..",
		"cat ~/notes.txt; cd ../../..; ls",
	} {
		if got := LeakRule(in, "/home/zed"); got != "" {
			t.Errorf("LeakRule(%q) = %q, want none", in, got)
		}
		if got := HomePaths(in); got != in {
			t.Errorf("HomePaths(%q) = %q, want it unchanged", in, got)
		}
	}
}

// TestLeakRuleText_NeverMatchesALeakPattern: the description of every rule is
// written into records and notices, so it must pass LeakRule itself; a rule
// text that names a leak would make a record of a record, withheld again on
// every run.
func TestLeakRuleText_NeverMatchesALeakPattern(t *testing.T) {
	const home = "/home/zed"
	for name, sample := range map[string]string{
		"own home":  "cat /home/zed/x",
		"own slug":  "-home-zed-proj",
		"path":      "cat /home/bob/x",
		"slug":      "-home-bob-proj",
		"climb out": "cd ~/../bob",
	} {
		rule := LeakRule(sample, home)
		if rule == "" {
			t.Errorf("%s: %q is not a leak", name, sample)
			continue
		}
		if again := LeakRule(rule, home); again != "" {
			t.Errorf("%s: the rule text %q is itself a leak (%q)", name, rule, again)
		}
	}
}
