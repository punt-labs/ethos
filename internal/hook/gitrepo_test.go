package hook

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseGitRemote(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "ssh with .git",
			url:  "git@github.com:punt-labs/ethos.git",
			want: "punt-labs/ethos",
		},
		{
			name: "ssh without .git",
			url:  "git@github.com:punt-labs/ethos",
			want: "punt-labs/ethos",
		},
		{
			name: "https with .git",
			url:  "https://github.com/punt-labs/ethos.git",
			want: "punt-labs/ethos",
		},
		{
			name: "https without .git",
			url:  "https://github.com/punt-labs/ethos",
			want: "punt-labs/ethos",
		},
		{
			name: "http with .git",
			url:  "http://github.com/punt-labs/ethos.git",
			want: "punt-labs/ethos",
		},
		{
			name: "empty string",
			url:  "",
			want: "",
		},
		{
			name: "whitespace only",
			url:  "   ",
			want: "",
		},
		{
			name: "ssh with trailing whitespace",
			url:  "git@github.com:punt-labs/ethos.git\n",
			want: "punt-labs/ethos",
		},
		{
			name: "https with trailing whitespace",
			url:  "https://github.com/punt-labs/ethos.git\n",
			want: "punt-labs/ethos",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseGitRemote(tt.url)
			assert.Equal(t, tt.want, got)
		})
	}
}
