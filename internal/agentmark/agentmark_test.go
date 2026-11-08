package agentmark

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"
)

func TestAdd(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"frontmatter", "---\nname: x\n---\nbody\n", "---\n" + Line + "\nname: x\n---\nbody\n"},
		{"already marked", "---\n" + Line + "\nname: x\n---\n", "---\n" + Line + "\nname: x\n---\n"},
		{"crlf", "---\r\nname: x\r\n---\r\nbody\r\n", "---\r\n" + Line + "\r\nname: x\r\n---\r\nbody\r\n"},
		{"no frontmatter", "body\n", "body\n"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Add(tt.in)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, got, Add(got), "Add must be idempotent")
		})
	}
}

func TestHas(t *testing.T) {
	assert.True(t, Has(Add("---\nname: x\n---\n")))
	assert.False(t, Has("---\nname: x\n---\n"))
	assert.False(t, Has("---\nname: x\n"+Line+"\n---\n"), "marker must be the first frontmatter line")
	assert.False(t, Has(""))
	assert.True(t, Has(Add("---\r\nname: x\r\n---\r\n")))
	assert.False(t, Has("---\r\nname: x\r\n---\r\n"))
}

func TestFrontmatterStaysValidYAML(t *testing.T) {
	var m map[string]any
	fm := Add("---\nname: x\n---\n")
	body := fm[len("---\n") : len(fm)-len("---\n")]
	assert.NoError(t, yaml.Unmarshal([]byte(body), &m))
	assert.Equal(t, "x", m["name"])
	assert.Len(t, m, 1, "the marker is a comment, not a key")
}
