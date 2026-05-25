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
type Server struct {
	repoRoot          string
	globalRoot        string
	globalSessionsDir string
	tmpl              *template.Template
	mux               *http.ServeMux
}

// NewServer creates a UI server reading data from repoRoot.
func NewServer(repoRoot string) (*Server, error) {
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
