package ui

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
)

type browseData struct {
	Title   string
	Path    string
	IsDir   bool
	Parent  string
	Entries []dirEntry
	Lines   []blameLine
	Notices []string // what could not be looked up, shown on the page
}

type dirEntry struct {
	Name  string
	Path  string
	IsDir bool
}

type blameLine struct {
	Num        int
	Text       string
	Author     string
	Commit     string
	Mission    string
	Delegation string
	Agent      string
}

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	relPath := strings.TrimPrefix(r.URL.Path, "/browse/")
	if relPath == "" {
		relPath = "."
	}

	absPath := filepath.Join(s.repoRoot, relPath)

	// Safety: don't escape the repo root. Require exact prefix match
	// with trailing separator so /code/ethos doesn't match
	// /code/ethos-private. repoRoot is abs-normalized in NewServer.
	abs, err := filepath.Abs(absPath)
	if err != nil || (abs != s.repoRoot && !strings.HasPrefix(abs, s.repoRoot+string(filepath.Separator))) {
		http.Error(w, "path outside repo", http.StatusForbidden)
		return
	}

	info, err := os.Stat(abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if info.IsDir() {
		s.renderDir(w, relPath, abs)
		return
	}

	s.renderFile(w, relPath, abs)
}

func (s *Server) renderDir(w http.ResponseWriter, relPath, absPath string) {
	entries, err := os.ReadDir(absPath)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	var dirs, files []dirEntry
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		childRel := relPath
		if childRel == "." {
			childRel = name
		} else {
			childRel = relPath + "/" + name
		}
		if e.IsDir() {
			dirs = append(dirs, dirEntry{Name: name, Path: childRel, IsDir: true})
		} else {
			files = append(files, dirEntry{Name: name, Path: childRel, IsDir: false})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	parent := ""
	if relPath != "." {
		parent = filepath.Dir(relPath)
		if parent == "." {
			parent = ""
		}
	}

	data := browseData{
		Title:   relPath,
		Path:    relPath,
		IsDir:   true,
		Parent:  parent,
		Entries: append(dirs, files...),
	}
	if err := s.tmpl.ExecuteTemplate(w, "browse.html", data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func (s *Server) renderFile(w http.ResponseWriter, relPath, absPath string) {
	blameData, notices := s.gitBlameWithTrailers(relPath)

	// Fallback: if blame fails, just show the file contents.
	if blameData == nil {
		f, err := os.Open(absPath)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		var lines []blameLine
		num := 1
		for sc.Scan() {
			lines = append(lines, blameLine{Num: num, Text: sc.Text()})
			num++
		}
		blameData = lines
	}

	data := browseData{
		Title:   filepath.Base(relPath),
		Path:    relPath,
		IsDir:   false,
		Lines:   blameData,
		Notices: notices,
	}
	if err := s.tmpl.ExecuteTemplate(w, "browse.html", data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

// git runs git in the repository root (the located checkout) in the Parsed
// environment, under the gitenv deadline. Every
// failure — an untracked file, an unknown commit, a timeout — is an error
// carrying git's message to the caller and a notice on stderr, so a page with
// no attribution has its reason in the log. None is read as an empty answer.
func (s *Server) git(args ...string) ([]byte, error) {
	out, _, err := gitenv.Read(gitenv.Spec{Git: "git", Dir: s.repoRoot, Args: args, Env: gitenv.Parsed()}, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos ui: %v\n", err)
	}
	return out, err
}

// errGitDown is the answer for a git question skipped because an earlier one in
// the same request timed out.
var errGitDown = errors.New("git did not answer earlier in this request")

// gitBlameWithTrailers returns the blame of relPath with each line's mission
// and delegation, and a notice for everything git could not answer: a blame
// that failed (nil lines, so the caller shows the file without attribution),
// or commits whose lookup failed (their lines are shown unattributed).
func (s *Server) gitBlameWithTrailers(relPath string) ([]blameLine, []string) {
	out, err := s.git("blame", "--porcelain", relPath)
	if err != nil {
		return nil, []string{fmt.Sprintf("git blame failed, so the file is shown without attribution: %v", err)}
	}

	// Parse porcelain blame output.
	// Each hunk starts with: <sha> <orig-line> <final-line> [<num-lines>]
	// Followed by header lines, then a tab-prefixed content line.
	type hunk struct {
		commit string
		author string
		line   int
		text   string
	}

	var hunks []hunk
	var cur hunk
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		if len(line) >= 40 && line[0] != '\t' {
			parts := strings.Fields(line)
			if len(parts) >= 3 && len(parts[0]) == 40 {
				var lineNum int
				fmt.Sscanf(parts[2], "%d", &lineNum)
				cur = hunk{commit: parts[0], line: lineNum}
			}
		}
		if strings.HasPrefix(line, "author ") {
			cur.author = strings.TrimPrefix(line, "author ")
		}
		if strings.HasPrefix(line, "\t") {
			cur.text = line[1:]
			hunks = append(hunks, cur)
		}
	}

	// down is set by the first timeout: a git that hangs costs one timeout per
	// page, not one per commit, so the rest of the request does not ask again.
	down := false
	ask := func(args ...string) ([]byte, error) {
		if down {
			return nil, errGitDown
		}
		out, err := s.git(args...)
		if errors.Is(err, gitenv.ErrTimeout) {
			down = true
		}
		return out, err
	}

	// Cache commit lookups.
	type commitInfo struct {
		mission    string
		delegation string
	}
	commitCache := map[string]*commitInfo{}
	failedLookups := 0
	var firstLookupErr error
	fail := func(err error) {
		if failedLookups == 0 {
			firstLookupErr = err
		}
		failedLookups++
	}

	// ticketToMission maps bead/ticket IDs (e.g., "ethos-ozrb") to
	// mission IDs by scanning missions.jsonl. Built lazily on first
	// fallback hit.
	var ticketMap map[string]string

	var notices []string
	buildTicketMap := func() {
		if ticketMap != nil {
			return
		}
		ticketMap = map[string]string{}
		rows, ns := s.readMissionsJSONL()
		notices = append(notices, ns...)
		for _, row := range rows {
			if row.Ticket != "" {
				ticketMap[row.Ticket] = row.ID
			}
		}
	}

	lookupCommit := func(sha string) *commitInfo {
		if c, ok := commitCache[sha]; ok {
			return c
		}
		c := &commitInfo{}

		// Try trailers first.
		var lookupErr error // the first git failure for this commit
		out, err := ask("log", "--format=%(trailers:key=Mission,valueonly)%n%(trailers:key=Delegation,valueonly)", "-1", sha)
		if err != nil {
			lookupErr = err
		} else {
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			if len(lines) >= 1 {
				c.mission = strings.TrimSpace(lines[0])
			}
			if len(lines) >= 2 {
				c.delegation = strings.TrimSpace(lines[1])
			}
		}

		// Fallback: parse bead/ticket ID from commit subject, look
		// up the mission in missions.jsonl.
		if c.mission == "" {
			subj, err := ask("log", "--format=%s", "-1", sha)
			if err != nil {
				if lookupErr == nil {
					lookupErr = err
				}
			} else {
				subject := string(subj)
				// Match ethos-XXXX or punt-labs-XXXX in parens at end of subject.
				for _, candidate := range extractTicketIDs(subject) {
					buildTicketMap()
					if mid, ok := ticketMap[candidate]; ok {
						c.mission = mid
						// Find the first delegation under this mission.
						var derr error
						c.delegation, derr = s.firstDelegation(mid)
						if derr != nil && lookupErr == nil {
							lookupErr = derr
						}
						break
					}
				}
			}
		}

		if lookupErr != nil {
			fail(lookupErr)
		}

		// Cached even when git failed: for the rest of this request (this call)
		// a failed commit is not asked about again. The next request asks anew.
		commitCache[sha] = c
		return c
	}

	// Build blame lines with delegation info.
	badAgents := 0
	var firstAgentErr error
	var result []blameLine
	for _, h := range hunks {
		bl := blameLine{
			Num:    h.line,
			Text:   h.text,
			Author: h.author,
			Commit: h.commit,
		}
		ci := lookupCommit(h.commit)
		if ci.delegation != "" {
			bl.Mission = ci.mission
			bl.Delegation = ci.delegation
			agent, aerr := s.delegationAgent(ci.mission, ci.delegation)
			if aerr != nil {
				if badAgents == 0 {
					firstAgentErr = aerr
				}
				badAgents++
			}
			bl.Agent = agent
		} else if ci.mission != "" {
			bl.Mission = ci.mission
			bl.Agent = "mission"
		}
		result = append(result, bl)
	}
	if failedLookups > 0 {
		notices = append(notices, fmt.Sprintf("%d commit lookup(s) could not be looked up, so their lines are shown without a mission: %v", failedLookups, firstLookupErr))
	}
	if badAgents > 0 {
		notices = append(notices, fmt.Sprintf("%d delegation record(s) could not be read, so those lines show no agent: %v", badAgents, firstAgentErr))
	}
	return result, notices
}

// delegationAgent reads the agent_type from a delegation record.
func (s *Server) delegationAgent(missionID, delegationID string) (string, error) {
	if missionID == "" || delegationID == "" {
		return "", nil
	}
	recordPath := filepath.Join(
		s.storeRoot, ".punt-labs", "ethos", "missions",
		missionID, "delegations", delegationID, "record.yaml",
	)
	d, err := loadDelegationYAML(recordPath)
	if err != nil {
		return "", fmt.Errorf("delegation %s of %s: %w", delegationID, missionID, err)
	}
	return d, nil
}

// extractTicketIDs finds bead-shaped IDs in a commit subject.
// Patterns: "ethos-ozrb", "punt-labs-tzi", "(ethos-ozrb)" at end.
func extractTicketIDs(subject string) []string {
	var ids []string
	// Split on non-alphanumeric-dash chars and check each token.
	for _, word := range strings.FieldsFunc(subject, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-')
	}) {
		// Match patterns like "ethos-XXXX" or "punt-labs-XXXX".
		if strings.Contains(word, "-") && len(word) >= 6 {
			ids = append(ids, word)
		}
	}
	return ids
}

// firstDelegation returns the first delegation ID under a mission dir.
func (s *Server) firstDelegation(missionID string) (string, error) {
	dir := filepath.Join(s.storeRoot, ".punt-labs", "ethos", "missions", missionID, "delegations")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil // a mission with no delegations yet
	}
	if err != nil {
		return "", fmt.Errorf("reading the delegations of %s: %w", missionID, err)
	}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			return e.Name(), nil
		}
	}
	return "", nil
}

// loadDelegationYAML reads just the agent_type from record.yaml
// without importing the mission package (avoids circular deps).
func loadDelegationYAML(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "agent_type:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "agent_type:")), nil
		}
	}
	return "", nil
}
