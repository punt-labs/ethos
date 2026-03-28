package hook

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildMemorySection(t *testing.T) {
	tests := []struct {
		name   string
		ext    map[string]map[string]string
		handle string
		want   string // substring to check; empty means expect empty output
		absent string // substring that must NOT appear; empty means no check
	}{
		{
			name:   "nil ext",
			ext:    nil,
			handle: "claude",
			want:   "",
		},
		{
			name:   "empty ext",
			ext:    map[string]map[string]string{},
			handle: "claude",
			want:   "",
		},
		{
			name: "no quarry namespace",
			ext: map[string]map[string]string{
				"beadle": {"email": "claude@punt-labs.com"},
			},
			handle: "claude",
			want:   "",
		},
		{
			name: "quarry with memory_collection only",
			ext: map[string]map[string]string{
				"quarry": {"memory_collection": "memory-claude"},
			},
			handle: "claude",
			want:   "memory-claude",
			absent: "Expertise",
		},
		{
			name: "quarry with memory_collection and expertise_collections",
			ext: map[string]map[string]string{
				"quarry": {
					"memory_collection":      "memory-claude",
					"expertise_collections": "claude-books,claude-blogs",
				},
			},
			handle: "claude",
			want:   "Expertise",
		},
		{
			name: "quarry with empty memory_collection",
			ext: map[string]map[string]string{
				"quarry": {"memory_collection": ""},
			},
			handle: "claude",
			want:   "",
		},
		{
			name: "quarry with expertise_collections but no memory_collection",
			ext: map[string]map[string]string{
				"quarry": {"expertise_collections": "claude-books"},
			},
			handle: "claude",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildMemorySection(tt.ext, tt.handle)
			if tt.want == "" {
				assert.Empty(t, got)
			} else {
				assert.Contains(t, got, tt.want)
			}
			if tt.absent != "" {
				assert.NotContains(t, got, tt.absent)
			}
		})
	}
}

func TestBuildMemorySection_HandleAppearsInOutput(t *testing.T) {
	ext := map[string]map[string]string{
		"quarry": {"memory_collection": "memory-mal"},
	}

	got := BuildMemorySection(ext, "mal")
	assert.Contains(t, got, `agent_handle="mal"`)
	assert.Contains(t, got, "memory-mal")

	// Different handle produces different output.
	got2 := BuildMemorySection(ext, "zoe")
	assert.Contains(t, got2, `agent_handle="zoe"`)
}

func TestBuildMemorySection_ContentStructure(t *testing.T) {
	ext := map[string]map[string]string{
		"quarry": {
			"memory_collection":      "memory-claude",
			"expertise_collections": "claude-books,claude-blogs",
		},
	}

	got := BuildMemorySection(ext, "claude")

	// Top-level heading.
	assert.Contains(t, got, "## Memory")
	// Working memory subsection.
	assert.Contains(t, got, "### Working Memory")
	// Memory types.
	assert.Contains(t, got, "fact:")
	assert.Contains(t, got, "observation:")
	assert.Contains(t, got, "procedure:")
	assert.Contains(t, got, "opinion:")
	// Expertise subsection.
	assert.Contains(t, got, "### Expertise")
	assert.Contains(t, got, "claude-books")
	assert.Contains(t, got, "claude-blogs")
}
