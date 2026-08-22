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
	path := mission.RepoStatePath(s.storeRoot, "missions.jsonl")
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
	Title            string
	Contract         *mission.Contract
	Delegations      []*mission.Delegation
	Results          []mission.Result
	ResultsError     string
	Corrections      []mission.Correction
	CorrectionsError string
	Events           []mission.Event
	EventsError      string
	EventsWarnings   []string
	AuditEntries     []hook.AuditView
	AuditCount       int
}

// missionStore builds the mission store the UI reads from. The record
// (contract, results) resolves under the store root, but the DES-058 event
// union is the per-checkout audit concern — it lives under the work tree
// where the events were appended and sealed. Wiring the checkout root
// (s.repoRoot, the work-tree root per CR#2) keeps the mission timeline
// non-empty when the UI runs from a linked worktree (Bugbot HIGH #370 class,
// one layer up).
func (s *Server) missionStore() *mission.Store {
	return mission.NewStoreWithRoots(s.storeRoot, s.globalRoot).
		WithCheckoutRoot(s.repoRoot)
}

func (s *Server) handleMission(w http.ResponseWriter, r *http.Request) {
	id := filepath.Base(strings.TrimPrefix(r.URL.Path, "/missions/"))
	if id == "" || id == "." || id == ".." {
		http.NotFound(w, r)
		return
	}

	store := s.missionStore()
	c, err := store.Load(id)
	if err != nil {
		http.Error(w, fmt.Sprintf("mission %q: %v", id, err), 404)
		return
	}

	delegations := s.loadDelegations(id)
	results, resultsErr := s.loadResults(store, id)
	events, warnings, eventsErr := s.loadEvents(store, id)

	// Corrections are a "correct" event under the same log the Events
	// section renders (DES-072) — deriving them from the events slice
	// already in hand avoids a second LoadEvents call (and a second
	// disk read + JSON decode) for the same mission log.
	var corrections []mission.Correction
	if eventsErr == nil {
		corrections = mission.CorrectionsFromEvents(id, events)
	}

	// Aggregate audit entries across all delegations under this mission.
	var allAudit []hook.AuditView
	for _, d := range delegations {
		entries, _ := hook.QueryAuditByDelegation(s.repoRoot, s.globalSessionsDir, d.ID)
		allAudit = append(allAudit, entries...)
	}
	sort.Slice(allAudit, func(i, j int) bool { return allAudit[i].Ts < allAudit[j].Ts })

	data := missionData{
		Title:          id,
		Contract:       c,
		Delegations:    delegations,
		Results:        results,
		Corrections:    corrections,
		Events:         events,
		EventsWarnings: warnings,
		AuditEntries:   allAudit,
		AuditCount:     len(allAudit),
	}
	if resultsErr != nil {
		data.ResultsError = resultsErr.Error()
	}
	if eventsErr != nil {
		data.EventsError = eventsErr.Error()
		// The Corrections section reads from the same event log; a
		// failure there is the same integrity signal, not a separate
		// one, so it is surfaced through CorrectionsError too rather
		// than left blank while Events shows the warning.
		data.CorrectionsError = eventsErr.Error()
	}
	if err := s.tmpl.ExecuteTemplate(w, "mission.html", data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func (s *Server) loadDelegations(missionID string) []*mission.Delegation {
	dir := mission.RepoStatePath(s.storeRoot, "missions", missionID, "delegations")
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

// loadResults reads the mission's result log. The error return is
// surfaced to the caller rather than swallowed: a failure here means
// the dashboard cannot prove the mission has no results, which is
// exactly the kind of integrity signal this page exists to show.
func (s *Server) loadResults(store *mission.Store, id string) ([]mission.Result, error) {
	results, err := store.LoadResults(id)
	if err != nil {
		return nil, fmt.Errorf("loading results for mission %q: %w", id, err)
	}
	return results, nil
}

// loadEvents reads the DES-058 event union for id. The error return is
// surfaced to the caller rather than swallowed: a failure here means
// the dashboard cannot prove the mission has no events, which is
// exactly the kind of integrity signal this page exists to show. The
// warnings return is likewise surfaced rather than discarded: some
// JSONL lines can fail to decode without the whole load failing, and a
// mission log that is partially unreadable is not the same as one that
// is fully healthy — see EventsWarnings on missionData.
func (s *Server) loadEvents(store *mission.Store, id string) ([]mission.Event, []string, error) {
	events, warnings, err := store.LoadEvents(id)
	if err != nil {
		return nil, nil, fmt.Errorf("loading events for mission %q: %w", id, err)
	}
	return events, warnings, nil
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
	missionID, delegationID := filepath.Base(parts[0]), filepath.Base(parts[1])
	if missionID == "." || missionID == ".." || delegationID == "." || delegationID == ".." {
		http.NotFound(w, r)
		return
	}

	dir := mission.RepoStatePath(s.storeRoot, "missions", missionID, "delegations", delegationID)
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
