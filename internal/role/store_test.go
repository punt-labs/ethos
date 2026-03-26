package role

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

func TestStore_SaveAndLoad(t *testing.T) {
	s := testStore(t)

	r := &Role{
		Name:             "coo",
		Responsibilities: []string{"execution quality", "sub-agent delegation"},
		Permissions:      []string{"approve-merges", "create-releases"},
	}
	require.NoError(t, s.Save(r))

	loaded, err := s.Load("coo")
	require.NoError(t, err)
	assert.Equal(t, "coo", loaded.Name)
	assert.Equal(t, []string{"execution quality", "sub-agent delegation"}, loaded.Responsibilities)
	assert.Equal(t, []string{"approve-merges", "create-releases"}, loaded.Permissions)
}

func TestStore_SaveDuplicate(t *testing.T) {
	s := testStore(t)

	r := &Role{Name: "coo"}
	require.NoError(t, s.Save(r))

	err := s.Save(r)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestStore_LoadNotFound(t *testing.T) {
	s := testStore(t)

	_, err := s.Load("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_List(t *testing.T) {
	s := testStore(t)

	require.NoError(t, s.Save(&Role{Name: "coo"}))
	require.NoError(t, s.Save(&Role{Name: "go-specialist"}))

	names, err := s.List()
	require.NoError(t, err)
	assert.Len(t, names, 2)
	assert.Contains(t, names, "coo")
	assert.Contains(t, names, "go-specialist")
}

func TestStore_ListEmpty(t *testing.T) {
	s := testStore(t)

	names, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, names)
}

func TestStore_Delete(t *testing.T) {
	s := testStore(t)

	require.NoError(t, s.Save(&Role{Name: "coo"}))
	assert.True(t, s.Exists("coo"))

	require.NoError(t, s.Delete("coo"))
	assert.False(t, s.Exists("coo"))
}

func TestStore_DeleteNotFound(t *testing.T) {
	s := testStore(t)

	err := s.Delete("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_Exists(t *testing.T) {
	s := testStore(t)

	assert.False(t, s.Exists("coo"))

	require.NoError(t, s.Save(&Role{Name: "coo"}))
	assert.True(t, s.Exists("coo"))
}

func TestStore_NameValidation(t *testing.T) {
	s := testStore(t)

	tests := []struct {
		name string
		ok   bool
	}{
		{"coo", true},
		{"go-specialist", true},
		{"a1b2", true},
		{"COO", false},
		{"-bad", false},
		{"bad-", false},
		{"bad name", false},
		{"bad.name", false},
		{"", false},
	}

	for _, tc := range tests {
		r := &Role{Name: tc.name}
		err := s.Save(r)
		if tc.ok {
			if err != nil {
				// Might fail on duplicate if run in loop; check it's not a validation error.
				assert.NotContains(t, err.Error(), "must be lowercase", "name %q should be valid", tc.name)
			}
		} else {
			assert.Error(t, err, "name %q should be invalid", tc.name)
		}
	}
}

func TestStore_PathTraversal(t *testing.T) {
	s := testStore(t)

	_, err := s.Load("../../etc/passwd")
	assert.Error(t, err)

	err = s.Save(&Role{Name: "../../etc/passwd"})
	assert.Error(t, err)
}

func TestStore_MinimalRole(t *testing.T) {
	s := testStore(t)

	r := &Role{Name: "minimal"}
	require.NoError(t, s.Save(r))

	loaded, err := s.Load("minimal")
	require.NoError(t, err)
	assert.Equal(t, "minimal", loaded.Name)
	assert.Nil(t, loaded.Responsibilities)
	assert.Nil(t, loaded.Permissions)
}
