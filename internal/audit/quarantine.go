package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// Quarantine retires a corrupt sealed chunk and recovers what it can, the
// sanctioned alternative to `git commit --no-verify` (docs/audit-seal.md
// §Seal failure policy). In order:
//
//  1. Retire the corrupt chunk: git mv <stem>.jsonl -> <stem>.jsonl.corrupt,
//     freeing the chunk name so the re-seal cannot clobber it.
//  2. Re-seal every line of the chunk's [first,last] range the live file
//     still holds into an ordinary content-named chunk.
//  3. Write a deterministic .quarantine marker: the retired chunk name, the
//     verified content-derived <last>, the unrecovered sub-range, the reason.
//  4. git add the .corrupt, the re-sealed chunk, and the marker.
//
// The marker is the last durable artifact, so a crash mid-verb resumes
// deterministically by artifact state:
//
//   - marker present & parses -> idempotent no-op (re-stage covered artifacts).
//   - marker absent/torn, chunk present -> fresh run (retire, re-seal, mark).
//   - marker absent/torn, .corrupt present -> resume (finish re-seal + mark);
//     a chunk already at the re-seal target that verifies is the completed
//     re-seal and is kept, not a collision.
//
// livePath is the session audit live file (session namespace) or the
// per-(mission, session) mission live log (mission namespace) the re-seal
// draws from. redactReason is applied to reason: the marker is committed
// beside the sealed chunks and the reason is free text, often an error
// string carrying a path. The caller supplies it because only the caller
// knows the home and repo prefixes. checkRetired names the rule a retired
// chunk's bytes break (a home or user path, a credential, an email address), or
// returns "". The .corrupt is
// staged in a tracked tree, so bytes that fail the check are never staged: the
// original is kept in the untracked local zone (QuarantineDir) and a record of
// its hash, size and rule is written and staged in its place, before the chunk
// is moved. Every run applies the same check to each .corrupt of every chunk the
// marker covers (coveredCorrupts), a re-sealed chunk of another stem included, so
// a crash or an older binary cannot leave leaking bytes of those chunks staged.
// Other .corrupt files of this namespace (in the mission namespace, of this
// session) are not in that set: StageUntrackedChunks stages them without the
// check. Returns the marker it wrote (or found).
func Quarantine(repoRoot, sealedDir string, cn ChunkName, livePath, reason string, redactReason func(string) string, guardLine func([]byte) ([]byte, string), checkRetired func([]byte) string) (Marker, error) {
	reason = redactReason(reason)
	np := &notePad{}
	stem := cn.Stem()
	chunkPath := filepath.Join(sealedDir, cn.ChunkFile())
	corruptPath := filepath.Join(sealedDir, CorruptName(stem))
	markerPath := filepath.Join(sealedDir, cn.MarkerFile())

	// State: a parseable marker means the quarantine already completed — an
	// idempotent no-op, but NOT blind to fresh damage (OPT-1): content-verify
	// any chunk standing at a name the marker covers and retire fresh
	// corruption as a new event under a content-hashed name, then re-stage.
	if mk, err := ReadMarker(markerPath); err == nil {
		if err := withholdSweep(repoRoot, sealedDir, cn, checkRetired, np); err != nil {
			return Marker{}, err
		}
		if err := reconcileUnderMarker(repoRoot, sealedDir, cn, checkRetired, np); err != nil {
			return Marker{}, err
		}
		if err := np.addUnstagedRecords(repoRoot, sealedDir, cn); err != nil {
			return Marker{}, err
		}
		if _, sErr := stageQuarantineArtifacts(repoRoot, sealedDir, cn); sErr != nil {
			return Marker{}, sErr
		}
		np.print()
		return mk, nil
	}

	// Ensure the corrupt bytes sit at <stem>.jsonl.corrupt — retire on a
	// fresh run, or find them already there on a resume.
	if _, err := os.Stat(chunkPath); err == nil {
		// A chunk AND an existing .corrupt with no covering marker is the
		// resume-window "fresh damage" state. The first event's .corrupt must
		// never be overwritten (docs §Seal failure policy), so the fresh chunk
		// is retired under a content-hashed .corrupt-<hash> name that cannot
		// collide with it (OPT-1), preserving both evidences; the re-seal +
		// marker below then complete the quarantine.
		done, fErr := finishRecordedRetirement(repoRoot, sealedDir, cn, chunkPath, np)
		if fErr != nil {
			return Marker{}, fErr
		}
		// A crash after the record was written left the chunk beside it, and
		// the retirement is finished. Otherwise this is a fresh retirement.
		if !done {
			if _, cErr := os.Stat(corruptPath); cErr == nil {
				if rErr := retireChunkUnderHash(repoRoot, sealedDir, cn, chunkPath, checkRetired, np); rErr != nil {
					return Marker{}, rErr
				}
			} else if rErr := retireChecked(repoRoot, cn.Stem(), chunkPath, corruptPath, checkRetired, np); rErr != nil {
				return Marker{}, rErr
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Marker{}, fmt.Errorf("stat %s: %w", chunkPath, err)
	} else if _, cErr := os.Stat(corruptPath); cErr != nil {
		return Marker{}, fmt.Errorf(
			"quarantine %s: neither the chunk nor its .corrupt exists", cn.ChunkFile())
	}

	if err := withholdSweep(repoRoot, sealedDir, cn, checkRetired, np); err != nil {
		return Marker{}, err
	}
	corruptBytes, err := os.ReadFile(corruptPath)
	if err != nil {
		return Marker{}, fmt.Errorf("reading retired chunk %s: %w", corruptPath, err)
	}
	corruptMax := maxParseableTS(corruptBytes)

	// Re-seal the live lines still present in the corrupt chunk's range.
	reseal, rf, rl, err := liveLinesInRange(livePath, cn.First, cn.Last)
	if err != nil {
		return Marker{}, err
	}
	// The re-sealed lines come from the live file, which an older binary or
	// another process may have appended to; each goes through guardLine, the
	// caller's check that it names no home, before it is sealed. Its ts is
	// kept, so the chunk name still matches. guardLine returns a notice for a
	// line it rewrote; the notices are printed only if the chunk is written.
	var notes []string
	for i, l := range reseal {
		g, note := guardLine(l)
		reseal[i] = g
		if note != "" {
			notes = append(notes, note)
		}
	}
	resealMax := cn.First - 1
	if len(reseal) > 0 {
		resealMax = rl
		re := ChunkName{Namespace: cn.Namespace, Session: cn.Session, First: rf, Last: rl}
		rePath := filepath.Join(sealedDir, re.ChunkFile())
		if _, statErr := os.Stat(rePath); statErr == nil {
			// Resume: an existing target that verifies is the completed
			// re-seal, kept as-is; one that fails is fresh damage.
			if _, vErr := ReadChunkVerified(rePath, rl); vErr != nil {
				return Marker{}, fmt.Errorf("quarantine resume: %w", vErr)
			}
		} else if err := WriteChunkAtomic(sealedDir, re.TempFile(), re.ChunkFile(), reseal); err != nil {
			return Marker{}, err
		} else {
			for _, n := range notes {
				fmt.Fprintln(os.Stderr, n)
			}
		}
	}

	verifiedLast := resealMax
	if corruptMax > verifiedLast {
		verifiedLast = corruptMax
	}
	var unrecFirst, unrecLast int64
	switch {
	case verifiedLast < cn.First:
		// Total loss: the live file held none of the range and the corrupt
		// bytes yielded no ts within it, so nothing was verified. Record the
		// whole nominal range [First,Last] as the gap — the maximal-loss case
		// must report loss, not silent full recovery.
		unrecFirst = cn.First
		unrecLast = cn.Last
	case verifiedLast > resealMax:
		unrecFirst = resealMax + 1
		if len(reseal) == 0 {
			unrecFirst = cn.First
		}
		unrecLast = verifiedLast
	}

	marker := Marker{
		Chunk:            stem,
		VerifiedLast:     verifiedLast,
		UnrecoveredFirst: unrecFirst,
		UnrecoveredLast:  unrecLast,
		Reason:           reason,
	}
	if err := writeMarkerAtomic(sealedDir, cn.MarkerFile(), marker); err != nil {
		return Marker{}, err
	}
	if err := np.addUnstagedRecords(repoRoot, sealedDir, cn); err != nil {
		return Marker{}, err
	}
	if _, err := stageQuarantineArtifacts(repoRoot, sealedDir, cn); err != nil {
		return Marker{}, err
	}
	np.print()
	return marker, nil
}

// stageQuarantineArtifacts git-adds every chunk-namespace artifact in the
// directory — the .corrupt, the marker, the re-sealed chunk — so the tree is
// clean and committable with no hand-staging.
func stageQuarantineArtifacts(repoRoot, sealedDir string, cn ChunkName) (int, error) {
	return StageUntrackedChunks(repoRoot, sealedDir, cn.Namespace, cn.Session)
}

// renameRetire moves src to dst via git (staging the rename) or a plain
// rename when git mv fails (an untracked source). The caller guarantees dst is
// absent, so the rename cannot clobber.
func renameRetire(repoRoot, src, dst string) error {
	if mvErr := GitMv(repoRoot, src, dst); mvErr != nil {
		if rErr := os.Rename(src, dst); rErr != nil {
			return fmt.Errorf("retiring %s: git mv: %v; rename: %w", src, mvErr, rErr)
		}
	}
	return nil
}

// corruptHashName returns the .corrupt-<hash> filename for a chunk stem and its
// content, using a deterministic never-overwrite sequence (-2, -3, …) so a
// second event never collides with the first .corrupt or an identical earlier
// .corrupt-<hash> (docs/audit-seal.md §Seal failure policy). A non-ENOENT stat
// error (EACCES, EIO) is returned rather than swallowed, so the loop surfaces
// the filesystem fault instead of spinning forever treating every candidate as
// taken.
func corruptHashName(dir, stem string, content []byte) (string, error) {
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])[:12]
	for n := 1; ; n++ {
		candidate := CorruptHashName(stem, hash, n)
		_, err := os.Stat(filepath.Join(dir, candidate))
		if errors.Is(err, fs.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", fmt.Errorf("stat %s: %w", filepath.Join(dir, candidate), err)
		}
	}
}

// retireChunkUnderHash retires the chunk at chunkPath under a content-hashed
// .corrupt-<hash> name, preserving any existing .corrupt evidence.
func retireChunkUnderHash(repoRoot, sealedDir string, chunkCN ChunkName, chunkPath string, check func([]byte) string, np *notePad) error {
	b, err := os.ReadFile(chunkPath)
	if err != nil {
		return fmt.Errorf("reading fresh-damage chunk %s: %w", chunkPath, err)
	}
	hashed, err := corruptHashName(sealedDir, chunkCN.Stem(), b)
	if err != nil {
		return err
	}
	return retireChecked(repoRoot, chunkCN.Stem(), chunkPath, filepath.Join(sealedDir, hashed), check, np)
}

// retireChecked moves the chunk at src to dst. When check names a rule for its
// bytes, nothing that holds them is staged: the original is kept in the local
// zone, the record of its hash, size and rule is written at dst, and src is
// then removed from the index and the tree. dst never holds the bytes. A crash
// after the record is written leaves src beside the record, and the next run
// finishes the retirement (finishRecordedRetirement), whether it finds no marker
// (Quarantine) or one (reconcileUnderMarker); a crash after src is removed
// leaves the record in the work tree with the index not yet holding it, and the
// next run stages it, on both paths. The withheld notice is printed where the
// record is written (np.say), so a crash after that point has already said it;
// the re-run says it once more for a record it finds unstaged.
func retireChecked(repoRoot, stem, src, dst string, check func([]byte) string, np *notePad) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	rule := check(b)
	if rule == "" {
		return renameRetire(repoRoot, src, dst)
	}
	if err := saveOriginal(repoRoot, stem, b); err != nil {
		return err
	}
	if err := writeRecord(dst, b, rule); err != nil {
		return err
	}
	np.say(dst, rule)
	tracked, err := IsTracked(repoRoot, src)
	if err != nil {
		return err
	}
	if tracked {
		if err := GitRmCached(repoRoot, src); err != nil {
			return err
		}
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("removing %s: %w", src, err)
	}
	if err := GitAdd(repoRoot, dst); err != nil {
		return err
	}
	return nil
}

// withholdSweep applies check to every .corrupt of every chunk the marker covers
// (coveredCorrupts) and withholds the
// ones that fail, in place.
func withholdSweep(repoRoot, sealedDir string, cn ChunkName, check func([]byte) string, np *notePad) error {
	corrupts, err := coveredCorrupts(sealedDir, cn)
	if err != nil {
		return err
	}
	for _, cf := range corrupts {
		b, err := os.ReadFile(cf.path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", cf.path, err)
		}
		rule := check(b)
		if rule == "" {
			// The work tree is clean, but the index may still hold what an
			// older binary staged, or what a failed add left: stage the work
			// tree, keeping the staged bytes in the local zone if they leak.
			if err := restageIfDifferent(repoRoot, cf.path, b, cf.stem, check, np); err != nil {
				return err
			}
			continue
		}
		if err := saveOriginal(repoRoot, cf.stem, b); err != nil {
			return err
		}
		if err := writeRecord(cf.path, b, rule); err != nil {
			return err
		}
		np.say(cf.path, rule)
		if err := GitAdd(repoRoot, cf.path); err != nil {
			return err
		}
	}
	return nil
}

// corruptFile is a .corrupt artifact and the stem of the chunk it came from.
type corruptFile struct {
	path, stem string
}

// coveredCorrupts lists every .corrupt artifact of a chunk the marker for cn
// covers: its own, and those of a re-sealed chunk whose stem differs, by the
// marker's range rule (the chunk's range lies inside cn's, in cn's session for
// the mission namespace), judged from the artifact's own name.
func coveredCorrupts(sealedDir string, cn ChunkName) ([]corruptFile, error) {
	entries, err := os.ReadDir(sealedDir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", sealedDir, err)
	}
	var out []corruptFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		c, kind := Classify(e.Name(), cn.Namespace)
		if kind != KindCorrupt {
			continue
		}
		if cn.Namespace == MissionNS && c.Session != cn.Session {
			continue
		}
		if c.Stem() != cn.Stem() && !(cn.First <= c.First && c.Last <= cn.Last) {
			continue
		}
		out = append(out, corruptFile{path: filepath.Join(sealedDir, e.Name()), stem: c.Stem()})
	}
	return out, nil
}

// restageIfDifferent stages path when the index holds something other than
// its work-tree bytes b. An index blob that fails check is saved first and
// its notice is printed (np.say) as soon as the add succeeds.
func restageIfDifferent(repoRoot, path string, b []byte, stem string, check func([]byte) string, np *notePad) error {
	blob, tracked, err := IndexBlob(repoRoot, path)
	if err != nil {
		return err
	}
	if !tracked || bytes.Equal(blob, b) {
		return nil
	}
	rule := check(blob)
	if rule != "" {
		if err := saveOriginal(repoRoot, stem, blob); err != nil {
			return err
		}
	}
	if err := GitAdd(repoRoot, path); err != nil {
		return err
	}
	if rule != "" {
		np.say(path, rule)
	}
	return nil
}

// finishRecordedRetirement completes a retirement a crash interrupted after
// its record was written: when a .corrupt of cn's stem holds a record of the
// chunk's own sha256, the chunk is removed from the index and the tree and the
// record is staged, and no second .corrupt-<hash> is made for it. Quarantine
// and reconcileUnderMarker both call it before they retire a chunk.
func finishRecordedRetirement(repoRoot, sealedDir string, cn ChunkName, chunkPath string, np *notePad) (bool, error) {
	b, err := os.ReadFile(chunkPath)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", chunkPath, err)
	}
	sum := sha256.Sum256(b)
	want := hex.EncodeToString(sum[:])
	entries, err := os.ReadDir(sealedDir)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", sealedDir, err)
	}
	prefix := cn.Stem() + ".jsonl.corrupt"
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		path := filepath.Join(sealedDir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return false, fmt.Errorf("reading %s: %w", path, err)
		}
		var rec struct {
			SHA256 string `json:"sha256"`
			Rule   string `json:"rule"`
		}
		if json.Unmarshal(bytes.TrimSpace(raw), &rec) != nil || rec.SHA256 != want {
			continue
		}
		if err := saveOriginal(repoRoot, cn.Stem(), b); err != nil {
			return false, err
		}
		tracked, err := IsTracked(repoRoot, chunkPath)
		if err != nil {
			return false, err
		}
		if tracked {
			if err := GitRmCached(repoRoot, chunkPath); err != nil {
				return false, err
			}
		}
		if err := os.Remove(chunkPath); err != nil {
			return false, fmt.Errorf("removing %s: %w", chunkPath, err)
		}
		if err := GitAdd(repoRoot, path); err != nil {
			return false, err
		}
		np.say(path, rec.Rule)
		return true, nil
	}
	return false, nil
}

// saveOriginal keeps b in the local zone as <stem>.<hash>.corrupt.
func saveOriginal(repoRoot, stem string, b []byte) error {
	sum := sha256.Sum256(b)
	dir := QuarantineDir(repoRoot)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	return writeFileAtomic(filepath.Join(dir, stem+"."+hex.EncodeToString(sum[:])[:12]+".corrupt"), b)
}

// WithheldRule is the rule a retired chunk's record names: every retired chunk
// is withheld, so the rule is a label, not a finding.
const WithheldRule = "retired chunk withheld"

// withheldText is the "withheld" field of a record.
const withheldText = "retired chunk bytes withheld from the tracked tree"

// IsWithheldRecord reports whether b is exactly a record writeRecord wrote with
// WithheldRule. Only such bytes may be staged as a retired chunk's .corrupt; they
// hold no byte of the chunk. The record is decoded, rebuilt the way writeRecord
// builds it, and the two must be byte for byte equal, so a duplicate key, a
// case-variant key, another key order, stray white space or a second line makes
// it something else. A ts, when present, must be a timestamp as FormatLineTS
// writes it.
func IsWithheldRecord(b []byte) bool {
	var rec struct {
		Withheld string `json:"withheld"`
		SHA256   string `json:"sha256"`
		Size     int    `json:"size"`
		Rule     string `json:"rule"`
		TS       string `json:"ts"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(&rec) != nil {
		return false
	}
	if rec.Withheld != withheldText || rec.Rule != WithheldRule {
		return false
	}
	if raw, err := hex.DecodeString(rec.SHA256); err != nil || len(raw) != sha256.Size {
		return false
	}
	ts := int64(-1)
	if rec.TS != "" {
		n, err := ParseLineTS(rec.TS)
		if err != nil || FormatLineTS(n) != rec.TS {
			return false
		}
		ts = n
	}
	rebuilt, err := recordBytes(rec.SHA256, rec.Size, rec.Rule, ts)
	return err == nil && bytes.Equal(b, rebuilt)
}

// recordBytes is the one encoding of a record: the fields, ts only when it is
// not negative, as one line.
func recordBytes(sumHex string, size int, rule string, ts int64) ([]byte, error) {
	rec := map[string]any{
		"withheld": withheldText,
		"sha256":   sumHex,
		"size":     size,
		"rule":     rule,
	}
	if ts >= 0 {
		rec["ts"] = FormatLineTS(ts)
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	return append(line, '\n'), nil
}

// writeRecord replaces the file at path with a one-line record of b's hash,
// size and the rule it broke. The record carries no byte of b, but keeps the
// newest ts b held so the quarantine can still tell how far the corruption
// reached.
func writeRecord(path string, b []byte, rule string) error {
	sum := sha256.Sum256(b)
	ts := int64(-1)
	if mx, ok := maxParseableTSOK(b); ok {
		ts = mx
	}
	line, err := recordBytes(hex.EncodeToString(sum[:]), len(b), rule, ts)
	if err != nil {
		return fmt.Errorf("encoding record for %s: %w", path, err)
	}
	return writeFileAtomic(path, line)
}

// notePad keeps the withheld notices of one Quarantine run, one per path. say
// prints a notice at once, where a record is written or a leaking index blob
// is restaged, unless this run already has. add is used only by
// addUnstagedRecords, to queue the notice for a record found unstaged; print
// says the queued ones once the run's final staging has succeeded.
type notePad struct {
	rules   map[string]string
	local   map[string]bool // add: whether a local copy of the original exists
	order   []string
	printed map[string]bool
}

// say prints the notice for path now, unless this run already has.
func (n *notePad) say(path, rule string) {
	if n.printed == nil {
		n.printed = map[string]bool{}
	}
	if n.printed[path] {
		return
	}
	n.printed[path] = true
	noticeWithheld(path, rule, true)
}

func (n *notePad) add(path, rule string, local bool) {
	if n.rules == nil {
		n.rules = map[string]string{}
		n.local = map[string]bool{}
	}
	n.local[path] = local
	if _, seen := n.rules[path]; !seen {
		n.order = append(n.order, path)
	}
	n.rules[path] = rule
}

// addUnstagedRecords adds every record among the .corrupt files of every chunk
// the marker covers (coveredCorrupts) whose bytes the index does
// not hold yet (a crash, or a failed add, left it so): the staging that follows
// is what puts it there, and this run says so.
func (n *notePad) addUnstagedRecords(repoRoot, sealedDir string, cn ChunkName) error {
	corrupts, err := coveredCorrupts(sealedDir, cn)
	if err != nil {
		return err
	}
	for _, cf := range corrupts {
		path := cf.path
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		var rec struct {
			Withheld string `json:"withheld"`
			Rule     string `json:"rule"`
			SHA256   string `json:"sha256"`
		}
		if json.Unmarshal(bytes.TrimSpace(raw), &rec) != nil || rec.Withheld == "" {
			continue
		}
		blob, tracked, err := IndexBlob(repoRoot, path)
		if err != nil {
			return err
		}
		if !tracked || !bytes.Equal(blob, raw) {
			n.add(path, rec.Rule, hasLocalCopy(repoRoot, cf.stem, rec.SHA256))
		}
	}
	return nil
}

// print writes one notice per collected path to stderr.
func (n *notePad) print() {
	for _, path := range n.order {
		if n.printed == nil {
			n.printed = map[string]bool{}
		}
		if n.printed[path] {
			continue
		}
		n.printed[path] = true
		noticeWithheld(path, n.rules[path], n.local[path])
	}
}

// noticeWithheld says that a retired chunk's bytes were replaced by a record.
func noticeWithheld(path, rule string, local bool) {
	where := filepath.Join(".punt-labs", "local", "ethos", "quarantine")
	if local {
		fmt.Fprintf(os.Stderr, "ethos: audit: retired chunk %s was withheld from the tracked tree (%s); the original is in %s\n",
			filepath.Base(path), rule, where)
		return
	}
	fmt.Fprintf(os.Stderr, "ethos: audit: retired chunk %s was withheld from the tracked tree (%s); no local copy of the original was found in %s\n",
		filepath.Base(path), rule, where)
}

// hasLocalCopy reports whether the local zone holds the original a record names:
// <stem>.<first 12 of sumHex>.corrupt, whose bytes hash to sumHex. A copy of
// another retirement of the same stem does not count.
func hasLocalCopy(repoRoot, stem, sumHex string) bool {
	if len(sumHex) < 12 {
		return false
	}
	b, err := os.ReadFile(filepath.Join(QuarantineDir(repoRoot), stem+"."+sumHex[:12]+".corrupt"))
	if err != nil {
		return false
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]) == sumHex
}

// reconcileUnderMarker is the "not blind to fresh damage" half of the
// idempotent no-op (OPT-1): it content-verifies every valid chunk standing at
// a name the marker's range covers and retires a corrupt one as a new event
// under a content-hashed .corrupt-<hash> name, so fresh corruption of a
// re-sealed chunk becomes recorded evidence rather than a chunk a later
// seal/read trips over.
func reconcileUnderMarker(repoRoot, sealedDir string, cn ChunkName, check func([]byte) string, np *notePad) error {
	entries, err := os.ReadDir(sealedDir)
	if err != nil {
		return fmt.Errorf("reading %s: %w", sealedDir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		c, kind := Classify(e.Name(), cn.Namespace)
		if kind != KindValid {
			continue
		}
		if cn.Namespace == MissionNS && c.Session != cn.Session {
			continue
		}
		// Only chunks whose range the marker covers are its concern.
		if !(cn.First <= c.First && c.Last <= cn.Last) {
			continue
		}
		p := filepath.Join(sealedDir, e.Name())
		if _, vErr := ReadChunkVerified(p, c.Last); vErr != nil {
			// A crash under the marker may have left this chunk's record
			// already; finish that retirement rather than write a second.
			done, fErr := finishRecordedRetirement(repoRoot, sealedDir, c, p, np)
			if fErr != nil {
				return fErr
			}
			if done {
				continue
			}
			if rErr := retireChunkUnderHash(repoRoot, sealedDir, c, p, check, np); rErr != nil {
				return rErr
			}
		}
	}
	return nil
}

// liveLinesInRange returns the live file's complete, parseable lines whose ts
// lies within [first,last] inclusive, plus that subset's first and last ts.
// The live file is never truncated, so in the common case every line of a
// corrupt chunk's range is still here and recovery is full.
func liveLinesInRange(livePath string, first, last int64) (lines [][]byte, rf, rl int64, err error) {
	data, err := os.ReadFile(livePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, 0, nil
		}
		return nil, 0, 0, fmt.Errorf("reading live %s: %w", livePath, err)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		if cut := lastNewline(data); cut >= 0 {
			data = data[:cut+1]
		} else {
			data = nil
		}
	}
	for _, raw := range SplitLines(data) {
		var h tsHolder
		if json.Unmarshal(raw, &h) != nil {
			continue
		}
		ts, perr := ParseLineTS(h.TS)
		if perr != nil || ts < first || ts > last {
			continue
		}
		if len(lines) == 0 {
			rf = ts
		}
		rl = ts
		cp := make([]byte, len(raw))
		copy(cp, raw)
		lines = append(lines, cp)
	}
	return lines, rf, rl, nil
}

// maxParseableTS returns the max ts over the parseable lines of a chunk's
// (corrupt) bytes — the max ts the corruption actually reached.
func maxParseableTS(data []byte) int64 {
	mx, _ := maxParseableTSOK(data)
	return mx
}

// maxParseableTSOK is maxParseableTS also reporting whether any line had a ts.
func maxParseableTSOK(data []byte) (int64, bool) {
	var mx int64
	first := true
	for _, raw := range SplitLines(data) {
		var h tsHolder
		if json.Unmarshal(raw, &h) != nil {
			continue
		}
		ts, perr := ParseLineTS(h.TS)
		if perr != nil {
			continue
		}
		if first || ts > mx {
			mx = ts
			first = false
		}
	}
	return mx, !first
}

// writeMarkerAtomic writes a marker via temp + fsync + rename so it never
// appears in a torn state (a torn marker reads as absent everywhere).
func writeMarkerAtomic(dir, name string, m Marker) error {
	data, err := MarshalMarker(m)
	if err != nil {
		return err
	}
	tempPath := filepath.Join(dir, layout.DotTmp(name))
	f, err := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("creating temp marker %s: %w", tempPath, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("writing temp marker %s: %w", tempPath, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("syncing temp marker %s: %w", tempPath, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("closing temp marker %s: %w", tempPath, err)
	}
	if err := os.Rename(tempPath, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("renaming temp marker: %w", err)
	}
	return nil
}
