package vendor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The lint's whole design is the BLOCK → pairs → EXCLUDE → WARN order.
// The cases that pin it are the ones where a name matches two lists at
// once.
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

		// BLOCK outranks EXCLUDE. Every one of these carries an EXCLUDE
		// token, and an EXCLUDE-first pass cleared all of them straight
		// into git — the fail-open this order exists to close.
		{"email_password", Block, "'email' must not clear a password"},
		{"host_password", Block, "'host' must not clear a password"},
		{"username_password", Block, "'username' must not clear a password"},
		{"server_secret", Block, "'server' must not clear a secret"},
		{"provider_token", Block, "'provider' must not clear a token"},
		{"model_apikey", Block, "'model' must not clear an apikey"},
		{"url_credential", Block, "'url' must not clear a credential"},
		{"file_passphrase", Block, "'file' must not clear a passphrase"},
		{"path_private_key", Block, "'path' must not clear a private key"},
		{"id_token", Block, "an OIDC id_token is a bearer credential"},
		{"public_auth", Block, "'public' must not clear an auth value"},

		// BLOCK pairs outrank EXCLUDE too: the vox/quarry shape of an API
		// key leads with the EXCLUDE token "provider" or "model".
		{"provider_api_key", Block, "the api+key pair beats 'provider'"},
		{"model_access_key", Block, "the access+key pair beats 'model'"},
		{"session_key", Block, "the session+key pair"},
		{"signing_key", Block, "the signing+key pair"},
		// The accepted cost of pairs-before-EXCLUDE: a public signing key
		// ID blocks. One --allow-ext-key clears it; the other direction
		// clears provider_api_key into git.
		{"gpg_signing_key_id", Block, "conservative: pair beats the trailing 'id'"},
		{"access_key_id", Block, "conservative: pair beats the trailing 'id'"},

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
	assert.Equal(t, Block, Classify("Email_Password"))
	assert.Equal(t, Clean, Classify("GPG_KEY_ID"))
}

// The structural guarantee, not a spot check: NO combination of an
// EXCLUDE token with a BLOCK token or BLOCK pair may clear. Spot cases
// alone let the fail-open survive — the original suite paired BLOCK with
// EXCLUDE exactly zero times, which is how `email_password` shipped as
// Clean. This test re-derives the matrix from the live lists, so adding
// a token cannot silently reopen the hole.
func TestNoExcludeTokenClearsASecret(t *testing.T) {
	for ex := range excludeTokens {
		for bl := range blockTokens {
			assert.Equal(t, Block, Classify(ex+"_"+bl), "%s_%s must block", ex, bl)
			assert.Equal(t, Block, Classify(bl+"_"+ex), "%s_%s must block", bl, ex)
		}
		for _, p := range blockPairs {
			pair := p[0] + "_" + p[1]
			assert.Equal(t, Block, Classify(ex+"_"+pair), "%s_%s must block", ex, pair)
			assert.Equal(t, Block, Classify(pair+"_"+ex), "%s_%s must block", pair, ex)
		}
	}
}

// The live ext roster, read off the identities this repo vendors. These
// are the names the guard must stay quiet about; a regression here
// blocks every vendor run and gets the guard switched off.
func TestClassifyClearsTheLiveExtRoster(t *testing.T) {
	for _, key := range []string{
		"gpg_key_id", "memory_collection", "session_context",
		"provider", "voice_id",
	} {
		assert.Equal(t, Clean, Classify(key), "%s is public config", key)
	}
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
