package vendor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The lint's whole design is the EXCLUDE → BLOCK → WARN order. The cases
// that pin it are the ones where a name matches two lists at once.
func TestClassify(t *testing.T) {
	tests := []struct {
		key  string
		want Verdict
		why  string
	}{
		// EXCLUDE wins over WARN. gpg_key_id is a published binding; if
		// the WARN token "key" decided it, every GPG-bound identity would
		// trip the lint and users would learn to ignore it.
		{"gpg_key_id", Clean, "public reference beats the ambiguous 'key'"},
		{"pubkey", Clean, "explicitly public"},
		{"public_key", Clean, "explicitly public"},
		{"key_fingerprint", Clean, "a fingerprint is publishable"},

		// The real roster, verified secret-free — these must stay quiet or
		// the guard blocks every existing vendor run.
		{"memory_collection", Clean, ""},
		{"session_context", Clean, ""},
		{"imap_server", Clean, ""},
		{"voice_id", Clean, ""},
		{"provider", Clean, ""},
		{"tty", Clean, ""},

		// BLOCK: the name says secret.
		{"api_token", Block, "the 'token' token"},
		{"password", Block, ""},
		{"imap_password", Block, ""},
		{"client_secret", Block, ""},
		{"private_key", Block, ""},
		{"auth_header", Block, ""},
		{"credentials", Block, ""},
		// Blocked by adjacency, not by a bare token: "key" alone is
		// ambiguous, "api key" is not.
		{"api_key", Block, "the api+key pair"},
		{"access_key", Block, "the access+key pair"},

		// WARN: ambiguous. Reported, never blocking — a guard that fires
		// on voice_key would be turned off.
		{"voice_key", Warn, ""},
		{"salt", Warn, ""},
		{"db_dsn", Warn, ""},
		{"random_seed", Warn, ""},

		{"", Clean, "no tokens, nothing to say"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			assert.Equal(t, tt.want, Classify(tt.key), tt.why)
		})
	}
}

func TestClassifyIsCaseInsensitive(t *testing.T) {
	assert.Equal(t, Block, Classify("API_TOKEN"))
	assert.Equal(t, Clean, Classify("GPG_KEY_ID"))
}

func TestParseAllowExtKeys(t *testing.T) {
	tests := []struct {
		name    string
		refs    []string
		wantErr bool
	}{
		{name: "well formed", refs: []string{"quarry/api_token", "beadle/password"}},
		{name: "whitespace tolerated", refs: []string{"  quarry/api_token  "}},
		// A malformed override must not be silently dropped: the user
		// would believe they granted an exemption they did not.
		{name: "dot instead of slash", refs: []string{"quarry.api_token"}, wantErr: true},
		{name: "no key", refs: []string{"quarry/"}, wantErr: true},
		{name: "no namespace", refs: []string{"/api_token"}, wantErr: true},
		{name: "empty", refs: []string{""}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set, err := parseAllowExtKeys(tt.refs)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "--allow-ext-key")
				return
			}
			require.NoError(t, err)
			assert.True(t, set.allows(Finding{Namespace: "quarry", Key: "api_token"}))
			assert.False(t, set.allows(Finding{Namespace: "quarry", Key: "other"}))
		})
	}
}

// The refusal must name every blocked key at once and hand back the
// exact flag for each, so one round of decisions settles the run.
func TestBlockErrorNamesEveryKeyAndTheFlag(t *testing.T) {
	err := blockError([]Finding{
		{Handle: "zoe", Namespace: "beadle", Key: "password"},
		{Handle: "mal", Namespace: "quarry", Key: "api_token"},
	})
	msg := err.Error()

	assert.Contains(t, msg, "mal quarry/api_token")
	assert.Contains(t, msg, "zoe beadle/password")
	assert.Contains(t, msg, "--allow-ext-key quarry/api_token")
	assert.Contains(t, msg, "--local")
	assert.Less(t, indexOf(msg, "mal quarry"), indexOf(msg, "zoe beadle"), "sorted by handle")
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
