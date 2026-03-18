package identity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate_ValidHuman(t *testing.T) {
	id := &Identity{
		Name:   "Jim Freeman",
		Handle: "jfreeman",
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
