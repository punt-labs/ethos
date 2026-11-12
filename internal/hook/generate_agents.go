package hook

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/agentmark"
	"github.com/punt-labs/ethos/v4/internal/bundle"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/pluginskill"
	"github.com/punt-labs/ethos/v4/internal/repomiss"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/role"
	"github.com/punt-labs/ethos/v4/internal/team"
)

// projectFilePatterns returns the shell case-statement glob pattern for
// file extensions that should trigger make check. Detection is by marker
// file at the repo root: go.mod → Go, pyproject.toml or setup.py →
// Python, neither → generic (Makefile, shell, YAML only).
// When both Go and Python markers exist, Go wins — a polyglot repo with
// go.mod is more likely Go-primary.
func projectFilePatterns(repoRoot string) string {
	goPatterns := "*.go|*go.mod|*go.sum|*go.work|*Makefile|*.sh|*.yaml|*.yml"
	pyPatterns := "*.py|*.pyi|*.toml|*uv.lock|*Makefile|*.sh|*.yaml|*.yml"
	genericPatterns := "*Makefile|*.sh|*.yaml|*.yml"

	if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err == nil {
		return goPatterns
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "pyproject.toml")); err == nil {
		return pyPatterns
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "setup.py")); err == nil {
		return pyPatterns
	}
	return genericPatterns
}

// GenerateAgentFiles creates .claude/agents/<handle>.md files from ethos
// identity, personality, writing-style, and role data. Skips the main
// agent (from repo config) and human identities. Writes are idempotent:
// files are only written when content differs.
//
// Single-root form: config and output resolve under the same root. Callers
// in a linked worktree, where the config lives in the shared store but the
// agent files belong to the checkout, use GenerateAgentFilesTo.
func GenerateAgentFiles(repoRoot string, identities identity.IdentityStore, teams *team.LayeredStore, roles *role.LayeredStore) error {
	return GenerateAgentFilesTo(repoRoot, repoRoot, identities, teams, roles)
}

// GenerateAgentFilesTo is GenerateAgentFiles with the config-read root and
// the output root split. configRoot resolves the repo config and thus the
// team/agent selection — the shared store (StoreRootIfAny), matching the
// team/role data the stores already resolve from main. destRoot is where the
// .claude/agents/ files are written and the project file-pattern detection
// runs — the current checkout (CheckoutRootIfAny), so a worktree
// generates agents into its OWN tree, where its Claude reads them and
// InstallAgentDefinitions writes (Bugbot HIGH on PR #370). They coincide
// outside a linked worktree.
func GenerateAgentFilesTo(configRoot, destRoot string, identities identity.IdentityStore, teams *team.LayeredStore, roles *role.LayeredStore) error {
	plan, planErr := PlanGeneratedAgentFiles(configRoot, destRoot, identities, teams, roles)
	reportAgentPlanProblems(plan)
	var failed []string
	for _, file := range plan.Files {
		path := filepath.Join(destRoot, ".claude", "agents", filepath.Base(file.Handle)+".md")
		if err := CheckAgentPath(destRoot, path); err != nil {
			fmt.Fprintf(os.Stderr, "ethos: generate-agents: %s: writing agent file: %v\n", file.Handle, err)
			failed = append(failed, file.Handle)
			continue
		}
		existing, readErr := os.ReadFile(path)
		changed, err := writeAgentFile(configRoot, destRoot, path, file.Content)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ethos: generate-agents: %s: writing agent file: %v\n", file.Handle, err)
			failed = append(failed, file.Handle)
			continue
		}
		if changed {
			if readErr == nil {
				oldLines := strings.Count(string(existing), "\n") + 1
				newLines := strings.Count(file.Content, "\n") + 1
				fmt.Fprintf(os.Stderr, "ethos: generate-agents: updated %s (net %+d lines, %d → %d)\n", filepath.Base(path), newLines-oldLines, oldLines, newLines)
			} else {
				fmt.Fprintf(os.Stderr, "ethos: generate-agents: wrote %s\n", filepath.Base(path))
			}
		}
	}
	if len(failed) > 0 {
		return errors.Join(planErr, fmt.Errorf("generated %d of %d expected agent files (failed: %s)", len(plan.Files)-len(failed), len(plan.Files), strings.Join(failed, ", ")))
	}
	return planErr
}

// AgentFile is the exact output of one agent writer, before installation.
type AgentFile struct {
	Handle  string
	Content string
}

// AgentPlan separates generator ownership from successfully rendered files.
// Files contains one effective output per handle; the last rendered membership wins.
// A failed or intentionally skipped generator member must not fall back to a stub.
// Owned is complete only when planning returns no error.
type AgentPlan struct {
	Files    []AgentFile
	Owned    map[string]bool
	Problems []string
}

// PlanGeneratedAgentFiles reads configuration and rendering inputs without
// writing agent files or migrating legacy identity fields.
func PlanGeneratedAgentFiles(configRoot, destRoot string, identities identity.IdentityStore, teams *team.LayeredStore, roles *role.LayeredStore) (AgentPlan, error) {
	if teams == nil || roles == nil {
		return AgentPlan{}, nil
	}
	cfg, err := resolve.ConfigOf(configRoot)
	if err != nil {
		return AgentPlan{}, fmt.Errorf("generate agents: %w", err)
	}
	return PlanGeneratedAgentFilesFromConfig(cfg, projectFilePatterns(destRoot), activeBundleDefaultSkills(configRoot), AgentIdentities(identities), teams, roles)
}

// AgentIdentityReader separates resolved rendering inputs from reference reads.
// Doctor can supply a nonmigrating reader without implementing identity writes.
type AgentIdentityReader interface {
	LoadAgentIdentity(string) (*identity.Identity, error)
	LoadAgentReference(string) (*identity.Identity, error)
}

type agentIdentities struct{ store identity.IdentityStore }

var _ AgentIdentityReader = agentIdentities{}

// AgentIdentities adapts an ordinary identity store for read-only planning.
// Attribute and extension resolution remain unchanged; legacy voice migration
// is a mutation and must not occur while computing a plan.
func AgentIdentities(s identity.IdentityStore) AgentIdentityReader {
	if s == nil {
		return nil
	}
	return agentIdentities{s}
}

func (s agentIdentities) LoadAgentIdentity(handle string) (*identity.Identity, error) {
	return s.store.Load(handle, identity.WithoutMigration())
}

func (s agentIdentities) LoadAgentReference(handle string) (*identity.Identity, error) {
	return s.store.Load(handle, identity.Reference(true), identity.WithoutMigration())
}

// PlanGeneratedAgentFilesFromConfig uses the caller's configuration snapshot,
// project patterns and bundle skills; it does not rediscover roots or reread
// configuration. Rendering is shared with generation, not a metadata approximation.
func PlanGeneratedAgentFilesFromConfig(cfg *resolve.RepoConfig, filePatterns string, bundleSkills []string, identities AgentIdentityReader, teams *team.LayeredStore, roles *role.LayeredStore) (AgentPlan, error) {
	plan := AgentPlan{Owned: make(map[string]bool)}
	if cfg == nil {
		return plan, nil // neither .punt-labs/ethos.yaml nor the legacy path exists
	}

	mainAgent := cfg.Agent
	teamName := cfg.Team
	if teamName == "" {
		return plan, nil // no team configured — nothing to generate
	}
	if teams == nil || roles == nil || identities == nil {
		return plan, fmt.Errorf("team %q is configured but agent rendering stores are unavailable", teamName)
	}

	t, err := teams.Load(teamName)
	if err != nil {
		return plan, fmt.Errorf("loading team %q: %w", teamName, err)
	}

	expected := make(map[string]bool)
	fileIndex := make(map[string]int)
	// Incomplete repository references count toward expected output even
	// though rendering cannot finish. Other skips remain in Problems for
	// diagnostics, preserving the generator's existing skip behavior.
	for _, m := range t.Members {
		if m.Identity == mainAgent {
			continue
		}
		agent, err := isAgentKindReader(identities, m.Identity)
		if err != nil {
			return plan, err
		}
		if !agent {
			continue
		}
		plan.Owned[m.Identity] = true

		id, err := identities.LoadAgentIdentity(m.Identity)
		if err != nil {
			plan.Problems = append(plan.Problems, fmt.Sprintf("%s: %v", m.Identity, err))
			// An incomplete repo-authoritative set is a fault, not a
			// by-design skip: the member IS on the team and DOES have a
			// personality, the repo just did not vendor everything it
			// references. Count it so the caller's error names it and the
			// run does not report success while silently producing fewer
			// agents than the team has (DES-057 Part A).
			//
			// Reference mode already established that this is an agent;
			// a human's missing attributes must not count as failed output.
			var incomplete *repomiss.ErrIncompleteRepoSet
			if errors.As(err, &incomplete) {
				expected[m.Identity] = true
			}
			continue
		}
		if id.Kind != "agent" {
			continue
		}
		// Load records an ext miss rather than failing, so a live session
		// is never bricked. Agent generation is the surface that must act
		// on it: an agent written without its memory wiring looks correct
		// and behaves as if it had amnesia (DES-057 Part A).
		if len(id.MissingExt) > 0 {
			plan.Problems = append(plan.Problems, fmt.Sprintf("%s: %v", m.Identity, repomiss.New(m.Identity, id.MissingExt)))
			expected[m.Identity] = true
			continue
		}

		r, err := roles.Load(m.Role)
		if err != nil {
			plan.Problems = append(plan.Problems, fmt.Sprintf("%s: role %s: %v", m.Identity, m.Role, err))
			continue
		}
		if len(normalizedAgentTools(r.Tools)) == 0 {
			continue
		}

		if id.PersonalityContent == "" {
			plan.Problems = append(plan.Problems, m.Identity+": no personality content")
			continue
		}

		expected[id.Handle] = true

		antiResps := deriveAntiResponsibilities(m.Role, t.Collaborations, roles)
		reportsTo := deriveReportsToTargetsReader(m.Role, t.Members, t.Collaborations, identities)
		skills := mergeSkills(id.Skills, bundleSkills)
		content := buildAgentFile(id, r, antiResps, reportsTo, filePatterns, skills)
		file := AgentFile{Handle: id.Handle, Content: content}
		if index, exists := fileIndex[id.Handle]; exists {
			// The old writer overwrote this destination for each supported
			// membership. Keep only its final bytes for both writers and doctor.
			plan.Files[index] = file
		} else {
			fileIndex[id.Handle] = len(plan.Files)
			plan.Files = append(plan.Files, file)
		}
	}

	if len(plan.Files) < len(expected) {
		var failedMembers []string
		for handle := range expected {
			if _, exists := fileIndex[handle]; !exists {
				failedMembers = append(failedMembers, handle)
			}
		}
		sort.Strings(failedMembers)
		return plan, fmt.Errorf("generated %d of %d expected agent files (failed: %s)",
			len(plan.Files), len(expected), strings.Join(failedMembers, ", "))
	}

	return plan, nil
}

// GeneratedAgentHandles returns the set of identity handles the
// SessionStart generator (GenerateAgentFilesTo) is responsible for:
// every agent-kind member of the configured team except the main
// session agent. InstallAgentDefinitions consults this set so it does
// not copy a legacy stub over a generated file.
//
// Ownership is by team membership and identity kind, not by whether
// generation succeeds. A member the generator skips for want of tools
// or personality is still the generator's handle; falling back to its
// stub would resurrect the copy-from-agents path DES-026 rejected.
//
// A nil team store or an unconfigured team yields an empty set — the
// installer then copies every stub, its behavior before this change.
func GeneratedAgentHandles(configRoot string, identities identity.IdentityStore, teams *team.LayeredStore) (map[string]bool, error) {
	if teams == nil {
		return nil, nil
	}
	cfg, err := resolve.ConfigOf(configRoot)
	if err != nil {
		return nil, fmt.Errorf("generated agent handles: %w", err)
	}
	if cfg == nil || cfg.Team == "" {
		return nil, nil
	}
	t, err := teams.Load(cfg.Team)
	if err != nil {
		return nil, fmt.Errorf("loading team %q: %w", cfg.Team, err)
	}
	owned := make(map[string]bool)
	reader := AgentIdentities(identities)
	for _, m := range t.Members {
		if m.Identity == cfg.Agent {
			continue
		}
		agent, err := isAgentKindReader(reader, m.Identity)
		if err != nil {
			return nil, err
		}
		if agent {
			owned[m.Identity] = true
		}
	}
	return owned, nil
}

// isAgentKindReader reports whether a handle names an agent, reading only the
// identity record. Reference mode skips attribute resolution, so it
// answers for an identity whose full load failed on missing references.
// An unreadable record is an ownership error, not a rendering skip: callers
// must refuse stub installation rather than treat the member as unowned.
func isAgentKindReader(identities AgentIdentityReader, handle string) (bool, error) {
	if identities == nil {
		return false, fmt.Errorf("classifying team member %q for generated agent ownership: identity store is unavailable", handle)
	}
	id, err := identities.LoadAgentReference(handle)
	if err != nil {
		return false, fmt.Errorf("classifying team member %q for generated agent ownership: %w", handle, err)
	}
	return id.Kind == "agent", nil
}

// antiResponsibility is a responsibility belonging to a role the agent
// reports to — i.e., something explicitly not the agent's job.
type antiResponsibility struct {
	Responsibility string
	TargetRole     string
}

// respNormalizer collapses every line-break character a YAML
// double-quoted scalar can legally contain down to a single space:
// LF, CRLF, bare CR, Unicode line separator U+2028, and Unicode
// paragraph separator U+2029. Caller collapses the resulting runs
// of whitespace and trims.
var respNormalizer = strings.NewReplacer(
	"\r\n", " ",
	"\n", " ",
	"\r", " ",
	"\u2028", " ",
	"\u2029", " ",
)

// normalizeResponsibility applies whitespace-only cleanup to a
// responsibility string: every line-break form becomes a space, runs
// of whitespace collapse to a single space, and outer whitespace is
// trimmed. The result is always a single line. Content is never
// otherwise rewritten — a responsibility string containing markdown
// metacharacters (e.g. a leading "- ") is the role author's choice
// and passes through verbatim.
func normalizeResponsibility(s string) string {
	s = respNormalizer.Replace(s)
	// strings.Fields splits on any Unicode whitespace run and drops
	// leading/trailing whitespace in one pass. Rejoining with a single
	// space collapses every internal run — tabs, double spaces, and
	// the indentation that follows a replaced newline.
	return strings.Join(strings.Fields(s), " ")
}

// deriveAntiResponsibilities walks the team's collaboration edges
// starting from roleName and returns the reports_to targets'
// responsibilities as a flat list in walk order. Each responsibility is
// normalized: embedded newlines collapse to spaces, surrounding
// whitespace is trimmed, and strings empty after normalization are
// dropped with a stderr warning. Targets whose role fails to load are
// also skipped with a warning. Non-reports_to edges from roleName are
// warned about and skipped — post-ethos-2z2 this branch only fires for
// collaborates_with and delegates_to, which are valid types at the
// structural level but are a semantic-level "not handled by the MVP"
// decision. Typo'd types (e.g. "report_to") and truly unknown types
// are rejected at Load time by team.ValidateStructural, so they
// cannot reach this function. Returns nil if roleName has no outgoing
// reports_to edges or every target contributes zero bullets.
func deriveAntiResponsibilities(roleName string, collabs []team.Collaboration, roles *role.LayeredStore) []antiResponsibility {
	var out []antiResponsibility
	for _, c := range collabs {
		if c.From != roleName {
			continue
		}
		// Post-ethos-2z2 invariant: c.Type is one of
		// {reports_to, collaborates_with, delegates_to}. Anything
		// else would have been rejected at Load time by
		// team.ValidateStructural. This branch handles the two
		// known-but-deferred types by warning the user that the
		// edge is valid-but-unhandled, so a fixture with a valid
		// collaborates_with edge from roleName still produces a
		// visible signal instead of silent drop.
		if c.Type != "reports_to" {
			fmt.Fprintf(os.Stderr,
				"ethos: generate-agents: anti-responsibilities: unsupported edge from %q to %q with type %q (expected \"reports_to\") — skipping\n",
				c.From, c.To, c.Type)
			continue
		}
		target, err := roles.Load(c.To)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ethos: generate-agents: anti-responsibilities: target role %q: %v\n", c.To, err)
			continue
		}
		for _, resp := range target.Responsibilities {
			norm := normalizeResponsibility(resp)
			if norm == "" {
				fmt.Fprintf(os.Stderr,
					"ethos: generate-agents: anti-responsibilities: role %q: empty responsibility skipped\n",
					c.To)
				continue
			}
			out = append(out, antiResponsibility{
				Responsibility: norm,
				TargetRole:     c.To,
			})
		}
	}
	return out
}

// reportsToTarget is one resolved occupant of a role that roleName's
// outgoing reports_to edge points to.
type reportsToTarget struct {
	Name   string
	Handle string
}

// deriveReportsToTargets walks the team's collaboration edges starting
// from roleName and returns, in walk order, the occupying identity of
// each outgoing reports_to edge's target role. Same walk as
// deriveAntiResponsibilities (c.From == roleName, c.Type ==
// "reports_to", same warn-and-skip on any other edge type) — only the
// resolution step differs: this resolves the target role's occupying
// IDENTITY through members, not the target role's Responsibilities
// through roles.
//
// A target role with no occupying member — prevented on every Load by
// team.ValidateStructural, but not provable from this function's own
// inputs — or an occupant whose identity fails to load, is warned to
// stderr and skipped, mirroring deriveAntiResponsibilities's
// target.Load handling above. The caller must not assert a reporting
// line it cannot back up. Returns nil if roleName has no outgoing
// reports_to edges or every target fails to resolve.
//
// If two distinct outgoing edges resolve to the same occupying
// identity (one identity filling two roles roleName reports to), that
// identity appears twice in the returned slice — not deduplicated, to
// preserve visibility of the underlying graph shape. If visually wrong
// in practice, add de-dup-by-handle before joinWithOxford in a
// follow-up.
//
// A single target role may itself have multiple occupants —
// team.ValidateStructural only dedupes (identity, role) pairs, not
// roles, so two different identities filling the same role is a valid
// team shape. All occupants of a matched role are appended, in the
// order they appear in members.
func deriveReportsToTargets(roleName string, members []team.Member, collabs []team.Collaboration, identities identity.IdentityStore) []reportsToTarget {
	return deriveReportsToTargetsReader(roleName, members, collabs, AgentIdentities(identities))
}

func deriveReportsToTargetsReader(roleName string, members []team.Member, collabs []team.Collaboration, identities AgentIdentityReader) []reportsToTarget {
	var out []reportsToTarget
	for _, c := range collabs {
		if c.From != roleName {
			continue
		}
		if c.Type != "reports_to" {
			fmt.Fprintf(os.Stderr,
				"ethos: generate-agents: reports-to: unsupported edge from %q to %q with type %q (expected \"reports_to\") — skipping\n",
				c.From, c.To, c.Type)
			continue
		}
		// Collect every member occupying c.To — multiple identities
		// may share a role (team.ValidateStructural only dedupes
		// (identity, role) pairs).
		var handles []string
		for _, m := range members {
			if m.Role == c.To {
				handles = append(handles, m.Identity)
			}
		}
		if len(handles) == 0 {
			fmt.Fprintf(os.Stderr,
				"ethos: generate-agents: reports-to: edge from %q to %q: target role has no occupying team member — skipping\n",
				c.From, c.To)
			continue
		}
		for _, handle := range handles {
			occ, err := identities.LoadAgentReference(handle)
			if err != nil {
				fmt.Fprintf(os.Stderr,
					"ethos: generate-agents: reports-to: edge from %q to %q: loading identity %q: %v\n",
					c.From, c.To, handle, err)
				continue
			}
			out = append(out, reportsToTarget{Name: occ.Name, Handle: occ.Handle})
		}
	}
	return out
}

// renderReportsToLine renders the resolved reports-to targets as one
// sentence, in the same shape resolveParentLine (subagent_start.go)
// uses for a single target: "You report to Name (handle)." Multiple
// targets join with joinWithOxford. Zero targets returns "" — the
// caller omits the line rather than assert a claim it cannot back up
// (audit finding 1).
func renderReportsToLine(targets []reportsToTarget) string {
	if len(targets) == 0 {
		return ""
	}
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = fmt.Sprintf("%s (%s)", t.Name, t.Handle)
	}
	return fmt.Sprintf("You report to %s.", joinWithOxford(names))
}

// joinWithOxford joins names in English. Two items: "a and b". Three or
// more: Oxford-comma "a, b, and c". One item returns as-is. Zero items
// returns the empty string.
func joinWithOxford(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
	}
}

// hasWriteTool reports whether the role's tools list contains Write or
// Edit — the signal for a role that modifies files and should run
// quality gates after every write.
func hasWriteTool(tools []string) bool {
	for _, t := range tools {
		if t == "Write" || t == "Edit" {
			return true
		}
	}
	return false
}

// toolScopeNote tells a generated agent which tools are real. Claude Code
// injects the instructions of every connected MCP server into a session.
// Injection is keyed to the server connection, not to the agent's tools
// allowlist, so a scoped specialist reads usage prose for servers it
// cannot call. The harness is not ours to change; this note is the
// counterweight, and it outranks the ambient prose because it is in the
// agent's own system prompt.
//
// The note disowns instructions server by server, not wholesale. A
// specialist DOES hold quarry, biff, and ethos tools, and those servers'
// instructions carry rules it must follow — quarry's "emit output
// verbatim" among them. Only a server whose tools the agent lacks is
// speaking past it.
//
// The examples stay short and generic on purpose. Naming the full server
// list would date the note every time a plugin is added or removed, and
// the rule — trust the tools: field, not the ambient prose — does not
// depend on which servers happen to be connected.
const toolScopeNote = "Only the tools listed in the `tools:` field above are available to you.\n" +
	"A session also carries usage instructions for every connected MCP server —\n" +
	"github, vox, and others — whether or not you hold their tools. Instructions\n" +
	"for a server whose tools you do NOT hold are not addressed to you. Ignore\n" +
	"any direction to call a tool that is not on your list.\n"

// activeBundleDefaultSkills returns the active bundle's default_skills
// (DES-073), or nil when no bundle is active, none is configured, or
// resolution fails. Resolution errors are not fatal here: agent
// generation must not brick over a bundle-resolution problem that
// `ethos doctor` and other bundle-aware commands already surface.
func activeBundleDefaultSkills(configRoot string) []string {
	globalRoot, err := tierBGlobalRoot()
	if err != nil {
		return nil
	}
	b, err := bundle.ResolveActive(configRoot, globalRoot)
	if err != nil || b == nil {
		return nil
	}
	return b.Manifest.DefaultSkills
}

// mergeSkills computes the deduplicated skill slug list for a generated
// agent: baseline-ops always first, then the identity's own Skills, then
// the active bundle's default_skills — in that order, first occurrence
// wins (DES-073).
//
// A slug ethos ships through its plugin (pluginskill.Is) is emitted under
// BOTH plugin-namespace prefixes, the released "ethos:" and the dev-build
// "ethos-dev:" (skillNames). ethos ships those skills through the plugin
// (plugin/skills/), not the flat ~/.claude/skills/ namespace, so a generated
// agent must reference them by their plugin-namespaced name for Claude Code
// to resolve them (standards/plugins.md §"Namespace scope"). The installed
// plugin is "ethos" in a consumer repo but "ethos-dev" in this repo's own dev
// sessions (plugin/.claude-plugin/plugin.json), and release-plugin.sh does
// NOT rewrite skill names in generated agents, so a single prod-only prefix
// would not resolve in dev. Listing both is the same tolerant dual-prefix
// pattern the role YAML uses for MCP tools (mcp__plugin_ethos_self__*
// alongside mcp__plugin_ethos-dev_self__*): Claude Code resolves whichever
// plugin is installed and skips the other with a debug-log warning.
//
// Any OTHER slug — a user's own skill, a third-party bundle skill under its
// bare slug, or an already-namespaced slug such as lux:scene — is passed
// through verbatim. Prefixing one of those with ethos: would name a skill
// ethos does not ship, which Claude Code then silently skips (the regression
// Qodo caught on this PR).
func skillNames(slug string) []string {
	if pluginskill.Is(slug) {
		return []string{"ethos:" + slug, "ethos-dev:" + slug}
	}
	return []string{slug}
}

func mergeSkills(identitySkills, bundleDefaultSkills []string) []string {
	seen := map[string]bool{"baseline-ops": true}
	out := skillNames("baseline-ops")
	for _, lists := range [][]string{identitySkills, bundleDefaultSkills} {
		for _, s := range lists {
			s = strings.TrimSpace(s)
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, skillNames(s)...)
		}
	}
	return out
}

// buildAgentFile assembles a .claude/agents/<handle>.md from identity,
// personality, writing-style, and role data. antiResps is the flat list
// of responsibilities belonging to roles this agent reports to; when
// non-empty, it is rendered as a "## What You Don't Do" section between
// Responsibilities and Talents. reportsTo is the resolved occupants of
// those same roles; when non-empty, it renders the "You report to ..."
// opening-line sentence.
func buildAgentFile(id *identity.Identity, r *role.Role, antiResps []antiResponsibility, reportsTo []reportsToTarget, filePatterns string, skills []string) string {
	var b strings.Builder

	// Extract description: first non-heading content line from personality.
	desc := extractDescription(id.PersonalityContent)

	// Frontmatter.
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", id.Handle)
	fmt.Fprintf(&b, "description: %s\n", yamlQuote(desc))
	b.WriteString("tools:\n")
	for _, t := range normalizedAgentTools(r.Tools) {
		fmt.Fprintf(&b, "  - %s\n", t)
	}
	if r.Model != "" {
		fmt.Fprintf(&b, "model: %s\n", yamlQuote(r.Model))
	}
	b.WriteString("skills:\n")
	for _, s := range skills {
		fmt.Fprintf(&b, "  - %s\n", yamlQuote(s))
	}
	if hasWriteTool(normalizedAgentTools(r.Tools)) {
		b.WriteString("hooks:\n")
		b.WriteString("  PostToolUse:\n")
		b.WriteString("    - matcher: \"Write|Edit\"\n")
		b.WriteString("      hooks:\n")
		b.WriteString("        - type: command\n")
		// Path-filtered make check. Reviewer-style agents emit 20KB+
		// markdown over many Write calls into .tmp/ and .punt-labs/local/ethos/ to
		// stage results and traces; running `make check` after each
		// of those writes serializes lint+test across the agent's
		// stream and trips the 600s Claude Code watchdog (bug
		// ethos-m3gh). The hook reads the tool's target path from the
		// Claude Code hook stdin payload via jq, then routes:
		//
		//   1. jq missing OR empty $_path (jq ran but stdin had no
		//      .tool_input.file_path, or jq itself errored and the
		//      2>/dev/null swallowed it) — fail closed: run make
		//      check. The path filter only short-circuits the gate
		//      when we KNOW the write is scratch/markdown; absence of
		//      a path is "unknown", which must trigger the gate, not
		//      skip it (Copilot reviews on PR #326).
		//
		//   2. Paths under .tmp/ or .punt-labs/local/ethos/ — bypass (exit 0).
		//      These are scratch and local trace dirs. The .punt-labs/ethos/
		//      source/record tree is NOT exempt. Patterns cover leading and
		//      embedded segments so worktree layouts (`*/.tmp/*`) and
		//      cwd-relative writes (`.tmp/*`) both match.
		//
		//   3. Project-type file extensions (from filePatterns) — run
		//      resolve the repo root FROM THE EDITED FILE, then run
		//      make check there with output-capture; on failure emit
		//      the tail -n 60 truncation to stderr and exit 2, else
		//      exit 0. Patterns are detected at generation time
		//      by projectFilePatterns: go.mod → Go patterns, pyproject.toml
		//      or setup.py → Python patterns, neither → generic fallback.
		//
		//   4. Anything else (markdown, JSON, text) — exit 0. Doc
		//      writes do not trigger compile or lint failures.
		//
		// The case statement is pure glob — no `..` resolution, no
		// realpath. Sh's glob patterns are byte-literal and
		// case-sensitive, so `.TMP/` does not match `.tmp/` and a
		// path like `.tmp/foo/../internal/bar.go` is matched against
		// the FIRST .tmp/ pattern (path text starts with `.tmp/`),
		// triggering bypass. That is the correct behavior: the worker
		// is asking to write to `.tmp/foo/../internal/bar.go`, which
		// the OS will resolve to the same inode as `internal/bar.go`,
		// but the user-supplied path text is the scratch dir — if a
		// malicious worker can synthesize such a path, the bypass is
		// the least of the problems. Production code writes use
		// direct paths (`internal/bar.go`), which fall through to the
		// trigger clause.
		//
		// In the trigger clause, resolve cwd from the EDITED FILE, not
		// from $CLAUDE_PROJECT_DIR: dirname the path, then `git -C <dir>
		// rev-parse --show-toplevel`, and cd to that root. An
		// isolated-worktree sub-agent edits files in its own linked
		// worktree while $CLAUDE_PROJECT_DIR still names the MAIN
		// checkout — running make check there is blind to the agent's
		// changes and blocks on unrelated failures (ethos-n4tk).
		// Resolving from the file lands make check in the worktree that
		// actually holds the edit. The resolution only runs for an
		// ABSOLUTE $_path (case /*): a relative path would make `git -C
		// $(dirname)` resolve against the hook's own cwd, which is
		// ambiguous, so a relative path takes the prior-behavior default
		// of $CLAUDE_PROJECT_DIR. Claude Code's tool input is absolute in
		// practice; the guard makes the fallback provable. When git
		// errors or the file is outside a repo (empty $_root), also fall
		// back to $CLAUDE_PROJECT_DIR. The two branches with no file path
		// (jq missing; empty .tool_input.file_path) have nothing to
		// resolve from and keep using $CLAUDE_PROJECT_DIR directly.
		// Output is captured to a variable first. On failure the tail
		// -n 60 truncation is written to stderr and the hook exits 2;
		// tail (not head) keeps the LAST 60 lines, which hold the
		// failing tool's diagnostic — make stops at the first failing
		// target, so the error is at the end of the accumulated output.
		// on success it is silent and exits 0. Claude Code surfaces
		// only stderr for a blocking PostToolUse hook, and only exit 2
		// hands the block reason back to the model — routing to stdout
		// or exiting with make's raw code swallows the failure detail
		// (ethos-bo84). The command stays POSIX-sh compatible (it runs
		// under /bin/sh, which is dash on Debian/Ubuntu): no `set -o
		// pipefail`, no process substitution, no bash-isms in the case
		// patterns.
		fmt.Fprintf(&b, "          command: \"if ! command -v jq >/dev/null 2>&1; then _out=$(cd \\\"$CLAUDE_PROJECT_DIR\\\" && make check 2>&1); _rc=$?; if [ $_rc -ne 0 ]; then printf '%%s\\\\n' \\\"$_out\\\" | tail -n 60 >&2; exit 2; fi; exit 0; fi; _path=$(jq -r '.tool_input.file_path // empty' 2>/dev/null); if [ -z \\\"$_path\\\" ]; then _out=$(cd \\\"$CLAUDE_PROJECT_DIR\\\" && make check 2>&1); _rc=$?; if [ $_rc -ne 0 ]; then printf '%%s\\\\n' \\\"$_out\\\" | tail -n 60 >&2; exit 2; fi; exit 0; fi; case \\\"$_path\\\" in */.tmp/*|*/.punt-labs/local/ethos/*|.tmp/*|.punt-labs/local/ethos/*) exit 0 ;; %s) case \\\"$_path\\\" in /*) _dir=$(dirname \\\"$_path\\\"); _root=$(git -C \\\"$_dir\\\" rev-parse --show-toplevel 2>/dev/null); if [ -z \\\"$_root\\\" ]; then _root=\\\"$CLAUDE_PROJECT_DIR\\\"; fi ;; *) _root=\\\"$CLAUDE_PROJECT_DIR\\\" ;; esac; _out=$(cd \\\"$_root\\\" && make check 2>&1); _rc=$?; if [ $_rc -ne 0 ]; then printf '%%s\\\\n' \\\"$_out\\\" | tail -n 60 >&2; exit 2; fi; exit 0 ;; *) exit 0 ;; esac\"\n", filePatterns)
	}
	b.WriteString("---\n")

	// Body.
	personalityBody := stripLeadingHeading(id.PersonalityContent)

	// Opening line.
	firstLine := firstContentSentence(id.PersonalityContent)
	firstLine = strings.TrimSuffix(firstLine, ".")
	// The template already opens with "You are %s (%s), ...". A personality
	// whose first sentence itself starts with "You are" (e.g. "You are
	// inspired by...") would duplicate that phrase, so strip it before
	// interpolating.
	const youAre = "you are "
	if len(firstLine) >= len(youAre) && strings.EqualFold(firstLine[:len(youAre)], youAre) {
		firstLine = firstLine[len(youAre):]
	}
	if firstLine == "" {
		fmt.Fprintf(&b, "\nYou are %s (%s).\n", id.Name, id.Handle)
	} else {
		fmt.Fprintf(&b, "\nYou are %s (%s), %s.\n", id.Name, id.Handle, firstLine)
	}
	if line := renderReportsToLine(reportsTo); line != "" {
		b.WriteString(line)
		b.WriteString("\n")
	}

	// Tool scope. Sits directly under the opening lines, next to the
	// frontmatter it refers to, and above the personality so the agent
	// reads it before any section that might name a tool.
	b.WriteString("\n")
	b.WriteString(toolScopeNote)

	// Personality content (after first paragraph, since opening line uses it).
	remaining := skipFirstParagraph(personalityBody)
	remaining = strings.TrimRight(remaining, "\n")
	if remaining != "" {
		b.WriteString("\n")
		b.WriteString(remaining)
		b.WriteString("\n")
	}

	// Writing style.
	if id.WritingStyleContent != "" {
		wsBody := stripLeadingHeading(id.WritingStyleContent)
		wsBody = strings.TrimRight(wsBody, "\n")
		if wsBody != "" {
			b.WriteString("\n## Writing Style\n\n")
			b.WriteString(wsBody)
			b.WriteString("\n")
		}
	}

	// Responsibilities.
	if len(r.Responsibilities) > 0 {
		b.WriteString("\n## Responsibilities\n\n")
		for _, resp := range r.Responsibilities {
			fmt.Fprintf(&b, "- %s\n", resp)
		}
	}

	// Anti-responsibilities — what this agent does NOT do, derived
	// from the target roles of reports_to edges. Bullets are bucketed
	// by target in a single pass: targets records first-seen order,
	// byTarget groups the bullets. Preamble and bullet block then
	// render from the same ordered slice, so the two orderings cannot
	// drift.
	if len(antiResps) > 0 {
		b.WriteString("\n## What You Don't Do\n\n")
		targets := make([]string, 0, len(antiResps))
		byTarget := make(map[string][]antiResponsibility, len(antiResps))
		for _, ar := range antiResps {
			if _, ok := byTarget[ar.TargetRole]; !ok {
				targets = append(targets, ar.TargetRole)
			}
			byTarget[ar.TargetRole] = append(byTarget[ar.TargetRole], ar)
		}
		fmt.Fprintf(&b, "You report to %s. These are not yours:\n\n", joinWithOxford(targets))
		for _, tgt := range targets {
			for _, ar := range byTarget[tgt] {
				fmt.Fprintf(&b, "- %s (%s)\n", ar.Responsibility, ar.TargetRole)
			}
		}
	}

	// Safety constraints — tool-usage restrictions the agent must obey.
	if len(r.SafetyConstraints) > 0 {
		b.WriteString("\n## Safety Constraints\n\n")
		b.WriteString("These restrictions apply to your tool usage. Violations will be caught by review.\n\n")
		for _, sc := range r.SafetyConstraints {
			fmt.Fprintf(&b, "- **%s**: %s\n", sc.Tool, sc.Message)
		}
	}

	// Talents. A leading newline guarantees a blank line between the
	// previous section (Responsibilities, anti-responsibilities, safety
	// constraints, or Writing Style) and this line so the label never
	// hugs a bullet.
	if len(id.Talents) > 0 {
		fmt.Fprintf(&b, "\nTalents: %s\n", strings.Join(id.Talents, ", "))
	}

	// Output format — the structured-handoff template the worker emits
	// when it reports back to the leader. Role-owned body, generator-
	// owned heading. Last thing in the file so it is the worker's final
	// reference when producing the result artifact. The TrimSpace guard
	// drops whitespace-only bodies ("\n", "   ") so a role YAML that
	// sets the key with a degenerate value omits the section entirely
	// instead of emitting an empty heading. TrimRight with "\r\n" as
	// the cutset normalizes trailing newlines (LF and CRLF) to exactly
	// one terminal "\n", regardless of whether the YAML was authored on
	// Unix or Windows with core.autocrlf — a bare trailing CR would
	// otherwise leak through and leave a stray "^M" at the end of the
	// generated file. Trailing spaces or tabs after non-whitespace
	// content are not part of the cutset and are preserved verbatim.
	if strings.TrimSpace(r.OutputFormat) != "" {
		b.WriteString("\n## Output Format\n\n")
		b.WriteString(strings.TrimRight(r.OutputFormat, "\r\n"))
		b.WriteString("\n")
	}

	return agentmark.Add(b.String())
}

// Tool grants are a set. Normalize only this catalog, without mutating the role
// or reordering prose, skills, talents or team collaboration edges.
func normalizedAgentTools(tools []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, tool := range tools {
		tool = strings.TrimSpace(tool)
		if tool != "" && !seen[tool] {
			seen[tool] = true
			out = append(out, tool)
		}
	}
	sort.Strings(out)
	return out
}

// extractDescription returns the first prose sentence from markdown
// content, suitable for the frontmatter description field. Skips
// headings, bullets, and other non-prose lines.
func extractDescription(content string) string {
	return firstContentSentence(content)
}

// yamlQuote wraps s in double quotes, escaping internal backslashes
// and double quotes. This prevents YAML-significant characters like
// # and : from corrupting frontmatter values.
func yamlQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
