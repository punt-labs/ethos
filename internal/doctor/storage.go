package doctor

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/punt-labs/ethos/v4/internal/autorepair"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// CheckStorage is the "Storage" row (DES-084): PASS when the global stamp, and
// the repository stamp inside a repository, are current and record no
// residual; WARN when a stamp is missing or old, or a step recorded a
// residual. Ensure runs before doctor, so a missing stamp means a repair that
// could not finish, not one that has not started.
func CheckStorage(loc resolve.Located, global string) Result {
	const name = "Storage"
	if global == "" {
		return Result{Name: name, Status: "WARN", Detail: "no layout stamp: the home directory could not be determined"}
	}
	g, err := autorepair.ReadStamp(autorepair.GlobalStampPath(global))
	if bad := stampProblem("global", g, err); bad != "" {
		return Result{Name: name, Status: "WARN", Detail: bad}
	}
	items, residual, upgraded := g.Items, len(g.Residual), g.RepairedAt
	if loc.Store != "" && loc.StoreFailure() == nil {
		p, perr := loc.Layout()
		if perr == nil {
			rs, rerr := autorepair.ReadStamp(autorepair.RepositoryStampPath(p))
			if bad := stampProblem("repository", rs, rerr); bad != "" {
				return Result{Name: name, Status: "WARN", Detail: bad}
			}
			items += rs.Items
			residual += len(rs.Residual)
			if rs.RepairedAt > upgraded {
				upgraded = rs.RepairedAt
			}
		}
	}
	if residual > 0 {
		return Result{Name: name, Status: "WARN", Detail: fmt.Sprintf(
			"layout v%d with %d residual condition(s); see ~/.punt-labs/ethos/logs/repair.log", autorepair.LayoutVersion, residual)}
	}
	return Result{Name: name, Status: "PASS", Detail: fmt.Sprintf(
		"layout v%d (upgraded %s, %d items)", autorepair.LayoutVersion, upgraded, items)}
}

// stampProblem says why the stamp read as (s, err) is not current, or "".
func stampProblem(which string, s autorepair.Stamp, err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Sprintf("no layout stamp for the %s store; the next ethos command repairs it", which)
	case err != nil:
		return fmt.Sprintf("the %s layout stamp is unreadable: %v", which, err)
	case !s.Current():
		return fmt.Sprintf("the %s store is at layout v%d, not v%d; the next ethos command repairs it", which, s.Version, autorepair.LayoutVersion)
	}
	return ""
}
