package seed

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlace_DecisionMatrix drives the per-file decision across every cell of
// the design's table: {absent, ==cur, !=cur&&==mf, !=cur&&!=mf, zero-byte} ×
// {manifest entry present, absent}, plus the force override. Each case sets up
// disk and manifest state, runs place, and asserts the action and the
// resulting manifest entry.
func TestPlace_DecisionMatrix(t *testing.T) {
	const shipped = "shipped content\n"
	cur := hashBytes([]byte(shipped))

	// action names which Result bucket a case lands in.
	type action int
	const (
		deployed action = iota
		updated
		unchanged
		skipped
		edited
		repaired
	)

	cases := []struct {
		name string
		// onDisk is the file's content before place; "" means absent.
		onDisk string
		// tracked records a manifest entry with entryHash before place.
		tracked   bool
		entryHash string
		force     bool
		want      action
		// wantContent is what the file must hold after place.
		wantContent string
		// wantRecorded is whether a manifest entry must exist after place,
		// and if so what hash it must carry.
		wantRecorded bool
		wantHash     string
	}{
		{
			name: "absent deploys and records",
			want: deployed, wantContent: shipped,
			wantRecorded: true, wantHash: cur,
		},
		{
			name:   "matches current is unchanged and adopted",
			onDisk: shipped, want: unchanged, wantContent: shipped,
			wantRecorded: true, wantHash: cur,
		},
		{
			name:   "matches current with a stale entry refreshes to cur",
			onDisk: shipped, tracked: true, entryHash: hashBytes([]byte("stale\n")),
			want: unchanged, wantContent: shipped,
			wantRecorded: true, wantHash: cur,
		},
		{
			name:   "tracked and unmodified upgrades",
			onDisk: "old shipped\n", tracked: true, entryHash: hashBytes([]byte("old shipped\n")),
			want: updated, wantContent: shipped,
			wantRecorded: true, wantHash: cur,
		},
		{
			name:   "tracked and edited is preserved",
			onDisk: "user edit\n", tracked: true, entryHash: hashBytes([]byte("old shipped\n")),
			want: edited, wantContent: "user edit\n",
			wantRecorded: true, wantHash: hashBytes([]byte("old shipped\n")),
		},
		{
			name:   "untracked and differing keeps no-clobber skip",
			onDisk: "stale\n", want: skipped, wantContent: "stale\n",
			wantRecorded: false,
		},
		{
			name:   "tracked edited is overwritten under force",
			onDisk: "user edit\n", tracked: true, entryHash: hashBytes([]byte("old shipped\n")),
			force: true, want: updated, wantContent: shipped,
			wantRecorded: true, wantHash: cur,
		},
		{
			name:   "untracked differing is overwritten and recorded under force",
			onDisk: "stale\n", force: true, want: updated, wantContent: shipped,
			wantRecorded: true, wantHash: cur,
		},
		{
			name:   "zero-byte is repaired regardless of manifest",
			onDisk: "", tracked: false, want: repaired, wantContent: shipped,
			wantRecorded: true, wantHash: cur,
			// onDisk "" is created as an empty file below (not absent).
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dest := t.TempDir()
			s := testSeeder(dest, "", tc.force)
			path := filepath.Join(dest, "roles", "sample.yaml")
			key := s.key(scopeEthos, path)

			// Distinguish "absent" from "empty file": the zero-byte case
			// writes an empty file; the deploy case leaves it absent.
			if tc.name == "zero-byte is repaired regardless of manifest" {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, []byte{}, 0o644))
			} else if tc.onDisk != "" {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, []byte(tc.onDisk), 0o644))
			}
			if tc.tracked {
				s.mf.Entries[key] = Entry{Scope: scopeEthos, Hash: tc.entryHash}
			}

			s.place(scopeEthos, path, []byte(shipped))
			require.Empty(t, s.r.Errors, "errors: %v", s.r.Errors)

			switch tc.want {
			case deployed:
				assert.Contains(t, s.r.Deployed, path)
			case updated:
				assert.Contains(t, s.r.Updated, path)
			case unchanged:
				assert.Contains(t, s.r.Unchanged, path)
			case skipped:
				assert.Contains(t, s.r.Skipped, path)
			case edited:
				assert.Contains(t, s.r.Edited, path)
			case repaired:
				assert.Contains(t, s.r.Repaired, path)
			}

			got, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, tc.wantContent, string(got))

			entry, ok := s.mf.Entries[key]
			assert.Equal(t, tc.wantRecorded, ok, "manifest entry presence")
			if tc.wantRecorded {
				assert.Equal(t, tc.wantHash, entry.Hash)
			}
		})
	}
}
