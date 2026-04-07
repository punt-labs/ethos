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
