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
		{
			name: "ssh single segment path",
			url:  "git@github.com:punt-labs",
			want: "",
		},
		{
			name: "https single segment path",
			url:  "https://github.com/punt-labs",
			want: "",
		},
		{
			name: "ssh scheme with .git",
			url:  "ssh://git@github.com/punt-labs/ethos.git",
			want: "punt-labs/ethos",
		},
		{
			name: "ssh scheme without .git",
			url:  "ssh://git@github.com/punt-labs/ethos",
			want: "punt-labs/ethos",
		},
		{
			name: "https too many segments",
			url:  "https://github.com/org/name/extra.git",
			want: "",
		},
		{
			name: "ssh scp too many segments",
			url:  "git@github.com:org/name/extra.git",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseGitRemote(tt.url)
			assert.Equal(t, tt.want, got)
		})
	}
}
