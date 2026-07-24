package identity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate_ValidHuman(t *testing.T) {
	id := &Identity{
		Name:   "Mal Reynolds",
		Handle: "mal",
		Kind:   "human",
	}
	assert.NoError(t, id.Validate())
}

func TestValidate_ValidAgent(t *testing.T) {
	id := &Identity{
		Name:   "Wei",
		Handle: "wei",
		Kind:   "agent",
	}
	assert.NoError(t, id.Validate())
}

func TestValidate_MissingName(t *testing.T) {
	id := &Identity{
		Handle: "test",
		Kind:   "human",
	}
	err := id.Validate()
	require.Error(t, err)
	var ve *ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, "name", ve.Field)
}

func TestValidate_MissingHandle(t *testing.T) {
	id := &Identity{
		Name: "Test",
		Kind: "human",
	}
	err := id.Validate()
	require.Error(t, err)
	var ve *ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, "handle", ve.Field)
}

func TestValidate_InvalidKind(t *testing.T) {
	id := &Identity{
		Name:   "Test",
		Handle: "test",
		Kind:   "robot",
	}
	err := id.Validate()
	require.Error(t, err)
	var ve *ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, "kind", ve.Field)
}

func TestValidate_Email(t *testing.T) {
	tests := []struct {
		name  string
		email string
		valid bool
	}{
		{"empty is valid", "", true},
		{"plain address", "mal@serenity.ship", true},
		{"no at-sign", "not-an-email", false},
		{"internal space", "jim at pobox", false},
		{"trailing space", "mal@serenity.ship ", false},
		{"whitespace only", "   ", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := &Identity{Name: "Test", Handle: "test", Kind: "human", Email: tt.email}
			err := id.Validate()
			if tt.valid {
				assert.NoError(t, err, "email %q should be valid", tt.email)
				return
			}
			require.Error(t, err, "email %q should be invalid", tt.email)
			var ve *ValidationError
			require.ErrorAs(t, err, &ve)
			assert.Equal(t, "email", ve.Field)
			assert.Contains(t, err.Error(), "is not a valid address")
			assert.Contains(t, err.Error(), tt.email, "error must name the bad value")
		})
	}
}

// TestValidate_AgentNoEmail guards the regression: an agent identity carries
// no email and must still validate. Email presence is enforced in setup's
// human path, never in Validate.
func TestValidate_AgentNoEmail(t *testing.T) {
	id := &Identity{Name: "Claude", Handle: "claude", Kind: "agent"}
	assert.NoError(t, id.Validate())
}

func TestValidate_HandleFormat(t *testing.T) {
	tests := []struct {
		handle string
		valid  bool
	}{
		{"mal", true},
		{"wei", true},
		{"agent-1", true},
		{"a", true},
		{"a1b2", true},
		{"my-agent-v2", true},
		{"Jim", false},        // uppercase
		{"-bad", false},       // leading hyphen
		{"bad-", false},       // trailing hyphen
		{"bad handle", false}, // space
		{"bad.handle", false}, // dot
		{"", false},           // empty (caught by required check)
	}

	for _, tt := range tests {
		t.Run(tt.handle, func(t *testing.T) {
			id := &Identity{
				Name:   "Test",
				Handle: tt.handle,
				Kind:   "human",
			}
			err := id.Validate()
			if tt.valid {
				assert.NoError(t, err, "handle %q should be valid", tt.handle)
			} else {
				assert.Error(t, err, "handle %q should be invalid", tt.handle)
			}
		})
	}
}
