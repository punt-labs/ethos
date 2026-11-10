package enable

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const legacyBlockLF = legacyGitignoreMarker + "\n" +
	".punt-labs/**/local/**\n" +
	".punt-labs/ethos/missions/**/*.lock\n"

func TestInsertLocalRuleLineEndings(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty file", "", LocalIgnoreNote + "\n" + LocalIgnoreRule + "\n"},
		{"LF without a final newline", "dist/", "dist/\n\n" + LocalIgnoreNote + "\n" + LocalIgnoreRule + "\n"},
		{"CRLF with a final newline", "dist/\r\n", "dist/\r\n\r\n" + LocalIgnoreNote + "\r\n" + LocalIgnoreRule + "\r\n"},
		{"CRLF without a final newline", "a\r\ndist/", "a\r\ndist/\r\n\r\n" + LocalIgnoreNote + "\r\n" + LocalIgnoreRule + "\r\n"},
		{"under an existing CRLF marker", "x\r\n" + gitignoreMarker + "\r\n", "x\r\n" + gitignoreMarker + "\r\n" + LocalIgnoreRule + "\r\n"},
		{"under a marker with no final newline", gitignoreMarker, gitignoreMarker + "\n" + LocalIgnoreRule + "\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, string(insertLocalRule([]byte(c.in))))
		})
	}
}

func TestRemoveLegacyManagedRulesLineEndings(t *testing.T) {
	cases := []struct {
		name, in, want string
		retired        int
	}{
		{"LF", legacyBlockLF, gitignoreMarker + "\n", 2},
		{"LF without a final newline", legacyGitignoreMarker + "\n.punt-labs/**/local/**", gitignoreMarker + "\n", 1},
		{
			"CRLF keeps its endings",
			"a\r\n" + legacyGitignoreMarker + "\r\n.punt-labs/**/local/**\r\n.punt-labs/**/*.local.*\r\nb\r\n",
			"a\r\n" + gitignoreMarker + "\r\nb\r\n", 2,
		},
		{
			"CRLF without a final newline",
			legacyGitignoreMarker + "\r\n.punt-labs/**/local/**",
			gitignoreMarker + "\r\n", 1,
		},
		{
			"a user line after the block ends it",
			legacyBlockLF + "dist/\n.punt-labs/**/local/**\n",
			gitignoreMarker + "\ndist/\n.punt-labs/**/local/**\n", 2,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, retired := removeLegacyManagedRules([]byte(c.in))
			assert.Equal(t, c.want, string(got))
			assert.Len(t, retired, c.retired)
		})
	}
}
