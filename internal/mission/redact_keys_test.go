package mission

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAfterPlaceholder: a placeholder exempts only a path it begins; a ~ or
// <repo> inside a word is an ordinary character.
func TestAfterPlaceholder(t *testing.T) {
	tests := []struct {
		in   string
		i    int
		want bool
	}{
		{"~/x", 1, true},
		{"cd ~/x", 4, true},
		{"x~/x", 2, false},
		{"a/~/x", 3, true}, // ~ after a slash is a placeholder too: file:///home/me becomes file://~
		{"<repo>/x", 6, true},
		{"y<repo>/x", 7, false},
		{"~[redacted-user]/x", len("~[redacted-user]"), true},
		{"z~[redacted-user]/x", len("z~[redacted-user]"), false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, afterPlaceholder(tt.in, tt.i), tt.in)
	}
}

// TestPathRedactor_Leaks_Boundaries pins the refusals the redactor's own
// check makes: a mid-word ~ hides nothing, a directory named like the account
// is not a home, and a system home is caught in its dash-encoded form.
func TestPathRedactor_Leaks_Boundaries(t *testing.T) {
	tests := []struct {
		home, in string
		want     bool
	}{
		{"/home/zed", "x~/home/zed/secret", true},
		{"/home/zed", "~/home/bob/.ssh/id", true},
		{"/home/runner", "/work/repo/internal/runner/x.go", false},
		{"/home/runner", "internal/runner/", false},
		{"/var/lib/jenkins", "see -var-lib-jenkins-proj", true},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, PathRedactor{Home: tt.home}.Leaks(tt.in), tt.in)
	}
}

// TestRedactRecord_RefusesAWriteSetNamedLikeTheAccount: HOME=/home/runner must
// not make internal/runner/ unwritable.
func TestRedactRecord_WriteSetNamedLikeTheAccount(t *testing.T) {
	t.Setenv("HOME", "/home/runner")
	c := Contract{WriteSet: []string{"internal/runner/"}}
	out, err := RedactRecord("/work/repo", c)
	require.NoError(t, err)
	assert.Equal(t, []string{"internal/runner/"}, out.WriteSet)
}

// TestPathRedactor_ValueRedactsKeys: a map key is redacted as a value is.
func TestPathRedactor_ValueRedactsKeys(t *testing.T) {
	r := PathRedactor{Home: "/home/zed", Repo: "/work/repo"}
	got := r.Map(map[string]any{"/home/zed/a": map[string]any{"/work/repo/b": "v"}})
	assert.Equal(t, map[string]any{"~/a": map[string]any{"<repo>/b": "v"}}, got)
}

// TestRedactYAMLDoc_RedactsKeys: a key the type does not name is data and is
// redacted; one that still names a home refuses the document.
func TestRedactYAMLDoc_RedactsKeys(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	r := PathRedactor{Home: "/home/zed", Repo: "/work/repo"}
	typ := reflect.TypeOf(Contract{})

	out, changed, err := redactYAMLDoc(r.Content, r.Path, typ, []byte("context: ok\nx:\n  /home/zed/a: v\n"))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Contains(t, string(out), "~/a: v")
	assert.NotContains(t, string(out), "zed")

	_, _, err = redactYAMLDoc(r.Content, r.Path, typ, []byte("context: ok\nx:\n  '%2Fhome%2Fbob%2Fproj': v\n"))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "bob")
}

// TestRedactJSONLines_RedactsKeys is the same for a JSONL line.
func TestRedactJSONLines_RedactsKeys(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	r := PathRedactor{Home: "/home/zed", Repo: "/work/repo"}
	typ := reflect.TypeOf(TraceSummary{})

	out, changed, err := redactJSONLines(r.Content, r.Path, typ, []byte(`{"x":{"/home/zed/a":"v"}}`))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.JSONEq(t, `{"x":{"~/a":"v"}}`, string(out))

	_, _, err = redactJSONLines(r.Content, r.Path, typ, []byte(`{"x":{"%2Fhome%2Fbob%2Fproj":"v"}}`))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "bob")
}

// TestRedactYAMLAndJSON_RenamedKeysNeverMerge: two keys the pass renames to
// one name keep both values, in a YAML mapping and a JSON object.
func TestRedactYAMLAndJSON_RenamedKeysNeverMerge(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	r := PathRedactor{Home: "/home/zed", Repo: "/work/repo"}
	out, _, err := redactYAMLDoc(r.Content, r.Path, nil, []byte("x:\n  /home/zed/a: one\n  ~/a: two\n"))
	require.NoError(t, err)
	assert.Contains(t, string(out), "one")
	assert.Contains(t, string(out), "two")
	assert.Contains(t, string(out), "~/a#2")

	out, _, err = redactJSONLines(r.Content, r.Path, nil, []byte(`{"/home/zed/a":"one","~/a":"two"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"~/a#2":"one","~/a":"two"}`, string(out))
}
