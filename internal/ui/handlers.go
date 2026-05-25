package ui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/internal/hook"
	"github.com/punt-labs/ethos/internal/mission"
)

type dashboardData struct {
	Title    string
	Counts   []countCard
	Missions []missionRow
}

type countCard struct {
	Label string
	N     int
}

type missionRow struct {
	ID        string
	Status    string
	Worker    string
	Evaluator string
	CreatedAt string
	Verdict   string
	Ticket    string
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	rows := s.readMissionsJSONL()

	counts := map[string]int{}
	for _, row := range rows {
		counts[row.Status]++
	}

	cards := []countCard{
		{Label: "Total", N: len(rows)},
		{Label: "Closed", N: counts["closed"]},
		{Label: "Open", N: counts["open"]},
		{Label: "Failed", N: counts["failed"]},
	}
	if counts["escalated"] > 0 {
		cards = append(cards, countCard{Label: "Escalated", N: counts["escalated"]})
	}

	recent := rows
	if len(recent) > 30 {
		recent = recent[len(recent)-30:]
	}
	// Reverse so most recent is first.
	for i, j := 0, len(recent)-1; i < j; i, j = i+1, j-1 {
		recent[i], recent[j] = recent[j], recent[i]
	}

	data := dashboardData{
		Title:    "Dashboard",
		Counts:   cards,
		Missions: recent,
	}
	if err := s.tmpl.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func (s *Server) readMissionsJSONL() []missionRow {
	path := mission.RepoStatePath(s.repoRoot, "missions.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var rows []missionRow
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var entry struct {
			ID        string `json:"id"`
			Status    string `json:"status"`
			Worker    string `json:"worker"`
			Evaluator string `json:"evaluator"`
			CreatedAt string `json:"created_at"`
			Verdict   string `json:"verdict"`
			Ticket    string `json:"ticket"`
		}
		if err := json.Unmarshal(sc.Bytes(), &entry); err != nil {
			continue
		}
		rows = append(rows, missionRow{
			ID:        entry.ID,
			Status:    entry.Status,
			Worker:    entry.Worker,
			Evaluator: entry.Evaluator,
			CreatedAt: entry.CreatedAt,
			Verdict:   entry.Verdict,
			Ticket:    entry.Ticket,
		})
	}
	return rows
}

type missionData struct {
	Title        string
	Contract     *mission.Contract
	Delegations  []*mission.Delegation
	Results      []mission.Result
	Events       []mission.Event
	AuditEntries []hook.AuditView
	AuditCount   int
}

func (s *Server) handleMission(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/missions/")
	if id == "" {
		http.NotFound(w, r)
		return
	}

	store := mission.NewStoreWithRoots(s.repoRoot, s.globalRoot)
	c, err := store.Load(id)
	if err != nil {
		http.Error(w, fmt.Sprintf("mission %q: %v", id, err), 404)
		return
	}

	delegations := s.loadDelegations(id)
	results := s.loadResults(store, id)
	events := s.loadEvents(store, id)

	// Aggregate audit entries across all delegations under this mission.
	var allAudit []hook.AuditView
	for _, d := range delegations {
		entries, _ := hook.QueryAuditByDelegation(s.repoRoot, s.globalSessionsDir, d.ID)
		allAudit = append(allAudit, entries...)
	}
	sort.Slice(allAudit, func(i, j int) bool { return allAudit[i].Ts < allAudit[j].Ts })

	data := missionData{
		Title:        id,
		Contract:     c,
		Delegations:  delegations,
		Results:      results,
		Events:       events,
		AuditEntries: allAudit,
		AuditCount:   len(allAudit),
	}
	if err := s.tmpl.ExecuteTemplate(w, "mission.html", data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func (s *Server) loadDelegations(missionID string) []*mission.Delegation {
	dir := mission.RepoStatePath(s.repoRoot, "missions", missionID, "delegations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var delegations []*mission.Delegation
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		recordPath := filepath.Join(dir, e.Name(), "record.yaml")
		d, loadErr := mission.LoadDelegation(recordPath)
		if loadErr != nil {
			continue
		}
		d.Mission = missionID
		delegations = append(delegations, d)
	}
	return delegations
}

func (s *Server) loadResults(store *mission.Store, id string) []mission.Result {
	results, err := store.LoadResults(id)
	if err != nil {
		return nil
	}
	return results
}

func (s *Server) loadEvents(store *mission.Store, id string) []mission.Event {
	events, _, err := store.LoadEvents(id)
	if err != nil {
		return nil
	}
	return events
}

type delegationData struct {
	Title        string
	MissionID    string
	Delegation   *mission.Delegation
	Prompt       string
	AuditEntries []hook.AuditView
	AuditCount   int
}

func (s *Server) handleDelegation(w http.ResponseWriter, r *http.Request) {
	// URL: /delegations/{missionID}/{delegationID}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/delegations/"), "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	missionID, delegationID := parts[0], parts[1]

	dir := mission.RepoStatePath(s.repoRoot, "missions", missionID, "delegations", delegationID)
	d, err := mission.LoadDelegation(filepath.Join(dir, "record.yaml"))
	if err != nil {
		http.Error(w, fmt.Sprintf("delegation %q: %v", delegationID, err), 404)
		return
	}
	d.Mission = missionID

	prompt := ""
	if data, readErr := os.ReadFile(filepath.Join(dir, "prompt.md")); readErr == nil {
		prompt = string(data)
	}

	auditEntries, _ := hook.QueryAuditByDelegation(s.repoRoot, s.globalSessionsDir, delegationID)
	sort.Slice(auditEntries, func(i, j int) bool { return auditEntries[i].Ts < auditEntries[j].Ts })

	data := delegationData{
		Title:        delegationID,
		MissionID:    missionID,
		Delegation:   d,
		Prompt:       prompt,
		AuditEntries: auditEntries,
		AuditCount:   len(auditEntries),
	}
	if err := s.tmpl.ExecuteTemplate(w, "delegation.html", data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}
