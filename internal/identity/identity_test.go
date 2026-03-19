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

func TestValidate_VoiceRequiresProvider(t *testing.T) {
	id := &Identity{
		Name:   "Test",
		Handle: "test",
		Kind:   "human",
		Voice:  &Voice{VoiceID: "abc123"},
	}
	err := id.Validate()
	require.Error(t, err)
	var ve *ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, "voice", ve.Field)
}

func TestValidate_VoiceWithProvider(t *testing.T) {
	id := &Identity{
		Name:   "Test",
		Handle: "test",
		Kind:   "human",
		Voice:  &Voice{Provider: "elevenlabs", VoiceID: "abc123"},
	}
	assert.NoError(t, id.Validate())
}
