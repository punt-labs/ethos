// Package adr defines typed Architecture Decision Records (ADRs) with schema
// validation, lifecycle status, and links to missions and beads.
package adr

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Status values for an ADR.
const (
	StatusProposed   = "proposed"
	StatusSettled    = "settled"
	StatusSuperseded = "superseded"
)

// validStatuses lists the three allowed Status values.
var validStatuses = map[string]bool{
	StatusProposed:   true,
	StatusSettled:    true,
	StatusSuperseded: true,
}

// validStatusFilters lists the allowed values for the list --status flag.
var validStatusFilters = map[string]bool{
	StatusProposed:   true,
	StatusSettled:    true,
	StatusSuperseded: true,
	"all":            true,
	"":               true,
}

// ValidateStatusFilter returns an error if filter is not a recognized status filter value.
func ValidateStatusFilter(filter string) error {
	if !validStatusFilters[filter] {
		return fmt.Errorf("invalid status filter %q: valid values are proposed, settled, superseded, all", filter)
	}
	return nil
}

// idPattern enforces the DES-NNN format.
var idPattern = regexp.MustCompile(`^DES-\d{3}$`)

// ADR is a typed Architecture Decision Record.
type ADR struct {
	ID           string   `yaml:"id" json:"id"`
	Title        string   `yaml:"title" json:"title"`
	Status       string   `yaml:"status" json:"status"`
	CreatedAt    string   `yaml:"created_at" json:"created_at"`
	UpdatedAt    string   `yaml:"updated_at" json:"updated_at"`
	Author       string   `yaml:"author" json:"author"`
	Context      string   `yaml:"context" json:"context"`
	Decision     string   `yaml:"decision" json:"decision"`
	Alternatives []string `yaml:"alternatives,omitempty" json:"alternatives,omitempty"`
	MissionID    string   `yaml:"mission_id,omitempty" json:"mission_id,omitempty"`
	BeadID       string   `yaml:"bead_id,omitempty" json:"bead_id,omitempty"`
}

// Validate checks that an ADR is well-formed.
func (a *ADR) Validate() error {
	if a == nil {
		return fmt.Errorf("ADR is nil")
	}
	if !idPattern.MatchString(a.ID) {
		return fmt.Errorf("invalid ADR id %q: must match DES-NNN", a.ID)
	}
	if strings.TrimSpace(a.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if !validStatuses[a.Status] {
		return fmt.Errorf("invalid status %q: must be one of proposed, settled, superseded", a.Status)
	}
	if strings.TrimSpace(a.Decision) == "" {
		return fmt.Errorf("decision is required")
	}
	if _, err := time.Parse(time.RFC3339, a.CreatedAt); err != nil {
		return fmt.Errorf("invalid created_at %q: %w", a.CreatedAt, err)
	}
	if _, err := time.Parse(time.RFC3339, a.UpdatedAt); err != nil {
		return fmt.Errorf("invalid updated_at %q: %w", a.UpdatedAt, err)
	}
	return nil
}
