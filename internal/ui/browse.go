package ui

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type browseData struct {
	Title   string
	Path    string
	IsDir   bool
	Parent  string
	Entries []dirEntry
	Lines   []blameLine
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

	// Safety: don't escape the repo root.
	abs, err := filepath.Abs(absPath)
	if err != nil || !strings.HasPrefix(abs, s.repoRoot) {
		http.Error(w, "path outside repo", 403)
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
	blameData := s.gitBlameWithTrailers(relPath)

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
		Title: filepath.Base(relPath),
		Path:  relPath,
		IsDir: false,
		Lines: blameData,
	}
	if err := s.tmpl.ExecuteTemplate(w, "browse.html", data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func (s *Server) gitBlameWithTrailers(relPath string) []blameLine {
	cmd := exec.Command("git", "blame", "--porcelain", relPath)
	cmd.Dir = s.repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil
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

	// Cache trailer lookups per commit.
	type trailerInfo struct {
		mission    string
		delegation string
	}
	trailerCache := map[string]*trailerInfo{}

	lookupTrailers := func(sha string) *trailerInfo {
		if t, ok := trailerCache[sha]; ok {
			return t
		}
		t := &trailerInfo{}
		cmd := exec.Command("git", "log", "--format=%(trailers:key=Mission,valueonly)%n%(trailers:key=Delegation,valueonly)", "-1", sha)
		cmd.Dir = s.repoRoot
		out, err := cmd.Output()
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			if len(lines) >= 1 {
				t.mission = strings.TrimSpace(lines[0])
			}
			if len(lines) >= 2 {
				t.delegation = strings.TrimSpace(lines[1])
			}
		}
		trailerCache[sha] = t
		return t
	}

	// Build blame lines with delegation info.
	var result []blameLine
	for _, h := range hunks {
		bl := blameLine{
			Num:    h.line,
			Text:   h.text,
			Author: h.author,
			Commit: h.commit,
		}
		t := lookupTrailers(h.commit)
		if t.delegation != "" {
			bl.Mission = t.mission
			bl.Delegation = t.delegation
			// Derive agent name from the delegation record if possible.
			bl.Agent = s.delegationAgent(t.mission, t.delegation)
		}
		result = append(result, bl)
	}
	return result
}

// delegationAgent reads the agent_type from a delegation record.
func (s *Server) delegationAgent(missionID, delegationID string) string {
	if missionID == "" || delegationID == "" {
		return ""
	}
	recordPath := filepath.Join(
		s.repoRoot, ".punt-labs", "ethos", "missions",
		missionID, "delegations", delegationID, "record.yaml",
	)
	d, err := loadDelegationYAML(recordPath)
	if err != nil {
		return ""
	}
	return d
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
