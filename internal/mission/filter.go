package mission

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
