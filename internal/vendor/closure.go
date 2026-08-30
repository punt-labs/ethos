package vendor

import (
	"fmt"
	"sort"

	"github.com/punt-labs/ethos/v4/internal/identity"
)

// closure walks the reference graph outward from the seed handles to a
// fixed point, collecting everything a resolvable set needs.
//
// Edges (DES-057 Part B):
//
//	identity → personality, writing style, talents
//	identity → its .ext/ base files
//	identity → teams that CONTAIN it        (the reverse edge)
//	team     → member identities, member roles
//
// Roles are leaves and there is no team→team edge, so the walk
// terminates: the node universe is finite and the visited set only
// grows.
//
// The reverse edge is the load-bearing one. Without it, vendoring `bwk`
// produces a set whose team file references `claudia`, an identity
// reachable only through membership — the team then fails to validate
// against the vendored layer alone, which is exactly the incomplete
// "complete" snapshot this command exists to prevent.
//
// Blast radius follows from that edge: the closure pulls the connected
// component of the team graph, so in a dense org vendoring one identity
// can vendor most of the roster. That is why the command plans by
// default and writes only under --apply.
func (v *Vendorer) closure(seeds []string) (*Plan, error) {
	p := &Plan{
		Seeds: append([]string(nil), seeds...),
		Ext:   map[string][]ExtFile{},
	}

	seenIdentity := map[string]bool{}
	seenTeam := map[string]bool{}
	seenRole := map[string]bool{}

	// queue holds identities still to expand. Teams are expanded inline
	// because expanding one only ever enqueues identities and roles.
	queue := append([]string(nil), seeds...)

	// teamsByMember is built once: the reverse edge needs to ask "which
	// teams contain this handle?" for every identity in the closure, and
	// re-listing every team per identity would be quadratic.
	teamsByMember, err := v.teamsByMember()
	if err != nil {
		return nil, err
	}

	for len(queue) > 0 {
		handle := queue[0]
		queue = queue[1:]
		if seenIdentity[handle] {
			continue
		}

		id, err := v.src.Identities.Load(handle, identity.Reference(true))
		if err != nil {
			return nil, fmt.Errorf("loading identity %q: %w", handle, err)
		}
		seenIdentity[handle] = true

		if id.Personality != "" {
			p.Personalities = appendUnique(p.Personalities, id.Personality)
		}
		if id.WritingStyle != "" {
			p.WritingStyles = appendUnique(p.WritingStyles, id.WritingStyle)
		}
		for _, slug := range id.Talents {
			p.Talents = appendUnique(p.Talents, slug)
		}

		ext, err := v.extFiles(handle)
		if err != nil {
			return nil, err
		}
		p.Ext[handle] = ext

		// The reverse edge: every team this identity belongs to joins the
		// closure, and each such team then contributes its own members.
		for _, name := range teamsByMember[handle] {
			if seenTeam[name] {
				continue
			}
			seenTeam[name] = true
			t, err := v.src.Teams.Load(name)
			if err != nil {
				return nil, fmt.Errorf("loading team %q: %w", name, err)
			}
			p.Teams = appendUnique(p.Teams, name)
			for _, m := range t.Members {
				if !seenIdentity[m.Identity] {
					queue = append(queue, m.Identity)
				}
				if !seenRole[m.Role] {
					// A team file can name a role that does not exist —
					// team.Load's structural validation does not check
					// existence. Catch it while planning; discovering it
					// during the copy would leave a half-written set.
					if !v.src.Roles.Exists(m.Role) {
						return nil, fmt.Errorf("team %q names role %q, which does not exist in any source layer", name, m.Role)
					}
					seenRole[m.Role] = true
					p.Roles = appendUnique(p.Roles, m.Role)
				}
			}
		}
	}

	p.Identities = sortedKeys(seenIdentity)
	sort.Strings(p.Personalities)
	sort.Strings(p.WritingStyles)
	sort.Strings(p.Talents)
	sort.Strings(p.Roles)
	sort.Strings(p.Teams)
	return p, nil
}

// teamsByMember indexes every readable team by the handles it contains,
// giving the reverse identity→teams edge in one pass.
//
// A team that fails to load is a hard error, not a skip: a vendored set
// built while quietly ignoring an unreadable team would be missing
// members and would still claim to be complete.
func (v *Vendorer) teamsByMember() (map[string][]string, error) {
	names, err := v.src.Teams.List()
	if err != nil {
		return nil, fmt.Errorf("listing teams: %w", err)
	}
	sort.Strings(names)
	out := map[string][]string{}
	for _, name := range names {
		t, err := v.src.Teams.Load(name)
		if err != nil {
			return nil, fmt.Errorf("loading team %q: %w", name, err)
		}
		for _, m := range t.Members {
			out[m.Identity] = append(out[m.Identity], name)
		}
	}
	return out, nil
}

func appendUnique(list []string, v string) []string {
	for _, existing := range list {
		if existing == v {
			return list
		}
	}
	return append(list, v)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
