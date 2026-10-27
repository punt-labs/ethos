package mission

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"gopkg.in/yaml.v3"
)

// writerFiles are the tracked record files the sink writes, one per typed
// record, by the base name that selects the type.
var writerFiles = []string{
	"contract.yaml", "results.yaml", "reflections.yaml", "record.yaml", "m-1.result.yaml",
	"missions.jsonl", "log.jsonl",
}

// writerSeeds are raw files: duplicate keys, invalid UTF-8, surrogate
// escapes, aliases, anchors, directives, unknown fields, leaks, clean records.
func writerSeeds() [][]byte {
	return [][]byte{
		[]byte("mission_id: m-1\nworker: bwk\nwrite_set: [app/home/page.tsx, cmd/]\n"),
		[]byte("mission_id: m-1\ncontext: ok\ncontext: dup\n"),
		[]byte("mission_id: m-1\nwrite_set: &a [x/home/bob]\ncontext: *a\n"),
		[]byte("%TAG !e! tag:example.com,2000:/home/bob/\n---\ncontext: !e!x ok\n"),
		[]byte("mission_id: m-1\ncontext: refuse \\ud800 text\n"),
		[]byte("mission_id: m-1\ncontext: \"a\xffb\"\n"),
		[]byte("mission_id: m-1\nfuture_field: x\n"),
		[]byte("mission_id: m-1\ninputs: {bead: t-1}\n"),
		[]byte("results:\n    - round: 1\n    - {}\n"),
		[]byte("results: []\n"),
		[]byte("- round: 1\n  verdict: pass\n"),
		[]byte("id: d-1\nsession: /home/bob/s\n"),
		[]byte("mission: m-1\nround: 2.50\n"),
		[]byte("mission_id: m-1\n? [a]\n: x\n"),
		[]byte("mission_id: m-1\nk: &k name\n*k : v\n"),
		[]byte(`{"id":"m-1","write_set":["app/home/page.tsx"],"success_criteria":["ok"]}` + "\n"),
		[]byte(`{"id":"m-1","id":"m-2"}` + "\n"),
		[]byte(`{"ts":"t","event":"e","actor":"a","details":{"n":1152921504606846977,"x":"\ud800"}}` + "\n"),
		[]byte(`{"ts":"t","event":"e","actor":"a","details":{"/home/zed/a":1,"~/a":2,"/home/bob/a":3}}` + "\n"),
		[]byte(`{"ts":"t","event":"e","actor":"a","TS":"u"}` + "\n"),
		[]byte("{}\n"), []byte("null\n"), []byte(""), []byte("\n\n"), []byte("not a record"),
		[]byte(`{"id":"m-1","write_set":[null]}` + "\n"),
	}
}

// writerLeaves is the multiset of non-empty leaf values of a decoded document:
// strings and non-zero numbers, keys not counted; the leaves a typed record
// may legitimately drop (empty, zero, null, false) are left out.
func writerLeaves(v any, out map[string]int) {
	switch x := v.(type) {
	case string:
		if x != "" {
			out["s:"+x]++
		}
	case json.Number:
		if f, err := x.Float64(); err != nil || f != 0 {
			out["n:"+x.String()]++
		}
	case int, int64, float64:
		if x != 0 {
			out["v:"+toString(x)]++
		}
	case []any:
		for _, e := range x {
			writerLeaves(e, out)
		}
	case map[string]any:
		for _, e := range x {
			writerLeaves(e, out)
		}
	}
}

func toString(v any) string { b, _ := json.Marshal(v); return string(b) }

// stringsOfFile decodes written bytes into every string and key, with
// exemption flags, for the oracle.
func stringsOfFile(name string, data []byte) []oStr {
	var strs []oStr
	switch filepath.Ext(name) {
	case ".yaml", ".yml":
		var n yaml.Node
		if yaml.Unmarshal(data, &n) == nil {
			nodeStrings(&n, &strs, false)
		}
	case ".json", ".jsonl":
		for _, line := range strings.Split(string(data), "\n") {
			var v any
			if strings.TrimSpace(line) != "" && json.Unmarshal([]byte(line), &v) == nil {
				stringsOf(v, &strs, false)
			}
		}
	}
	return strs
}

// genericLeaves decodes a file's bytes generically, numbers as written.
func genericLeaves(name string, data []byte) (map[string]int, bool) {
	out := map[string]int{}
	switch filepath.Ext(name) {
	case ".yaml", ".yml":
		var n yaml.Node
		if yaml.Unmarshal(data, &n) != nil {
			return nil, false
		}
		yamlLeaves(&n, out)
	default:
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			dec := json.NewDecoder(bytes.NewReader([]byte(line)))
			dec.UseNumber()
			var v any
			if dec.Decode(&v) != nil {
				return nil, false
			}
			writerLeaves(v, out)
		}
	}
	return out, true
}

// checkFileProperties asserts the four properties for guardWrite on one file:
// (a) no leak outside exempt path fields, (b) no non-empty value lost, (c) a
// change is a refusal (so what is written equals the input's content), and
// (d) the output is a fixed point.
func checkFileProperties(t *testing.T, what, name string, in []byte, home string) {
	t.Helper()
	out, err := guardWrite(filepath.Join("/r", name), in)
	if err != nil {
		return // (c): refused
	}
	for _, s := range stringsOfFile(name, out) {
		if s.holds(home) {
			t.Fatalf("%s %s: (a) %q in the output (home %s) for input %q", what, name, s.v, home, in)
		}
	}
	if want, ok := genericLeaves(name, in); ok {
		if got, ok := genericLeaves(name, out); ok {
			for leaf, n := range want {
				if got[leaf] < n {
					t.Fatalf("%s %s: (b) value %q lost (%d -> %d): in %q out %q", what, name, leaf, n, got[leaf], in, out)
				}
			}
			// (c) A write that is not a refusal changes no value: nothing is
			// added, rewritten or replaced either.
			for leaf, n := range got {
				if want[leaf] < n {
					t.Fatalf("%s %s: (c) value %q appears in the output (%d, input %d) and the write was not refused: in %q out %q", what, name, leaf, n, want[leaf], in, out)
				}
			}
		}
	}
	again, err := guardWrite(filepath.Join("/r", name), out)
	if err != nil || !bytes.Equal(again, out) {
		t.Fatalf("%s %s: (d) not a fixed point: in %q out %q again %q err %v", what, name, in, out, again, err)
	}
}

// FuzzWritersMission drives the mission-tree writers with raw bytes: the sink
// guard for every typed record, scrub, and migrate's copy.
func FuzzWritersMission(f *testing.F) {
	for i := range fuzzHomes {
		for _, s := range writerSeeds() {
			for w := range writerFiles {
				f.Add(s, uint8(i), uint8(w))
			}
		}
	}
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte, h uint8, which uint8) {
		home := fuzzHomes[int(h)%len(fuzzHomes)]
		t.Setenv("HOME", home)
		name := writerFiles[int(which)%len(writerFiles)]
		testhelpers.CaptureStderr(t, func() {
			checkFileProperties(t, "guardWrite", name, data, home)

			// Scrub: the file in place, then again.
			repo := filepath.Join(dir, "repo")
			rel := ".punt-labs/ethos/missions/m-2026-01-01-001/" + name
			switch name {
			case "missions.jsonl":
				rel = ".punt-labs/ethos/missions.jsonl"
			case "log.jsonl", "m-1.result.yaml":
				return // not scrubbable record files
			case "record.yaml":
				rel = ".punt-labs/ethos/missions/m-2026-01-01-001/delegations/d-1/record.yaml"
			}
			dest := filepath.Join(repo, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dest, data, 0o600); err != nil {
				t.Fatal(err)
			}
			r := PathRedactor{Home: home, Repo: repo}
			if _, err := scrubFile(r, repo, rel, false); err != nil {
				got, _ := os.ReadFile(dest)
				if !bytes.Equal(got, data) {
					t.Fatalf("scrub refused %q but changed the file to %q", data, got)
				}
				return
			}
			got, err := os.ReadFile(dest)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range stringsOfFile(name, got) {
				if s.holds(home) {
					t.Fatalf("scrub %s: (a) %q in the output (home %s) for input %q", name, s.v, home, data)
				}
			}
			changed, err := scrubFile(r, repo, rel, false)
			if err != nil || changed {
				t.Fatalf("scrub %s: (d) not a fixed point for input %q (changed %v, err %v)", name, data, changed, err)
			}
		})
	})
}

// yamlLeaves counts the non-empty scalar values of a node tree, by their text
// (so 01 is the string 01 whatever a decode makes of it), aliases followed,
// mapping keys not counted.
func yamlLeaves(n *yaml.Node, out map[string]int) {
	n = derefAlias(n)
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag != "!!null" && n.Value != "" && n.Value != "0" && n.Value != "false" {
			out["s:"+n.Value]++
		}
	case yaml.MappingNode:
		for i := 1; i < len(n.Content); i += 2 {
			yamlLeaves(n.Content[i], out)
		}
	default:
		for _, c := range n.Content {
			yamlLeaves(c, out)
		}
	}
}

// decodeDetails decodes data as one JSON object, numbers as written.
func decodeDetails(data []byte) (map[string]any, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, false
	}
	return m, true
}

// FuzzWritersRecords drives the record writers that build their bytes from
// values: an event with raw Details through appendEventLocked, a delegation
// skeleton with a raw prompt and session, and migrate's copy of a raw file.
func FuzzWritersRecords(f *testing.F) {
	for i := range fuzzHomes {
		for _, s := range writerSeeds() {
			f.Add(s, uint8(i))
		}
	}
	f.Fuzz(func(t *testing.T, data []byte, h uint8) {
		home := fuzzHomes[int(h)%len(fuzzHomes)]
		t.Setenv("HOME", home)
		testhelpers.CaptureStderr(t, func() {
			// An event whose Details are the raw map.
			if m, ok := decodeDetails(data); ok {
				repo := t.TempDir()
				s := NewStoreWithRoots(repo, t.TempDir())
				const id = "m-2026-01-01-001"
				if err := s.appendEventLocked(id, Event{TS: "t", Event: "e", Actor: "a", Details: m}); err == nil {
					raw, err := os.ReadFile(audit.LiveMissionLogPath(repo, id, sessionlessID))
					if err != nil {
						t.Fatalf("event: reading the live log back: %v for %q", err, data)
					}
					got := bytes.TrimSpace(raw)
					var strs []oStr
					var v any
					if json.Unmarshal(got, &v) == nil {
						stringsOf(v, &strs, false)
					}
					for _, st := range strs {
						if st.holds(home) {
							t.Fatalf("event: (a) %q stored (home %s) for %q", st.v, home, data)
						}
					}
					have := testhelpers.Leaves{}
					dm, _ := decodeDetails(got)
					if d, ok := dm["details"]; ok {
						have = testhelpers.CollectLeaves(d)
					}
					if leaf, ok := testhelpers.LostLeaf(testhelpers.CollectLeaves(m), have); ok {
						t.Fatalf("event: (b) value %q lost with nothing in its place: in %q stored %q", leaf, data, got)
					}
				}
			}

			// A delegation skeleton with a raw prompt and session.
			repo := t.TempDir()
			rec, err := WriteDelegationSkeleton(repo, "m-2026-01-01-001", "d-2026-01-01-001", DelegationSkeleton{
				Tier: TierB, AgentType: "bwk", Session: string(data), Prompt: data,
			})
			if err == nil {
				prompt, _ := os.ReadFile(filepath.Join(filepath.Dir(rec), "prompt.md"))
				if LeakRule(string(prompt)) != "" {
					t.Fatalf("delegation: (a) prompt.md leaks (home %s) for %q: %q", home, data, prompt)
				}
				body, _ := os.ReadFile(rec)
				for _, st := range stringsOfFile("record.yaml", body) {
					if st.holds(home) {
						t.Fatalf("delegation: (a) %q in record.yaml (home %s) for %q", st.v, home, data)
					}
				}
			}

			// Migrate's copy of a raw file, for each artifact kind, then its own
			// output again.
			dir := t.TempDir()
			r := PathRedactor{Home: home, Repo: repo}
			for _, name := range []string{"contract.yaml", "results.yaml", "reflections.yaml", "log.jsonl"} {
				src, dst := filepath.Join(dir, "src", name), filepath.Join(dir, "dst", name)
				_ = os.MkdirAll(filepath.Dir(src), 0o700)
				_ = os.MkdirAll(filepath.Dir(dst), 0o700)
				if err := os.WriteFile(src, data, 0o600); err != nil {
					t.Fatal(err)
				}
				if copyIfExists(r, src, dst) != nil {
					continue
				}
				out, _ := os.ReadFile(dst)
				for _, st := range stringsOfFile(name, out) {
					if st.holds(home) {
						t.Fatalf("migrate %s: (a) %q in the copy (home %s) for %q", name, st.v, home, data)
					}
				}
				src2, dst2 := filepath.Join(dir, "src2", name), filepath.Join(dir, "again", name)
				_ = os.MkdirAll(filepath.Dir(src2), 0o700)
				_ = os.MkdirAll(filepath.Dir(dst2), 0o700)
				if err := os.WriteFile(src2, out, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := copyIfExists(r, src2, dst2); err != nil {
					t.Fatalf("migrate %s: (d) the copy's own output is refused: %v for %q -> %q", name, err, data, out)
				}
				again, _ := os.ReadFile(dst2)
				if !bytes.Equal(again, out) {
					t.Fatalf("migrate %s: (d) not a fixed point: %q -> %q -> %q", name, data, out, again)
				}
			}
		})
	})
}
