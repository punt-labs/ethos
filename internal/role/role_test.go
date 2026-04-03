package role

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateModel(t *testing.T) {
	tests := []struct {
		name  string
		model string
		ok    bool
	}{
		{"empty is valid", "", true},
		{"opus", "opus", true},
		{"sonnet", "sonnet", true},
		{"haiku", "haiku", true},
		{"inherit", "inherit", true},
		{"full opus ID", "claude-opus-4-6", true},
		{"full sonnet ID", "claude-sonnet-4-6", true},
		{"full haiku ID", "claude-haiku-4-5-20251001", true},
		{"unknown model", "gpt-4", false},
		{"random string", "banana", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateModel(tt.model)
			if tt.ok {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "unrecognized model")
			}
		})
	}
}
