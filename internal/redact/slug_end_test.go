package redact

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSlugEnd_ProseIsNotASlug: ordinary text with a dash, a word and
// users-list or home-page in it is not a home-directory slug. The slug-end rule
// does not read across white space, quotes or slashes.
func TestSlugEnd_ProseIsNotASlug(t *testing.T) {
	for _, s := range []string{
		"- add the get-users-list endpoint",
		"- fix the user-home-page view",
		"-v --home-dir x",
		"-- see list-users-api for details",
		"nav--home-page.scss", "btn--users-list.css",
	} {
		assert.Empty(t, LeakRule(s, "/home/zed"), "%q", s)
	}
}

// TestSlugEnd_DashEncodedSlugEndingAtTheName: a slug that ends the component.
// Known cost: --home-x, a component that is nothing but two dashes and a word
// named home or users, has the shape of a slug and is a match.
func TestSlugEnd_DashEncodedSlugEndingAtTheName(t *testing.T) {
	for _, s := range []string{
		"C--Users-al", "-var-home-bob/f.jsonl", "x/-home-bob",
		"~/.claude/projects/-var-home-bob/log.jsonl", "a/b--home-page.scss", "--home-x",
	} {
		assert.NotEmpty(t, LeakRule(s, "/home/zed"), "%q", s)
	}
}

// TestSlugEnd_InsideRule: inside the checkout a
// one-letter block with a file extension (a/b--home-page.scss) is a file; a
// component that is dash-encoded, or a drive-letter slug with a dotless name,
// is still a slug.
func TestSlugEnd_InsideRule(t *testing.T) {
	for _, s := range []string{"a/b--home-page.scss", "nav--home-page.scss", "app/home/page.tsx", "- add the get-users-list endpoint"} {
		assert.Empty(t, InsideRule(s, "/home/zed"), "%q", s)
	}
	for _, s := range []string{"x/-home-bob", "-var-home-bob/f.jsonl", "-home-bob-x/f", "src/C--Users-al", "src/a--home-bob", "<repo>/c--users-bob/f"} {
		assert.NotEmpty(t, InsideRule(s, "/home/zed"), "%q", s)
	}
}

// TestSlugEnd_HomeRelativeRule: under ~/ a file with a one-letter block is a
// file; a drive-letter slug with a dotless name and a dash-start component are
// slugs.
func TestSlugEnd_HomeRelativeRule(t *testing.T) {
	assert.Empty(t, HomeRelativeRule("~/x-wt/a/b--home-page.scss", "/home/zed"))
	assert.Empty(t, HomeRelativeRule("~/x-wt/app/home/page.tsx", "/home/zed"))
	assert.NotEmpty(t, HomeRelativeRule("~/.claude/projects/C--Users-al", "/home/zed"))
	assert.NotEmpty(t, HomeRelativeRule("~/.claude/projects/-var-home-bob/log.jsonl", "/home/zed"))
}
