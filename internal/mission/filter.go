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
