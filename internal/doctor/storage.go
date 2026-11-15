package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/autorepair"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// storageGOOS is runtime.GOOS, a variable so a test can drive the Windows
// arm from any host.
var storageGOOS = runtime.GOOS

// CheckStorage is the "Storage" row (DES-084): PASS when the global stamp, and
// the repository stamp inside a repository, are current and every residual
// is settled (a final state, nothing to do); WARN when a stamp is missing or
// old, or a residual is actionable, naming the action. Every residual is
// listed in Result.Lines for --verbose. Ensure runs before doctor, so a missing stamp means a repair that
// could not finish, not one that has not started. On Windows Ensure compiles
// and does nothing (DES-084 Wiring), so the row says so instead of promising
// a repair.
func CheckStorage(loc resolve.Located, global string) Result {
	const name = "Storage"
	if storageGOOS == "windows" {
		return Result{Name: name, Status: "WARN", Detail: "automatic storage repair is not supported on Windows (DES-084)"}
	}
	if global == "" {
		return Result{Name: name, Status: "WARN", Detail: "no layout stamp: the home directory could not be determined"}
	}
	g, err := autorepair.ReadStamp(autorepair.GlobalStampPath(global))
	if bad := stampProblem("global", g, err); bad != "" {
		return Result{Name: name, Status: "WARN", Detail: bad}
	}
	items, upgraded := g.Items, g.RepairedAt
	residuals := append([]autorepair.Residual(nil), g.Residual...)
	if loc.Store != "" {
		p, perr := loc.Layout()
		if perr != nil {
			return Result{Name: name, Status: "WARN", Detail: "repository layout stamp not checked: " + perr.Error()}
		}
		rs, rerr := autorepair.ReadStamp(autorepair.RepositoryStampPath(p))
		if bad := stampProblem("repository", rs, rerr); bad != "" {
			return Result{Name: name, Status: "WARN", Detail: bad}
		}
		items += rs.Items
		residuals = append(residuals, rs.Residual...)
		if rs.RepairedAt > upgraded {
			upgraded = rs.RepairedAt
		}
	}
	var act, settled []autorepair.Residual
	for _, r := range residuals {
		if r.EffectiveClass() == autorepair.ClassSettled {
			settled = append(settled, r)
		} else {
			act = append(act, r)
		}
	}
	lines := make([]string, 0, len(residuals))
	for _, r := range act {
		lines = append(lines, "action needed: "+residualLine(r))
	}
	for _, r := range settled {
		lines = append(lines, "settled: "+residualLine(r))
	}
	if len(act) > 0 {
		const show = 3
		parts := make([]string, 0, show+1)
		for i, r := range act {
			if i == show {
				parts = append(parts, fmt.Sprintf("and %d more (ethos doctor --verbose)", len(act)-show))
				break
			}
			parts = append(parts, residualLine(r))
		}
		return Result{Name: name, Status: "WARN", Lines: lines, Detail: fmt.Sprintf(
			"layout v%d; %d condition(s) need action: %s", autorepair.LayoutVersion, len(act), strings.Join(parts, "; "))}
	}
	detail := fmt.Sprintf("layout v%d (upgraded %s, %d items)", autorepair.LayoutVersion, upgraded, items)
	if len(settled) > 0 {
		detail = fmt.Sprintf("layout v%d; %s, no action needed", autorepair.LayoutVersion, settledSummary(settled))
	}
	return Result{Name: name, Status: "PASS", Detail: detail, Lines: lines}
}

// residualLine is one residual as "step id: reason; action". A residual from
// a stamp written before actions existed shows the reason alone.
func residualLine(r autorepair.Residual) string {
	s := fmt.Sprintf("%s %s: %s", r.Step, r.ID, r.Reason)
	if r.Action != "" {
		s += "; " + r.Action
	}
	return s
}

// settledSummary counts settled residuals by what they are.
func settledSummary(rs []autorepair.Residual) string {
	var fenced, imported, other int
	for _, r := range rs {
		switch r.Step {
		case "round-decisions":
			fenced++
		case "global-missions":
			imported++
		default:
			other++
		}
	}
	var parts []string
	if fenced > 0 {
		parts = append(parts, fmt.Sprintf("%d legacy missions fenced", fenced))
	}
	if imported > 0 {
		parts = append(parts, fmt.Sprintf("%d missions imported with the global copy kept", imported))
	}
	if other > 0 {
		parts = append(parts, fmt.Sprintf("%d other settled conditions", other))
	}
	return strings.Join(parts, "; ")
}

// stampProblem says why the stamp read as (s, err) is not current, or "".
func stampProblem(which string, s autorepair.Stamp, err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Sprintf("no layout stamp for the %s store; see ~/.punt-labs/ethos/logs/repair.log", which)
	case err != nil:
		return fmt.Sprintf("the %s layout stamp is unreadable: %v", which, err)
	case !s.Current():
		return fmt.Sprintf("the %s store is at layout v%d, not v%d; see ~/.punt-labs/ethos/logs/repair.log", which, s.Version, autorepair.LayoutVersion)
	}
	return ""
}
