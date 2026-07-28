package identity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The manifest is what makes an omitted extension detectable: a repo
// with no `<handle>.ext/` might be an identity with no extensions, or an
// identity whose extensions vendor forgot. Without that record the store
// reports nothing rather than guessing.
func TestMissingExt(t *testing.T) {
	tests := []struct {
		name              string
		repoAuthoritative bool
		required          map[string][]string
		writeExt          bool
		wantMissing       []string
	}{
		{
			name:              "manifest-recorded file present",
			repoAuthoritative: true,
			required:          map[string][]string{"mal": {"quarry.yaml"}},
			writeExt:          true,
		},
		{
			name:              "manifest-recorded file absent is a miss",
			repoAuthoritative: true,
			required:          map[string][]string{"mal": {"quarry.yaml"}},
			wantMissing:       []string{"ext/mal/quarry"},
		},
		{
			name:              "no manifest means unverifiable, not missing",
			repoAuthoritative: true,
		},
		{
			name:     "layered mode never reports an ext miss",
			required: map[string][]string{"mal": {"quarry.yaml"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, global := NewStore(t.TempDir()), NewStore(t.TempDir())
			ls := NewLayeredStoreWithBundle(repo, nil, global, tt.repoAuthoritative)
			ls.WithRequiredExt(tt.required)

			require.NoError(t, repo.Save(&Identity{Name: "Mal", Handle: "mal", Kind: "human"}))
			if tt.writeExt {
				require.NoError(t, repo.ExtSet("mal", "quarry", "memory_collection", "mem"))
			}

			id, err := ls.Load("mal")
			// Live Load is additive on extensions: a missing namespace
			// records a verdict, it never bricks a running session.
			require.NoError(t, err)

			var got []string
			for _, m := range id.MissingExt {
				got = append(got, m.String())
			}
			assert.Equal(t, tt.wantMissing, got)
		})
	}
}

func TestMissingExtNamesThePath(t *testing.T) {
	repo, global := NewStore(t.TempDir()), NewStore(t.TempDir())
	ls := NewLayeredStoreWithBundle(repo, nil, global, true)
	ls.WithRequiredExt(map[string][]string{"mal": {"quarry.yaml"}})
	require.NoError(t, repo.Save(&Identity{Name: "Mal", Handle: "mal", Kind: "human"}))

	id, err := ls.Load("mal")
	require.NoError(t, err)
	require.Len(t, id.MissingExt, 1)
	assert.Equal(t, repo.extPath("mal", "quarry"), id.MissingExt[0].Path)
}
