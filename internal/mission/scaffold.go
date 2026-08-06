package mission

// ScaffoldContractYAML returns a fillable-in-place YAML skeleton for
// `ethos mission create --file`. Every field DecodeContractStrict and
// Contract.Validate require is present with a placeholder value and a
// comment naming its type and purpose, so a caller can go from zero
// to a valid contract by editing values in place — never by
// discovering a required field one unmarshal error at a time
// (ethos-q682).
//
// Fields the store computes itself (mission_id, status, created_at,
// updated_at, closed_at, evaluator.pinned_at, evaluator.hash,
// current_round) are omitted: `mission create` overwrites them
// unconditionally (see runMissionCreate / Store.ApplyServerFields),
// so listing them here would only invite an operator to fill in a
// value that is discarded. Advanced fields that most contracts never
// need (pipeline, depends_on, extract_into, tools, preconditions,
// delegations, session, repo) are named in the trailing comment
// rather than scaffolded — see `ethos mission create --help` for
// their shape.
//
// The placeholder values are individually well-formed: write_set,
// success_criteria, and budget.rounds pass Contract.Validate as
// written. worker and evaluator.handle differ, satisfying the
// self-verification guard (Store.checkSelfVerification). Only the
// CHANGE_ME markers need editing before `mission create` will accept
// the result — see scaffold_test.go for the exact round-trip this
// guarantees.
func ScaffoldContractYAML(leader string) string {
	return `# ethos mission contract — fill in the CHANGE_ME values, then run:
#   ethos mission create --file <this-file>
#
# Fields not shown here (mission_id, status, created_at, updated_at,
# closed_at, evaluator.pinned_at, evaluator.hash, current_round) are
# server-controlled: "mission create" overwrites them regardless of
# what this file supplies. Advanced fields not shown here (pipeline,
# depends_on, extract_into, tools, preconditions, delegations,
# session, repo) are documented in "ethos mission create --help".

leader: ` + leader + `              # string, required — handle of the delegating leader
worker: CHANGE_ME           # string, required — handle of the specialist doing the work
evaluator:
  handle: CHANGE_ME_EVAL    # string, required — reviewer handle; must differ from worker

write_set:                  # list of strings, required — at least one entry;
  - path/to/file.go         #   relative path (file or directory) this mission may create/modify

success_criteria:                          # list of strings, required — at least one entry
  - "CHANGE_ME — e.g. make check passes"

budget:
  rounds: 2                    # int, required — round budget, 1-10
  reflection_after_each: true  # bool, required — require a reflection after every round

context: ""                 # string, optional — free-text context for the worker

inputs:                     # optional — what the worker should read before starting
  ticket: ""                 # string, optional — bead/ticket ID
  files: []                  # list of strings, optional — files the worker MUST read
  references: []             # list of strings, optional — supporting docs the worker MAY consult
`
}

// ScaffoldResultYAML returns a fillable-in-place YAML skeleton for
// `ethos mission result --file`. Every field DecodeResultStrict and
// Result.Validate require is present with a placeholder value and a
// comment naming its type and purpose — including the shape of
// evidence entries (name + status), the class of friction ethos-q682
// reports being discovered one unmarshal error at a time (mission,
// round, author, evidence non-empty, EvidenceCheck shape).
//
// The placeholder mission ID matches the m-YYYY-MM-DD-NNN pattern and
// round/verdict/confidence/evidence are individually well-formed, so
// only the CHANGE_ME markers and the mission/round values need
// editing to match the real mission before `mission result` will
// accept the result — see scaffold_test.go for the exact round-trip
// this guarantees.
func ScaffoldResultYAML() string {
	return `# ethos mission result — fill in the CHANGE_ME values (and mission,
# round below) to match your mission, then run:
#   ethos mission result <id> --file <this-file>
#
# Submitted once per round; a second submission for the same round is
# refused. See "ethos mission result --help" for the escalate shape
# and the --verify flag.

mission: m-2026-01-01-001   # string, required — mission ID this result covers (m-YYYY-MM-DD-NNN); must match the target mission
round: 1                    # int, required — round this result covers; must equal the mission's current round
author: CHANGE_ME           # string, required — handle of the worker submitting this result

verdict: pass                # string, required — one of: pass, fail, escalate
confidence: 0.9              # float, required — calibrated confidence in [0.0, 1.0]

files_changed:                 # list, optional — omit or leave empty when the round changed no files
  - path: path/to/file.go     #   string, required — must live inside the contract's write_set
    added: 0                  #   int, required — lines added (non-negative)
    removed: 0                #   int, required — lines removed (non-negative)

evidence:                                    # list, required — at least one entry
  - name: "CHANGE_ME — e.g. make check"      #   string, required — short label for the check you ran
    status: pass                             #   string, required — one of: pass, fail, skip

# open_questions:            # list of strings, optional — ambiguity for the leader to resolve
#   - "example open question"

# prose: |                   # string, optional — human-facing narrative, never read by ethos
#   Multi-line prose goes here.
`
}
