package audit

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func gitInit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	require.NoError(t, cmd.Run(), "git %v", args)
}

func TestRepoIdentity(t *testing.T) {
	t.Run("origin resolves to org/name", func(t *testing.T) {
		dir := t.TempDir()
		gitInit(t, dir, "init")
		gitInit(t, dir, "remote", "add", "origin", "git@github.com:punt-labs/ethos.git")
		assert.Equal(t, "punt-labs/ethos", RepoIdentity(dir))
	})

	t.Run("no origin yields empty", func(t *testing.T) {
		dir := t.TempDir()
		gitInit(t, dir, "init")
		assert.Equal(t, "", RepoIdentity(dir))
	})

	t.Run("empty root yields empty", func(t *testing.T) {
		assert.Equal(t, "", RepoIdentity(""))
	})
}
