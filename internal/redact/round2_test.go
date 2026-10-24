package redact

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSecrets_EscapedQuotes(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"escaped json in shell", `curl -d "{\"password\":\"hunter2\"}" https://x`, `curl -d "{\"password\":\"[redacted-secret]\"}" https://x`},
		{"escaped json with spaces", `{\"api_key\": \"plainvalue123\"}`, `{\"api_key\": \"[redacted-secret]\"}`},
		{"escaped env value", `PASSWORD=\"a b c\"`, `PASSWORD=[redacted-secret]`},
		{"escaped single-quoted env value", `PASSWORD=\'a b c\' go`, `PASSWORD=[redacted-secret] go`},
		{"plain json still works", `{"password":"hunter2"}`, `{"password":"[redacted-secret]"}`},
		{"plain json with escaped inner quote", `{"password": "a\"b"}`, `{"password": "[redacted-secret]"}`},
		{"escaped flag value", `tool --token \"abc def\" run`, `tool --token [redacted-secret] run`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SecretsString(tt.in)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, got, SecretsString(got), "idempotent")
		})
	}
}

func TestSecrets_URLUserinfo(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"postgres", "psql postgres://admin:S3cr3tPw@db:5432/app", "psql postgres://admin:[redacted-secret]@db:5432/app"},
		{"https ip host", "git clone https://alice:hunter2pass@10.0.0.5/repo.git", "git clone https://alice:[redacted-secret]@10.0.0.5/repo.git"},
		{"https tld host and special chars", "https://alice:p!ss#word@example.com/x", "https://alice:[redacted-secret]@example.com/x"},
		{"token user", "https://x-access-token:ghs_" + strings.Repeat("a", 24) + "@github.com/o/r", "https://x-access-token:[redacted-secret]@github.com/o/r"},
		{"plain password", "https://oauth2:plainpassword@gitlab.internal/o/r", "https://oauth2:[redacted-secret]@gitlab.internal/o/r"},
		{"at sign in password", "redis://u:p@ss@cache:6379", "redis://u:[redacted-secret]@cache:6379"},
		{"variable reference kept", "postgres://u:$DB_PASS@host/db", "postgres://u:$DB_PASS@host/db"},
		{"no password kept", "ssh://git@host/repo", "ssh://git@host/repo"},
		{"no userinfo kept", "https://example.com/a:b@c", "https://example.com/a:b@c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SecretsString(tt.in)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, got, SecretsString(got), "idempotent")
		})
	}
	// The email pass must not be what removes the password.
	assert.NotContains(t, Prose("https://alice:p!ss#word@example.com/x"), "p!ss")
}

func TestSecrets_ColonAndSpacedForms(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"yaml", "password: hunter2", "password: [redacted-secret]"},
		{"yaml quoted", `password: "a b c"`, `password: [redacted-secret]`},
		{"aws secret", "aws_secret_access_key: wJalrXUtnFEMI/K7MDENG", "aws_secret_access_key: [redacted-secret]"},
		{"header", "X-Api-Key: abcdef0123456789abcdef", "X-Api-Key: [redacted-secret]"},
		{"header in curl", "curl -H 'X-API-Key: abcdef0123456789abcdef' x", "curl -H 'X-API-Key: [redacted-secret]' x"},
		{"spaced equals", "API_KEY = abc123", "API_KEY = [redacted-secret]"},
		{"spaced equals quoted", `echo "API_KEY = abc123"`, `echo "API_KEY = [redacted-secret]"`},
		{"no space", "token:abc", "token:[redacted-secret]"},
		{"variable reference kept", "api_key: ${API_KEY}", "api_key: ${API_KEY}"},
		{"command substitution kept", "TOKEN=$(pass show x)", "TOKEN=$(pass show x)"},
		{"invalid reference redacted", "PASSWORD=$3cret!", "PASSWORD=[redacted-secret]"},
		{"lone dollar redacted", "PASSWORD=$&x", "PASSWORD=[redacted-secret]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SecretsString(tt.in)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, got, SecretsString(got), "idempotent")
		})
	}
}

// TestSecrets_ColonFormFalsePositives pins what the colon form
// over-matches. The name is the only signal a password has, so a name that
// contains token or secret is redacted whatever follows it.
func TestSecrets_ColonFormFalsePositives(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"max_tokens", "max_tokens: 4096", "max_tokens: [redacted-secret]"},
		{"token_count", "token_count: 3", "token_count: [redacted-secret]"},
		{"secret_manager", "secret_manager: vault", "secret_manager: [redacted-secret]"},
		{"prose", "the password: is documented below", "the password: [redacted-secret] documented below"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SecretsString(tt.in))
		})
	}
	// Not matched: the name is not followed by a colon or equals.
	assert.Equal(t, "PASSWORD=[redacted-secret]", SecretsString(`PASSWORD="abc def`), "an unterminated quote redacts to the line end")
	for _, in := range []string{"password policy", "tokens are cheap", "a secret garden: lovely"} {
		assert.Equal(t, in, SecretsString(in), in)
	}
}

func TestSecrets_PGPBlock(t *testing.T) {
	in := "x\n-----BEGIN " + "PGP PRIVATE KEY BLOCK-----\nlQOYBF\nabc\n-----END " + "PGP PRIVATE KEY BLOCK-----\ny"
	assert.Equal(t, "x\n[redacted-secret]\ny", SecretsString(in))
	assert.Equal(t, "[redacted-secret]", SecretsString("-----BEGIN "+"PGP PRIVATE KEY BLOCK-----\nlQOY"))
	assert.Equal(t, "pub", SecretsString("pub"), "a public key block is not a secret")
	assert.Equal(t, "-----BEGIN PGP PUBLIC KEY BLOCK-----\nabc", SecretsString("-----BEGIN PGP PUBLIC KEY BLOCK-----\nabc"))
}

func TestSecrets_MoreTokenShapes(t *testing.T) {
	a := strings.Repeat("a1", 20)
	tests := []struct{ name, in string }{
		{"stripe live", "sk_" + "live_" + a},
		{"stripe restricted", "rk_live_" + a},
		{"slack bot", "xox" + "b-123456789012-1234567890123-abcdefghijklmnopqrstuvwx"},
		{"slack user", "xox" + "p-123456789012-abcdefghijkl"},
		{"gitlab", "glp" + "at-abcdefghij1234567890"},
		{"google", "AI" + "zaSyA-abcdefghijklmnopqrstuvwxyz12345"},
		{"npm", "npm_" + strings.Repeat("a", 36)},
		{"hugging face", "hf_" + strings.Repeat("a", 34)},
		{"jwt", "eyJhbGciOiJIUzI1NiJ9." + "eyJzdWIiOiIxIn0.abcdefghijklmnopqrstuv"},
		{"github after underscore", "x_ghp_" + strings.Repeat("a", 36)},
		{"github token after underscore in env", "GH=_gho_" + strings.Repeat("b", 36)},
		{"sk after underscore", "x_sk-" + strings.Repeat("a", 24)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SecretsString("use " + tt.in + " here")
			assert.Contains(t, got, SecretToken)
			assert.NotContains(t, got, "abcdefghij")
			assert.NotContains(t, got, a)
			assert.Equal(t, got, SecretsString(got))
		})
	}
	for _, in := range []string{"eyJ is a prefix", "a.b.c", "npm_install", "hf_ok", "AIza"} {
		assert.Equal(t, in, SecretsString(in), in)
	}
}

func TestSecretsValue_NonStringValuesUnderSecretKeys(t *testing.T) {
	in := map[string]any{
		"password":  json.Number("123456"),
		"pin_token": 42,
		"api_key":   []any{"x", "y"},
		"secret":    3.5,
		"flag":      true,
		"nested":    map[string]any{"access_key": []any{}, "n": 1},
		"count":     7,
	}
	out, changed := SecretsValue(in)
	assert.True(t, changed)
	m := out.(map[string]any)
	assert.Equal(t, SecretToken, m["password"])
	assert.Equal(t, SecretToken, m["pin_token"])
	assert.Equal(t, SecretToken, m["api_key"])
	assert.Equal(t, SecretToken, m["secret"])
	assert.Equal(t, true, m["flag"])
	assert.Equal(t, 7, m["count"])
	assert.Equal(t, 1, m["nested"].(map[string]any)["n"])
	assert.Equal(t, []any{"x", "y"}, in["api_key"], "input unchanged")
}

func TestRecord_NonStringValuesUnderSecretKeys(t *testing.T) {
	type doc struct{ Details map[string]any }
	out := Record(doc{Details: map[string]any{"password": 123, "tokens": []any{"a"}, "n": 4}}, func(s string) string { return s })
	assert.Equal(t, map[string]any{"password": SecretToken, "tokens": SecretToken, "n": 4}, out.Details)
}

func TestHomePaths_DashEncodedSlugs(t *testing.T) {
	// The username length is unknown in a slug, so the whole component
	// after -home-/-Users- is replaced, up to a slash, space or quote.
	tests := []struct{ in, want string }{
		{"~/.claude/projects/-home-jfreeman-Coding-punt-labs-ethos/memory/MEMORY.md", "~/.claude/projects/-~-[redacted-slug]/memory/MEMORY.md"},
		{"/x/projects/-Users-alice-work", "/x/projects/-~-[redacted-slug]"},
		{"-home-bob", "-~-[redacted-slug]"},
		{"slug -home-bob-", "slug -~-[redacted-slug]"},
		{"/p/-home-alice-smith-work/x", "/p/-~-[redacted-slug]/x"},
		{"/p/-home-alice.smith-work", "/p/-~-[redacted-slug]"},
		{"in 'p/-Users-a-b-c' ok", "in 'p/-~-[redacted-slug]' ok"},
		// Not slugs: no leading boundary, or a flag.
		{"my-home-page-title", "my-home-page-title"},
		{"--home-dir-x", "--home-dir-x"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, HomePaths(tt.in), tt.in)
		assert.Equal(t, tt.want, HomePaths(HomePaths(tt.in)), "idempotent: "+tt.in)
	}
	assert.Equal(t, "see -~-[redacted-slug] here", HomePaths("see -home-jfreeman here"))
}
