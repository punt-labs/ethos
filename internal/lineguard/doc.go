// Package lineguard is the audit line guard and the tool-input redaction it
// rests on: the check at the sink of every audit appender, the seal's and
// quarantine's legacy-line guards, and the hashing and preview of a stored
// tool_input. It is a leaf below internal/hook and internal/autorepair, which
// both import it; it imports neither, so the engine can be wired into the
// session-start hook without a cycle.
package lineguard
