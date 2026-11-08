package ui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/provenance"
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
	Num          int
	Text         string
	Author       string
	Commit       string
	Mission      string
	Delegation   string
	Agent        string
	OriginalPath string
	OriginalLine int
	Candidates   []provenance.Association
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
		if err := sc.Err(); err != nil {
			notices = append(notices, fmt.Sprintf("reading %s stopped after line %d: %v; the rest is not shown", relPath, num-1, err))
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
	out, err := s.git("blame", "--line-porcelain", "--", relPath)
	if err != nil {
		return nil, []string{fmt.Sprintf("git blame failed, so the file is shown without attribution: %v", err)}
	}

	hunks, err := provenance.ParseBlame(out)
	if err != nil {
		return nil, []string{fmt.Sprintf("malformed git blame: %v", err)}
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
		manifests  []*provenance.Manifest
	}
	repository := provenance.Repository{Root: s.repoRoot, Env: gitenv.Parsed()}
	ctx := context.Background()
	commitCache := map[string]*commitInfo{}
	failedLookups := 0
	var firstLookupErr error
	fail := func(err error) {
		if failedLookups == 0 {
			firstLookupErr = err
		}
		failedLookups++
	}

	var notices []string

	lookupCommit := func(sha string) *commitInfo {
		if c, ok := commitCache[sha]; ok {
			return c
		}
		c := &commitInfo{}

		// Try trailers first.
		var lookupErr error // the first git failure for this commit
		out, err := ask("log", "--no-show-signature", "--format=%(trailers:key=Mission,valueonly,separator=%x1f)%n%(trailers:key=Delegation,valueonly,separator=%x1f)%n%(trailers:key=Ethos-Provenance,valueonly,separator=%x1f)", "-1", sha)
		if err != nil {
			lookupErr = err
		} else {
			lines := strings.Split(string(out), "\n")
			// Git emits three fields and a final newline even when no trailers
			// exist. Extra or missing lines are a failed lookup, not absence.
			if len(lines) != 4 || lines[3] != "" {
				lookupErr = fmt.Errorf("malformed git trailer response: expected three newline-terminated fields")
			} else {
				if !strings.Contains(lines[0], "\x1f") && !strings.Contains(lines[1], "\x1f") {
					c.mission = strings.TrimSpace(lines[0])
					c.delegation = strings.TrimSpace(lines[1])
				}
				if strings.TrimSpace(lines[2]) != "" {
					c.mission = ""
					c.delegation = ""
					for _, name := range strings.Split(strings.TrimSpace(lines[2]), "\x1f") {
						m, err := repository.VerifiedManifest(ctx, sha, name)
						if err != nil {
							lookupErr = err
							if errors.Is(err, gitenv.ErrTimeout) {
								down = true
							}
							break
						}
						c.manifests = append(c.manifests, m)
					}
				}
			}
		}

		if lookupErr != nil {
			fail(lookupErr)
			c = &commitInfo{}
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
	type agentKey struct{ commit, mission, delegation string }
	type agentResult struct {
		agent string
		err   error
	}
	agents := map[agentKey]agentResult{}
	blobs := map[string]string{}
	for _, h := range hunks {
		bl := blameLine{
			Num:          h.Final,
			Text:         h.Text,
			Author:       h.Author,
			Commit:       h.Commit,
			OriginalPath: h.Path,
			OriginalLine: h.Original,
		}
		ci := lookupCommit(h.Commit)
		if len(ci.manifests) > 0 && !down {
			key := h.Commit + ":" + h.Path
			blob, ok := blobs[key]
			var err error
			if !ok {
				var out []byte
				out, err = ask("rev-parse", key)
				blob = strings.TrimSpace(string(out))
				if err == nil {
					blobs[key] = blob
				}
			}
			if err != nil {
				fail(err)
			} else {
				seen := map[provenance.Association]bool{}
				for _, m := range ci.manifests {
					pairs, err := m.Lookup(h.Path, blob, h.Original)
					if err != nil {
						fail(err)
						bl.Candidates = nil
						break
					}
					for _, a := range pairs {
						if !seen[a] {
							seen[a] = true
							bl.Candidates = append(bl.Candidates, a)
						}
					}
				}
				if len(bl.Candidates) == 1 {
					ci = &commitInfo{mission: bl.Candidates[0].Mission, delegation: bl.Candidates[0].Delegation}
				} else if len(bl.Candidates) > 1 {
					bl.Agent = "candidates"
					notices = append(notices, fmt.Sprintf("line %d has overlapping mission/delegation candidates: %v", h.Final, bl.Candidates))
				} else {
					notices = append(notices, fmt.Sprintf("line %d has no unambiguous recorded mission/delegation association", h.Final))
				}
			}
		}
		if ci.mission != "" && ci.delegation != "" && !down {
			bl.Mission = ci.mission
			bl.Delegation = ci.delegation
			key := agentKey{h.Commit, ci.mission, ci.delegation}
			cached, ok := agents[key]
			if !ok {
				cached.agent, cached.err = repository.AgentAt(ctx, h.Commit, provenance.Association{Mission: ci.mission, Delegation: ci.delegation})
				agents[key] = cached
			}
			agent, aerr := cached.agent, cached.err
			if aerr != nil {
				if errors.Is(aerr, gitenv.ErrTimeout) {
					down = true
				}
				if badAgents == 0 {
					firstAgentErr = aerr
				}
				badAgents++
			}
			bl.Agent = agent
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
