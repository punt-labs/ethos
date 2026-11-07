package provenance

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/mission"
)

func TestProvenanceSummaryPresenceIndexAndCommit(t *testing.T) {
	for _, tc := range []struct {
		name           string
		open           bool
		present        bool
		listed         bool
		digestMismatch bool
		wantErr        string
	}{
		{
			name: "open explicitly inventoried absent row with empty digest", open: true, listed: true,
			wantErr: "selected summary missing or altered: " + missionA,
		},
		{
			name: "open without summary inventory entry", open: true,
		},
		{
			name: "open present row with matching digest", open: true, present: true, listed: true,
		},
		{
			name: "open present row with mismatching digest", open: true, present: true, listed: true, digestMismatch: true,
			wantErr: "selected summary missing or altered: " + missionA,
		},
		{
			name:    "terminal missing required summary without inventory entry",
			wantErr: "terminal mission missing summary: " + missionA,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, base := repositoryTest(t)
			r := p.Repository
			snapshot := selectedTest(t)
			if tc.open {
				for i := range snapshot.Records {
					record := &snapshot.Records[i]
					if record.Path != ".punt-labs/ethos/missions/"+missionA+"/contract.yaml" {
						continue
					}
					c, err := mission.DecodeContractStrict(record.Data, missionA)
					require.NoError(t, err)
					c.Status = mission.StatusOpen
					c.ClosedAt = ""
					record.Data = yamlTest(t, c)
					rows, _, err := SummaryRows(snapshot.Summary)
					require.NoError(t, err)
					row, err := json.Marshal(mission.TraceSummary{ID: missionA, Status: c.Status, RoundsUsed: c.CurrentRound})
					require.NoError(t, err)
					snapshot.Summary = bytes.Join([][]byte{row, rows[missionB], nil}, []byte{'\n'})
				}
			}
			receipt, err := p.Publish(t.Context(), snapshot, base)
			require.NoError(t, err)
			require.NoError(t, r.VerifyIndex(t.Context(), []string{receipt.Manifest}))
			m, err := r.LoadManifest(t.Context(), ":", receipt.Manifest)
			require.NoError(t, err)
			if !tc.present {
				data, err := r.record(t.Context(), ":", SummaryPath)
				require.NoError(t, err)
				rows, _, err := SummaryRows(data)
				require.NoError(t, err)
				// Keep the summary file and the other mission's valid row. Only
				// the explicitly selected mission's row is absent.
				require.NoError(t, r.stageBytes(t.Context(), SummaryPath, append(rows[missionB], '\n')))
			}
			var summaries []Summary
			for _, row := range m.Summaries {
				if row.Mission == missionA {
					if !tc.listed {
						continue
					}
					if !tc.present || tc.digestMismatch {
						row.SHA256 = Digest(nil)
					}
				}
				summaries = append(summaries, row)
			}
			m.Summaries = summaries
			name, data, err := Encode(*m)
			require.NoError(t, err)
			require.NoError(t, r.stageBytes(t.Context(), name, data))
			_, err = r.LoadManifest(t.Context(), ":", name)
			require.NoError(t, err, "the content-addressed manifest itself must decode")

			_, before := indexBytesTest(t, r)
			err = r.VerifyIndex(t.Context(), []string{name})
			if tc.wantErr == "" {
				assert.NoError(t, err, "index verification")
			} else {
				assert.ErrorContains(t, err, tc.wantErr, "index verification")
			}
			_, after := indexBytesTest(t, r)
			assert.Equal(t, before, after, "verification must not rewrite the index")

			gitTest(t, r, "commit", "-m", "snapshot\n\nEthos-Provenance: "+name)
			_, err = r.VerifyCommit(t.Context(), "HEAD")
			if tc.wantErr == "" {
				assert.NoError(t, err, "commit verification")
			} else {
				assert.ErrorContains(t, err, tc.wantErr, "commit verification")
			}
		})
	}
}
