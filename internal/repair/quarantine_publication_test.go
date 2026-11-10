//go:build linux || darwin

package repair

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestQuarantineDiagnosticsRetainMissionPublications(t *testing.T) {
	for _, tc := range []struct {
		name, canonical, checkout, exportSuffix string
	}{
		{name: "canonical current", canonical: "round_decisions: {version: 1, entries: []}\n"},
		{name: "checkout current", checkout: "round_decisions: {version: 1, entries: []}\n"},
		{name: "current outranks legacy", canonical: "round_decisions: {version: 1, entries: []}\n", checkout: "mission_id: m-one\n"},
		{name: "missing authority is ambiguous"},
		{name: "null decisions are ambiguous", canonical: "round_decisions: null\n"},
		{name: "flat export outranks legacy", canonical: "mission_id: m-one\n", exportSuffix: ".contract.yaml"},
		{name: "incomplete export outranks legacy", canonical: "mission_id: m-one\n", exportSuffix: ".result.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, checkout := fixture(t), fixture(t)
			p, err := layout.NewRepository(filepath.Dir(filepath.Dir(store.RecordRoot())), filepath.Dir(filepath.Dir(checkout.RecordRoot())), layout.RecordBlame)
			require.NoError(t, err)
			if tc.canonical != "" {
				put(t, filepath.Join(p.RecordRoot(), "missions", "m-one", "contract.yaml"), tc.canonical)
			}
			if tc.checkout != "" {
				put(t, filepath.Join(p.CheckoutRecordRoot(), "missions", "m-one", "contract.yaml"), tc.checkout)
			}
			if tc.exportSuffix != "" {
				for _, root := range []string{p.RecordRoot(), p.CheckoutRecordRoot()} {
					put(t, filepath.Join(root, "missions", "m-one"+tc.exportSuffix), "export record\n")
				}
			}
			cn := audit.ChunkName{Namespace: audit.MissionNS, Session: "sid", First: 100, Last: 200}
			marker, err := audit.MarshalMarker(audit.Marker{Chunk: cn.Stem(), VerifiedLast: 200, UnrecoveredFirst: 100, UnrecoveredLast: 200, Reason: "shared gap"})
			require.NoError(t, err)
			retained := map[string]string{}
			for _, root := range []string{p.RecordRoot(), p.CheckoutRecordRoot()} {
				dir := filepath.Join(root, "missions", "m-one")
				for name, body := range map[string]string{
					cn.MarkerFile():                               string(marker),
					audit.CorruptName(cn.Stem()):                  "damaged published diagnostic\n",
					audit.CorruptHashName(cn.Stem(), "abcdef", 1): "{\"withheld\":\"raw bytes retained locally\"}\n",
					(audit.ChunkName{Namespace: audit.MissionNS, Session: "sid", First: 300, Last: 400}).MarkerFile(): "{\"chunk\":",
				} {
					path := filepath.Join(dir, name)
					put(t, path, body)
					retained[path] = body
				}
			}
			obsolete := filepath.Join(p.RecordRoot(), "missions", "m-obsolete", "log.jsonl")
			put(t, obsolete, "obsolete history\n")
			put(t, filepath.Join(filepath.Dir(obsolete), "contract.yaml"), "mission_id: m-obsolete\n")
			plan, err := Inventory(context.Background(), p)
			require.NoError(t, err)
			var candidates []string
			for _, item := range plan.Items {
				candidates = append(candidates, item.Path)
			}
			assert.Equal(t, []string{obsolete}, candidates)
			removed, err := plan.Apply(context.Background())
			require.NoError(t, err)
			assert.Equal(t, []string{obsolete}, removed)
			for path, before := range retained {
				after, err := os.ReadFile(path)
				require.NoError(t, err, path)
				assert.Equal(t, before, string(after), path)
			}
		})
	}
}

func TestQuarantineDiagnosticsRetainAmbiguousSessions(t *testing.T) {
	p := fixture(t)
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: 100, Last: 200}
	var paths []string
	for _, name := range []string{cn.MarkerFile(), audit.CorruptName(cn.Stem()), audit.CorruptHashName(cn.Stem(), "abcdef", 1)} {
		path := filepath.Join(p.RecordRoot(), "sessions", "2026-01-01-sid", name)
		put(t, path, "malformed shared diagnostic\n")
		paths = append(paths, path)
	}
	plan, err := Inventory(context.Background(), p)
	require.NoError(t, err)
	assert.Empty(t, plan.Items, "session diagnostics have no authority or captured mode proving obsolescence")
	removed, err := plan.Apply(context.Background())
	require.NoError(t, err)
	assert.Empty(t, removed)
	for _, path := range paths {
		b, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "malformed shared diagnostic\n", string(b))
	}
}

func TestQuarantineDiagnosticAuthorityChangeInvalidatesDeletion(t *testing.T) {
	for _, publication := range []string{"current authority", "flat export"} {
		t.Run(publication, func(t *testing.T) {
			p := fixture(t)
			dir := filepath.Join(p.RecordRoot(), "missions", "m-old")
			contract := filepath.Join(dir, "contract.yaml")
			put(t, contract, "mission_id: m-old\n")
			cn := audit.ChunkName{Namespace: audit.MissionNS, Session: "sid", First: 100, Last: 200}
			path := filepath.Join(dir, cn.MarkerFile())
			put(t, path, "obsolete diagnostic\n")
			plan, err := Inventory(context.Background(), p)
			require.NoError(t, err)
			require.Len(t, plan.Items, 1)
			if publication == "current authority" {
				put(t, contract, "round_decisions: {version: 1, entries: []}\n")
			} else {
				put(t, filepath.Join(p.RecordRoot(), "missions", "m-old.result.yaml"), "export record\n")
			}
			removed, err := plan.Apply(context.Background())
			require.ErrorContains(t, err, "inventory changed")
			assert.Empty(t, removed)
			assert.FileExists(t, path)
		})
	}
}

func TestQuarantineDiagnosticsRefuseMalformedMissionAuthority(t *testing.T) {
	for _, body := range []string{"[broken", "null\n", "mission_id: m-old\n---\nround_decisions: null\n", "mission_id: m-old\nmission_id: m-other\n"} {
		t.Run(body, func(t *testing.T) {
			p := fixture(t)
			dir := filepath.Join(p.RecordRoot(), "missions", "m-old")
			contract := filepath.Join(dir, "contract.yaml")
			put(t, contract, body)
			cn := audit.ChunkName{Namespace: audit.MissionNS, Session: "sid", First: 100, Last: 200}
			path := filepath.Join(dir, cn.MarkerFile())
			put(t, path, "partial diagnostic\n")
			_, err := Inventory(context.Background(), p)
			require.ErrorContains(t, err, "left in place: "+contract)
			assert.FileExists(t, path)
		})
	}
}
