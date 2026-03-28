package hook

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildExtensionContext(t *testing.T) {
	tests := []struct {
		name string
		ext  map[string]map[string]string
		want string
	}{
		{
			name: "nil ext",
			ext:  nil,
			want: "",
		},
		{
			name: "empty ext map",
			ext:  map[string]map[string]string{},
			want: "",
		},
		{
			name: "one namespace with session_context",
			ext: map[string]map[string]string{
				"quarry": {"session_context": "You have memory via quarry."},
			},
			want: "You have memory via quarry.",
		},
		{
			name: "multiple namespaces sorted",
			ext: map[string]map[string]string{
				"zebra":  {"session_context": "zebra context"},
				"alpha":  {"session_context": "alpha context"},
				"middle": {"session_context": "middle context"},
			},
			want: "alpha context\n\nmiddle context\n\nzebra context",
		},
		{
			name: "namespace without session_context key",
			ext: map[string]map[string]string{
				"beadle": {"email": "test@example.com"},
			},
			want: "",
		},
		{
			name: "mixed namespaces",
			ext: map[string]map[string]string{
				"beadle": {"email": "test@example.com"},
				"quarry": {"session_context": "quarry active", "memory_collection": "mem-x"},
				"vox":    {"voice": "alloy"},
			},
			want: "quarry active",
		},
		{
			name: "session_context with trailing newlines trimmed",
			ext: map[string]map[string]string{
				"quarry": {"session_context": "some context\n\n\n"},
			},
			want: "some context",
		},
		{
			name: "session_context empty string after trim",
			ext: map[string]map[string]string{
				"quarry": {"session_context": "\n\n"},
			},
			want: "",
		},
		{
			name: "session_context empty string",
			ext: map[string]map[string]string{
				"quarry": {"session_context": ""},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildExtensionContext(tt.ext)
			assert.Equal(t, tt.want, got)
		})
	}
}
