package autorepair

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

const stepGlobalMissions = "global-missions"

// reasonSharedOwnership is the residual reason for a mission whose global
// copy stays because this repository cannot prove it is the only owner.
const reasonSharedOwnership = "shared ownership; global copy kept"

func init() {
	Register(Step{Name: stepGlobalMissions, Scope: ScopeBoth, Run: runGlobalMissions})
}

// runGlobalMissions imports the global missions (v3.0-v3.11) this repository
// owns into its store through the redactor. A global copy is deleted only
// after the repository copy has been verified and only when the ownership is
// exclusive; a mission another repository may own too is imported as a copy
// and its global copy kept, recorded as a residual. Missions the repository
// does not own stay global. A mission that cannot be parsed, redacted or
// verified is a residual and stays where it is; the step finishes. The step
// prints nothing: every action and warning goes to env.Log.
func runGlobalMissions(ctx context.Context, env Env) (Outcome, error) {
	var out Outcome
	if !env.InRepository() {
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		out.Deferred = true
		return out, nil
	}
	exclusive := exclusiveMissions(env)
	res, err := mission.ImportGlobalMissions(env.Global, env.Roots.Store, env.Roots.Checkout, exclusive)
	if err != nil {
		return out, fmt.Errorf("importing global missions: %w", err)
	}
	global := filepath.Join(env.Global, "missions")
	repo := filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "missions")
	for _, w := range res.Warnings {
		env.Log.Logf(stepGlobalMissions, "scan-warning", nil, fmt.Errorf("%s", w))
	}
	for _, id := range res.Imported {
		env.Log.Logf(stepGlobalMissions, "import", []string{filepath.Join(global, id+".yaml"), filepath.Join(repo, id)}, nil)
	}
	for _, id := range res.Deleted {
		env.Log.Logf(stepGlobalMissions, "delete-global", []string{filepath.Join(global, id+".yaml")}, nil)
	}
	for _, id := range res.Shared {
		env.Log.Logf(stepGlobalMissions, "keep-global", []string{filepath.Join(global, id+".yaml")}, nil)
		out.Residuals = append(out.Residuals, Residual{Step: stepGlobalMissions, ID: id, Reason: reasonSharedOwnership})
	}
	out.Items = len(res.Imported)
	for _, f := range res.Failed {
		env.Log.Logf(stepGlobalMissions, "import-failed", []string{filepath.Join(global, f.ID+".yaml")}, fmt.Errorf("%s", f.Reason))
		out.Residuals = append(out.Residuals, Residual{Step: stepGlobalMissions, ID: f.ID, Reason: f.Reason})
	}
	return out, nil
}

// exclusiveMissions is the set of mission IDs this repository can prove it
// owns alone. The proof is the global roster of every session whose live
// audit here names the mission: each such roster must carry a checkout and
// every checkout must be this repository. A roster with no checkout (every
// roster before v4.13.0), a missing roster, or a roster naming another
// checkout leaves the mission unproven, and an unproven mission is never
// deleted from the global tree.
func exclusiveMissions(env Env) map[string]bool {
	sessions := missionSessions(env.Roots.Store)
	if env.Roots.Checkout != "" && env.Roots.Checkout != env.Roots.Store {
		for id, ss := range missionSessions(env.Roots.Checkout) {
			sessions[id] = append(sessions[id], ss...)
		}
	}
	out := map[string]bool{}
	for id, ss := range sessions {
		proven := len(ss) > 0
		for _, sid := range ss {
			if !rosterNames(env, sid) {
				proven = false
				break
			}
		}
		if proven {
			out[id] = true
		}
	}
	return out
}

// rosterNames reports whether the global roster of session sid names this
// repository as its checkout.
func rosterNames(env Env, sid string) bool {
	b, err := os.ReadFile(filepath.Join(env.Global, "sessions", sid+".yaml"))
	if err != nil {
		return false
	}
	var r struct {
		Checkout string `yaml:"checkout"`
	}
	if yaml.Unmarshal(b, &r) != nil || r.Checkout == "" {
		return false
	}
	for _, root := range []string{env.Roots.Checkout, env.Roots.Store} {
		if root != "" && mission.SamePath(filepath.Clean(r.Checkout), filepath.Clean(root)) {
			return true
		}
	}
	return false
}

// missionSessions maps each mission ID named in root's live session audit
// to the sessions that named it. The live zone is where a v3 repository
// keeps the MISSION_ID stamp of a dispatch; a malformed line is skipped here
// and reported by the import's own scan.
func missionSessions(root string) map[string][]string {
	out := map[string][]string{}
	entries, err := os.ReadDir(audit.LiveSessionsDir(root))
	if err != nil {
		return out
	}
	for _, e := range entries {
		sid, ok := strings.CutSuffix(e.Name(), ".audit.jsonl")
		if !ok || e.IsDir() {
			continue
		}
		b, err := os.ReadFile(audit.LiveAuditPath(root, sid))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(line, `"contract_id"`) {
				continue
			}
			var rec struct {
				ContractID string `json:"contract_id"`
			}
			if json.Unmarshal([]byte(line), &rec) != nil || rec.ContractID == "" {
				continue
			}
			out[rec.ContractID] = append(out[rec.ContractID], sid)
		}
	}
	return out
}
