package doctor

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// handEditFileNames is the closed set of mission artifact files this
// check scans. Every other file under missions/<id>/ (log.jsonl and
// its sealed chunks) is machine-appended JSONL, not YAML, and outside
// this check's scope.
var handEditFileNames = map[string]bool{
	"contract.yaml":    true,
	"results.yaml":     true,
	"reflections.yaml": true,
}

// CheckNoHandEditedMissionFiles flags any contract.yaml, results.yaml,
// or reflections.yaml under <storeRoot>/.punt-labs/ethos/missions/
// carrying a genuine YAML comment. These files are exclusively
// yaml.Marshal output — the encoder never emits a comment — so any
// comment present is definitionally a hand-edit, not a machine write.
// The sanctioned fix for "the record says something wrong" is `ethos
// mission correct`, which appends to the event log instead of
// touching these files at all — see DES-072.
//
// DES-072's Decision section describes the detector as a `^\s*#`
// regex. This implementation parses each file as YAML and inspects
// the decoded node tree's comment fields instead: a naive line regex
// false-positives on a legitimate multi-line block scalar (`prose: |`
// on a Result) whose content happens to start a line with '#' — e.g.
// prose referencing "PR #430", which this repo's own tracked mission
// data does today (m-2026-08-06-018/results.yaml). YAML's block-scalar
// grammar does not treat '#' as a comment marker inside literal
// content, and gopkg.in/yaml.v3's Node tracks exactly the comments its
// own parser recognizes — so this detector keeps the design's stated
// promise ("yaml.Marshal never emits a comment, so any comment present
// is a hand-edit") without also flagging content that was never a
// comment in the first place.
func CheckNoHandEditedMissionFiles(storeRoot string) Result {
	name := "Mission file hand-edits"

	if storeRoot == "" {
		return Result{Name: name, Status: "PASS", Detail: "not in a repo"}
	}
	missionsDir := filepath.Join(storeRoot, ".punt-labs", "ethos", "missions")
	info, err := os.Stat(missionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{Name: name, Status: "PASS", Detail: "no missions directory"}
		}
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("cannot stat %s: %v", missionsDir, err)}
	}
	if !info.IsDir() {
		return Result{Name: name, Status: "PASS", Detail: "no missions directory"}
	}

	var flagged []string
	walkErr := filepath.WalkDir(missionsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !handEditFileNames[d.Name()] {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", path, readErr)
		}
		hasComment, decodeErr := yamlHasComment(data)
		if decodeErr != nil {
			// A file that fails to parse as YAML at all is a
			// different, more severe problem than a hand-edited
			// comment — surface it as its own flagged entry so the
			// operator sees corruption rather than a silent skip.
			rel, relErr := filepath.Rel(storeRoot, path)
			if relErr != nil {
				rel = path
			}
			flagged = append(flagged, fmt.Sprintf("%s (unparseable: %v)", rel, decodeErr))
			return nil
		}
		if hasComment {
			rel, relErr := filepath.Rel(storeRoot, path)
			if relErr != nil {
				rel = path
			}
			flagged = append(flagged, rel)
		}
		return nil
	})
	if walkErr != nil {
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("scanning %s: %v", missionsDir, walkErr)}
	}
	if len(flagged) == 0 {
		return Result{Name: name, Status: "PASS", Detail: "no hand-edited mission files"}
	}
	sort.Strings(flagged)
	return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
		"comment found in machine-written file(s) — these are yaml.Marshal output and never contain comments, so this is a hand-edit: %s — use `ethos mission correct` instead",
		strings.Join(flagged, ", "))}
}

// yamlHasComment reports whether data, parsed as YAML, carries any
// genuine comment recognized by the parser. Empty input (a blank
// results.yaml/reflections.yaml before any entry is appended) decodes
// to a zero-value node with no comments and returns false, not an
// error.
func yamlHasComment(data []byte) (bool, error) {
	if strings.TrimSpace(string(data)) == "" {
		return false, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false, err
	}
	return nodeHasComment(&doc), nil
}

// nodeHasComment walks a decoded yaml.Node tree and reports whether
// any node carries a head, line, or foot comment — the three fields
// gopkg.in/yaml.v3 populates from '#' text its parser recognized as
// a comment (as opposed to literal content inside a block scalar).
func nodeHasComment(n *yaml.Node) bool {
	if n == nil {
		return false
	}
	if n.HeadComment != "" || n.LineComment != "" || n.FootComment != "" {
		return true
	}
	for _, c := range n.Content {
		if nodeHasComment(c) {
			return true
		}
	}
	return false
}
