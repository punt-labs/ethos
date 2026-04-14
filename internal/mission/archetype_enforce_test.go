package mission

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnforceWriteSetConstraints(t *testing.T) {
	tests := []struct {
		name        string
		writeSet    []string
		constraints []string
		wantErr     string // substring; "" means no error
	}{
		{
			name:        "no constraints passes",
			writeSet:    []string{"foo.go"},
			constraints: nil,
		},
		{
			name:        "empty write_set passes",
			writeSet:    nil,
			constraints: []string{"*_test.go"},
		},
		{
			name:        "file matches glob",
			writeSet:    []string{"internal/mission/store_test.go"},
			constraints: []string{"*_test.go"},
		},
		{
			name:        "file matches one of several globs",
			writeSet:    []string{"docs/architecture.md"},
			constraints: []string{"*_test.go", "*.md"},
		},
		{
			name:        "file matches dir/** prefix",
			writeSet:    []string{"docs/deep/nested.md"},
			constraints: []string{"docs/**"},
		},
		{
			name:        "file matches dir/** exact",
			writeSet:    []string{"testdata/input.json"},
			constraints: []string{"testdata/**"},
		},
		{
			name:        "directory envelope exempt",
			writeSet:    []string{"internal/mission/"},
			constraints: []string{"*_test.go"},
		},
		{
			name:        "mixed directory and file",
			writeSet:    []string{"internal/mission/", "store_test.go"},
			constraints: []string{"*_test.go"},
		},
		{
			name:        "file does not match any constraint",
			writeSet:    []string{"internal/mission/store.go"},
			constraints: []string{"*_test.go", "testdata/**", "docs/**", "*.md"},
			wantErr:     `write_set entry "internal/mission/store.go" does not match any constraint`,
		},
		{
			name:        "one match one mismatch",
			writeSet:    []string{"store_test.go", "store.go"},
			constraints: []string{"*_test.go"},
			wantErr:     `write_set entry "store.go" does not match any constraint`,
		},
		{
			name:        "malformed pattern surfaces error",
			writeSet:    []string{"store.go"},
			constraints: []string{"[invalid"},
			wantErr:     `invalid constraint pattern "[invalid"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Contract{WriteSet: tc.writeSet}
			a := &Archetype{WriteSetConstraints: tc.constraints}
			err := enforceWriteSetConstraints(c, a)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestEnforceRequiredFields(t *testing.T) {
	tests := []struct {
		name     string
		fields   []string
		contract Contract
		wantErr  string
	}{
		{
			name:   "no required fields passes",
			fields: nil,
		},
		{
			name:     "context required and present",
			fields:   []string{"context"},
			contract: Contract{Context: "Some context"},
		},
		{
			name:    "context required and empty",
			fields:  []string{"context"},
			wantErr: `required field "context" is empty`,
		},
		{
			name:     "context required and whitespace only",
			fields:   []string{"context"},
			contract: Contract{Context: "   "},
			wantErr:  `required field "context" is empty`,
		},
		{
			name:     "inputs.files required and present",
			fields:   []string{"inputs.files"},
			contract: Contract{Inputs: Inputs{Files: []string{"a.go"}}},
		},
		{
			name:    "inputs.files required and empty",
			fields:  []string{"inputs.files"},
			wantErr: `required field "inputs.files" is empty`,
		},
		{
			name:     "inputs.ticket required and present",
			fields:   []string{"inputs.ticket"},
			contract: Contract{Inputs: Inputs{Ticket: "ethos-123"}},
		},
		{
			name:    "inputs.ticket required and empty",
			fields:  []string{"inputs.ticket"},
			wantErr: `required field "inputs.ticket" is empty`,
		},
		{
			name:     "success_criteria required and present",
			fields:   []string{"success_criteria"},
			contract: Contract{SuccessCriteria: []string{"pass"}},
		},
		{
			name:    "success_criteria required and empty",
			fields:  []string{"success_criteria"},
			wantErr: `required field "success_criteria" is empty`,
		},
		{
			name:    "unknown field rejected",
			fields:  []string{"nonexistent"},
			wantErr: `unknown required field "nonexistent"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := &Archetype{RequiredFields: tc.fields}
			err := enforceRequiredFields(&tc.contract, a)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateWithArchetype_AllowEmptyWriteSet(t *testing.T) {
	tests := []struct {
		name      string
		archetype *Archetype
		writeSet  []string
		wantErr   string
	}{
		{
			name:      "nil archetype rejects empty write_set",
			archetype: nil,
			writeSet:  nil,
			wantErr:   "write_set must contain at least one entry",
		},
		{
			name:      "AllowEmptyWriteSet=false rejects empty",
			archetype: &Archetype{AllowEmptyWriteSet: false},
			writeSet:  nil,
			wantErr:   "write_set must contain at least one entry",
		},
		{
			name:      "AllowEmptyWriteSet=true allows empty",
			archetype: &Archetype{AllowEmptyWriteSet: true},
			writeSet:  nil,
		},
		{
			name:      "AllowEmptyWriteSet=true with non-empty still passes",
			archetype: &Archetype{AllowEmptyWriteSet: true},
			writeSet:  []string{"internal/mission/"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := validContract()
			c.WriteSet = tc.writeSet
			err := c.ValidateWithArchetype(tc.archetype)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
