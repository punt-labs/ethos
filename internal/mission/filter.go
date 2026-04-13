package mission

import "fmt"

// StatusMatches reports whether contractStatus passes the filter.
//
// Empty filter and "all" match any status. Any other filter is
// required to match exactly. Shared by the CLI's `mission list`
// command and the MCP tool's list method so the two surfaces stay in
// lockstep — one implementation, one set of tests.
func StatusMatches(filter, contractStatus string) bool {
	if filter == "" || filter == "all" {
		return true
	}
	return filter == contractStatus
}

// IsValidStatusFilter reports whether filter is one of the accepted
// list filter values: "all" (match any), or one of the Status*
// constants. Used by CLI and MCP handlers to reject unknown filters
// loudly instead of silently returning an empty list.
func IsValidStatusFilter(filter string) bool {
	switch filter {
	case StatusOpen, StatusClosed, StatusFailed, StatusEscalated, "all":
		return true
	}
	return false
}

// ListEntry is the projection of a Contract used by the `mission list`
// command and the MCP list handler. Both surfaces return the same
// shape, so the type lives here to keep them in lockstep — an update
// to the fields displayed to a user lands in one place instead of
// two identical copies drifting over time.
//
// The struct is deliberately a projection, not the full Contract: the
// list view is a summary of each mission's identity and assignment,
// not its full write_set / success criteria / inputs. Callers who
// want the full contract read it via Show.
type ListEntry struct {
	MissionID string `json:"mission_id"`
	Status    string `json:"status"`
	Leader    string `json:"leader"`
	Worker    string `json:"worker"`
	Evaluator string `json:"evaluator"`
	CreatedAt string `json:"created_at"`
}

// NewListEntry projects a Contract into the list-view summary. Used
// by the CLI runMissionList and the MCP handleListMissions so both
// entry points build their list entries the same way.
func NewListEntry(c *Contract) ListEntry {
	return ListEntry{
		MissionID: c.MissionID,
		Status:    c.Status,
		Leader:    c.Leader,
		Worker:    c.Worker,
		Evaluator: c.Evaluator.Handle,
		CreatedAt: c.CreatedAt,
	}
}

// TopoSortContracts returns contracts ordered so that each contract
// appears after any of its DependsOn entries that are also present in
// the input slice. Dependencies outside the input slice are treated as
// already satisfied (they are not in scope for this view). If the
// remaining contracts form a cycle, they are appended at the end in
// their original order.
func TopoSortContracts(contracts []*Contract) ([]*Contract, []string) {
	if len(contracts) <= 1 {
		return contracts, nil
	}

	idxByID := make(map[string]int, len(contracts))
	for i, c := range contracts {
		idxByID[c.MissionID] = i
	}

	emitted := make(map[string]bool, len(contracts))
	var sorted []*Contract
	var warnings []string

	remaining := make([]int, len(contracts))
	for i := range remaining {
		remaining[i] = i
	}

	for len(remaining) > 0 {
		progress := false
		var pending []int
		for _, idx := range remaining {
			c := contracts[idx]
			ready := true
			for _, dep := range c.DependsOn {
				if _, inSet := idxByID[dep]; inSet && !emitted[dep] {
					ready = false
					break
				}
			}
			if ready {
				sorted = append(sorted, c)
				emitted[c.MissionID] = true
				progress = true
			} else {
				pending = append(pending, idx)
			}
		}
		if !progress {
			for _, idx := range pending {
				sorted = append(sorted, contracts[idx])
				warnings = append(warnings,
					fmt.Sprintf("mission %s has unresolved dependencies", contracts[idx].MissionID))
			}
			break
		}
		remaining = pending
	}
	return sorted, warnings
}
