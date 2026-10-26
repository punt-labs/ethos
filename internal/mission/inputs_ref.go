package mission

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// inputsRefPattern matches one ${inputs.X} substitution marker. X is
// a dot-separated path of identifier segments (ticket, files.0,
// references.1, etc.). The whole match is replaced; multiple markers
// in one entry are resolved left to right.
var inputsRefPattern = regexp.MustCompile(`\$\{inputs\.([a-zA-Z0-9_.]+)\}`)

// SubstituteInputs replaces every ${inputs.X} marker in s with the
// value of the named field of contract.Inputs. Supported X values:
//
//   - ticket            -> Inputs.Ticket
//   - files.N           -> Inputs.Files[N]
//   - references.N      -> Inputs.References[N]
//
// Unknown X, out-of-range N, or a malformed marker returns an error
// — the caller marks the predicate unevaluable and the strict-fail
// policy decides block vs warn.
func SubstituteInputs(s string, contract *Contract) (string, error) {
	matches := inputsRefPattern.FindAllStringSubmatchIndex(s, -1)
	if matches == nil {
		// No ${inputs.X} markers. If the string still carries a "${"
		// it must be an unsupported substitution form (${env.X},
		// ${vars.Y}). Refuse rather than smuggle the literal through.
		if strings.Contains(s, "${") {
			return "", fmt.Errorf("unsupported substitution in %q", s)
		}
		return s, nil
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		b.WriteString(s[last:m[0]])
		key := s[m[2]:m[3]]
		val, err := lookupInput(key, contract)
		if err != nil {
			return "", err
		}
		b.WriteString(val)
		last = m[1]
	}
	b.WriteString(s[last:])
	// Catch a stray ${ that survived after the last ${inputs.X}.
	if strings.Contains(b.String(), "${") {
		return "", fmt.Errorf("unsupported substitution in %q", s)
	}
	return b.String(), nil
}

// lookupInput resolves a single ${inputs.X} key against
// contract.Inputs. The supported key shapes are documented on
// SubstituteInputs; this helper is the single dispatch table so the
// substitution rules are visible in one place.
func lookupInput(key string, contract *Contract) (string, error) {
	if contract == nil {
		return "", fmt.Errorf("inputs.%s: contract is nil", key)
	}
	switch {
	case key == "ticket":
		if contract.Inputs.Ticket == "" {
			return "", fmt.Errorf("inputs.ticket: not set")
		}
		return contract.Inputs.Ticket, nil
	case strings.HasPrefix(key, "files."):
		return lookupIndexed("files", key[len("files."):], contract.Inputs.Files)
	case strings.HasPrefix(key, "references."):
		return lookupIndexed("references", key[len("references."):], contract.Inputs.References)
	default:
		return "", fmt.Errorf("inputs.%s: unknown input key", key)
	}
}

// lookupIndexed parses an integer index and returns the n-th entry
// of arr. The caller passes the field name (e.g. "files") so the
// error message names the slice the index missed.
func lookupIndexed(field, idx string, arr []string) (string, error) {
	n, err := strconv.Atoi(idx)
	if err != nil {
		return "", fmt.Errorf("inputs.%s.%s: not an integer", field, idx)
	}
	if n < 0 || n >= len(arr) {
		return "", fmt.Errorf("inputs.%s.%d: index out of range [0,%d)", field, n, len(arr))
	}
	if arr[n] == "" {
		return "", fmt.Errorf("inputs.%s.%d: empty value", field, n)
	}
	return arr[n], nil
}
