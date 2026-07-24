// Package ui provides a localhost web UI for ethos traceability data.
package ui

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

//go:embed templates/*.html
var templateFS embed.FS

// Server serves the traceability UI on localhost.
//
// repoRoot and storeRoot differ only inside a linked worktree. repoRoot is
// the current work tree — it backs the file browser, git blame, and audit
// lookups, whose data lives in the committing checkout. storeRoot is the
// repo that owns the .punt-labs/ethos mission store — it backs the mission
// and delegation reads, which in a worktree live in the main work tree
// (CR#2). A single root would show an empty dashboard from a worktree.
type Server struct {
	repoRoot          string
	storeRoot         string
	globalRoot        string
	globalSessionsDir string
	tmpl              *template.Template
	mux               *http.ServeMux
}

// NewServer creates a UI server. repoRoot is the current work tree (file
// browse, blame, audit); storeRoot is the repo whose mission store backs the
// mission and delegation reads (CR#2).
func NewServer(repoRoot, storeRoot string) (*Server, error) {
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving repo root: %w", err)
	}
	repoRoot = absRoot

	absStore, err := filepath.Abs(storeRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving store root: %w", err)
	}
	storeRoot = absStore

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("determining home directory: %w", err)
	}
	globalRoot := filepath.Join(home, ".punt-labs", "ethos")

	funcMap := template.FuncMap{
		"truncate": truncate,
		"join":     strings.Join,
	}

	tmpl, err := template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parsing templates: %w", err)
	}

	s := &Server{
		repoRoot:          repoRoot,
		storeRoot:         storeRoot,
		globalRoot:        globalRoot,
		globalSessionsDir: filepath.Join(globalRoot, "sessions"),
		tmpl:              tmpl,
		mux:               http.NewServeMux(),
	}
	s.mux.HandleFunc("/", s.handleDashboard)
	s.mux.HandleFunc("/missions/", s.handleMission)
	s.mux.HandleFunc("/delegations/", s.handleDelegation)
	s.mux.HandleFunc("/browse/", s.handleBrowse)
	return s, nil
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
