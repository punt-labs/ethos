package mission

// This file carries NO build constraint on purpose.
//
// globMeta is shared by the path matcher in conflict.go (which is
// //go:build !windows) and the per-entry validator in validate.go
// (which is not). Defining it beside the matcher made validate.go
// depend on a !windows-only symbol, so validate.go stopped compiling
// under GOOS=windows — invisible to a darwin/linux `make check`
// (Copilot on PR #415). A constant with no platform behavior belongs
// in a file with no platform constraint.

// globMeta is the set of characters that make a write_set segment a
// pattern rather than a literal name. One definition serves the
// matcher and the validator: a validator that recognized fewer
// characters would admit an entry the matcher then reads as a
// wildcard.
const globMeta = "*?[]"
