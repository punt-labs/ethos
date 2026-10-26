package redact

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSecretsValue_CredentialKeyIsReplacedWhole: a key the credential pass
// changes at all is replaced whole, never partly redacted, so it keeps no
// fragment of the credential and a second pass does not change it again.
func TestSecretsValue_CredentialKeyIsReplacedWhole(t *testing.T) {
	cases := map[string]struct{ key, fragment string }{
		"authorization": {"Authorization: Bearer ghp_abcdefghijklmnopqrstuvwxyz0123456789", "ghp_"},
		"assignment":    {"token=abc def", "abc"},
		"userinfo":      {"https://user:hunter2@example.com/x", "hunter2"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			out, changed := SecretsValue(map[string]any{c.key: "y"})
			assert.True(t, changed)
			// The value may go too (a key that reads as a credential name takes
			// its value with it); the key is what is asserted.
			keys := []string{}
			for k := range out.(map[string]any) {
				keys = append(keys, k)
				assert.NotContains(t, k, c.fragment)
			}
			assert.Equal(t, []string{SecretToken}, keys)
			again, changed := SecretsValue(out)
			assert.False(t, changed, "a second pass changes nothing")
			assert.Equal(t, out, again)
		})
	}
	t.Run("collision", func(t *testing.T) {
		out, _ := SecretsValue(map[string]any{"token=a": 1, "token=b": 2, SecretToken: 3})
		m := out.(map[string]any)
		assert.Len(t, m, 3, "no two keys merge")
		assert.Contains(t, m, SecretToken)
		assert.Contains(t, m, SecretToken+"#2")
	})
}

// TestRecord_CredentialKeyIsReplacedWhole: Record applies the whole-key rule
// of SecretsValue to the map keys of a record, whatever f does to them.
func TestRecord_CredentialKeyIsReplacedWhole(t *testing.T) {
	type rec struct{ M map[string]string }
	same := func(s string) string { return s }
	for _, key := range []string{
		"Authorization: Bearer ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"token=abc def",
		"https://user:hunter2@example.com/x",
	} {
		for name, f := range map[string]func(string) string{"identity": same, "prose": Prose} {
			t.Run(name+"/"+key[:5], func(t *testing.T) {
				out := Record(rec{M: map[string]string{key: "y"}}, f)
				keys := []string{}
				for k := range out.M {
					keys = append(keys, k)
				}
				assert.Equal(t, []string{SecretToken}, keys)
				assert.Equal(t, out, Record(out, f), "a second pass changes nothing")
			})
		}
	}
	t.Run("collision", func(t *testing.T) {
		out := Record(rec{M: map[string]string{"token=a": "1", "token=b": "2"}}, Prose)
		assert.Len(t, out.M, 2, "no two keys merge")
		assert.Contains(t, out.M, SecretToken)
		assert.Contains(t, out.M, SecretToken+"#2")
	})
}
