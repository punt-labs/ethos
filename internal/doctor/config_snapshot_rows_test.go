package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/role"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/punt-labs/ethos/v4/internal/team"
)

// These rows either consume configuration directly or read the store layers it
// selected. Filesystem-only checks deliberately remain live, not snapshotted.
func configSnapshotRows() []string {
	return []string{
		"Identity directory", HumanIdentityRow, "Default agent", "Duplicate fields",
		"Orphaned agent files", "Agent file currency", "Team members",
		"Repo-only completeness", "Extension key names",
	}
}

func snapshotRow(t *testing.T, results []Result, name string) Result {
	t.Helper()
	var found []Result
	for _, r := range results {
		if r.Name == name {
			found = append(found, r)
		}
	}
	require.Len(t, found, 1, "row %q must execute exactly once", name)
	return found[0]
}

func snapshotWrite(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

func configSnapshotFixture(t *testing.T, layers string) (resolve.Located, *identity.LayeredStore, *team.LayeredStore) {
	t.Helper()
	store, checkout, old, _ := currencyFixture(t)
	global := filepath.Join(physicalAgentRoot(t, t.TempDir()), ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(global, 0o700))
	t.Setenv("HOME", filepath.Dir(filepath.Dir(global)))
	t.Setenv("USER", "caller")
	// The caller is intentionally global-only, including under repo-only.
	snapshotWrite(t, filepath.Join(global, "identities", "caller.yaml"), "name: Caller\nhandle: caller\nkind: human\nemail: t@example.com\n")
	root, active := old.RepoRoot(), ""
	config := "agent: leader\nteam: engineering\nresolution: repo-only\n"
	repoOnly := layers != "layered"
	if layers == "bundle" {
		active = filepath.Join(store, ".punt-labs", "ethos-bundles", "snapshot")
		snapshotWrite(t, filepath.Join(active, "bundle.yaml"), "name: snapshot\nversion: 1\n")
		for _, dir := range []string{"identities", "personalities", "roles", "teams"} {
			require.NoError(t, os.Rename(filepath.Join(root, dir), filepath.Join(active, dir)))
		}
		config += "active_bundle: snapshot\n"
	}
	if !repoOnly {
		// A global-only team exercises the supplied layered team/identity stores.
		for _, dir := range []string{"identities", "personalities", "roles", "teams"} {
			if dir == "identities" {
				require.NoError(t, os.Rename(filepath.Join(root, dir, "bwk.yaml"), filepath.Join(global, dir, "bwk.yaml")))
			} else {
				require.NoError(t, os.Rename(filepath.Join(root, dir), filepath.Join(global, dir)))
			}
		}
		config = "agent: leader\nteam: engineering\nresolution: layered\nrecord: full\n"
	}
	snapshotWrite(t, filepath.Join(store, ".punt-labs", "ethos.yaml"), config)
	var b *identity.Store
	if active != "" {
		b = identity.NewStore(active)
	}
	s := identity.NewLayeredStoreWithBundle(identity.NewStore(root), b, identity.NewStore(global), repoOnly)
	teams := team.NewLayeredStoreWithBundle(root, active, global, repoOnly)
	roles := role.NewLayeredStoreWithBundle(root, active, global, repoOnly)
	require.NoError(t, hook.GenerateAgentFilesTo(store, checkout, s, teams, roles))
	cfg, err := resolve.ConfigOf(store)
	require.NoError(t, err)
	return resolve.Located{Store: store, Checkout: checkout, Ethos: root, Config: cfg}, s, teams
}

func snapshotDoctor(loc resolve.Located, s identity.IdentityStore, ss *session.Store, teams *team.LayeredStore, caller bool) []Result {
	if caller {
		return RunAllForCaller(loc, s, ss, teams, Result{Name: HumanIdentityRow, Status: "PASS", Detail: "declared caller, not environment"})
	}
	return RunAllFor(loc, s, ss, teams)
}

func TestDoctorConfigSnapshotRows(t *testing.T) {
	for _, layers := range []string{"repo-only", "layered", "bundle"} {
		for _, caller := range []bool{false, true} {
			entry := "RunAllFor"
			if caller {
				entry = "RunAllForCaller"
			}
			t.Run(layers+"/"+entry, func(t *testing.T) {
				loc, s, teams := configSnapshotFixture(t, layers)
				ss := session.NewStore(physicalAgentRoot(t, t.TempDir()))
				control := snapshotDoctor(loc, s, ss, teams, caller)
				human := snapshotRow(t, control, HumanIdentityRow)
				require.Equal(t, "PASS", human.Status, human.Detail)
				if caller {
					assert.Equal(t, "declared caller, not environment", human.Detail)
				} else {
					assert.Contains(t, human.Detail, "Caller (caller)")
				}
				for _, tc := range []struct{ name, body string }{
					{"malformed", "team: [interrupted\n"},
					{"invalid record", "record: invalid\n"},
					{"invalid resolution", "resolution: invalid\n"},
					{"different valid", "agent: different\nteam: different\nresolution: layered\nrecord: blame\n"},
					{"different bundle", "active_bundle: missing\nresolution: repo-only\n"},
					{"removed", ""},
				} {
					t.Run(tc.name, func(t *testing.T) {
						path := filepath.Join(loc.Store, ".punt-labs", "ethos.yaml")
						snapshotWrite(t, path, tc.body)
						if tc.name == "removed" {
							require.NoError(t, os.Remove(path))
						}
						before := diagnosticTreeHash(t, loc.Store, loc.Checkout, s.GlobalRoot())
						got := snapshotDoctor(loc, s, ss, teams, caller)
						for _, name := range configSnapshotRows() {
							assert.Equal(t, snapshotRow(t, control, name), snapshotRow(t, got, name), name)
						}
						for _, r := range got {
							assert.NotEqual(t, "Repository configuration", r.Name, "must not discover the replacement bundle/policy")
						}
						assert.Equal(t, before, diagnosticTreeHash(t, loc.Store, loc.Checkout, s.GlobalRoot()), "doctor must stay read-only")
						// The controls must distinguish a new request from the old one.
						detail, ok := CheckDefaultAgent(loc.Checkout, loc.Store)
						assert.False(t, ok && detail == loc.Config.Agent, "a fresh convenience call must see the replacement")
					})
				}
			})
		}
	}
}

func TestDoctorConfigSnapshotRefusalRows(t *testing.T) {
	for _, caller := range []bool{false, true} {
		for _, failure := range []string{"malformed", "invalid record", "store", "ethos", "bundle", "resolution"} {
			t.Run(failure+map[bool]string{false: "/RunAllFor", true: "/RunAllForCaller"}[caller], func(t *testing.T) {
				loc, s, teams := configSnapshotFixture(t, "repo-only")
				path := filepath.Join(loc.Store, ".punt-labs", "ethos.yaml")
				switch failure {
				case "malformed", "invalid record":
					body := "team: [interrupted\n"
					if failure == "invalid record" {
						body = "record: invalid\n"
					}
					snapshotWrite(t, path, body)
					cfg, err := resolve.ConfigOf(loc.Store)
					require.Error(t, err)
					loc.Config, loc.ConfigErr = cfg, &resolve.ConfigError{Err: err, What: "the repository configuration could not be read"}
				case "store":
					loc.StoreErr = errors.New("captured store refusal")
				case "ethos":
					loc.EthosErr = errors.New("captured ethos refusal")
				case "bundle":
					loc.Config.ActiveBundle = "missing"
				case "resolution":
					loc.Config.Resolution = "invalid"
				}
				ss := session.NewStore(physicalAgentRoot(t, t.TempDir()))
				control := snapshotDoctor(loc, s, ss, teams, caller)
				require.True(t, AnyFailed(control), "captured refusals fail closed")
				snapshotWrite(t, path, "agent: replacement\nteam: replacement\nresolution: layered\n")
				got := snapshotDoctor(loc, s, ss, teams, caller)
				for _, name := range configSnapshotRows() {
					r := snapshotRow(t, got, name)
					assert.Equal(t, snapshotRow(t, control, name), r, name)
					assert.NotEqual(t, "PASS", r.Status, "unavailable layers cannot pass: %s", name)
				}
				require.True(t, AnyFailed(got))
			})
		}
	}
}

func TestDoctorConfigSnapshotKeepsLiveHealthFailures(t *testing.T) {
	loc, s, teams := configSnapshotFixture(t, "repo-only")
	ss := session.NewStore(physicalAgentRoot(t, t.TempDir()))
	control := RunAllFor(loc, s, ss, teams)
	require.Equal(t, "PASS", snapshotRow(t, control, "Agent file currency").Status)
	snapshotWrite(t, filepath.Join(loc.Store, ".punt-labs", "ethos.yaml"), "record: invalid\n")
	snapshotWrite(t, filepath.Join(loc.Checkout, ".claude", "agents", "bwk.md"), "physical drift\n")
	drift := snapshotRow(t, RunAllFor(loc, s, ss, teams), "Agent file currency")
	assert.Equal(t, "FAIL", drift.Status, drift.Detail)
	assert.Contains(t, drift.Detail, "bwk.md", "the captured policy must still check actual generated bytes")
	// Global content cannot repair a repo-only completeness failure.
	snapshotWrite(t, filepath.Join(s.GlobalRoot(), "personalities", "clear.md"), "# Clear\n\nClear prose.\n")
	require.NoError(t, os.Remove(filepath.Join(s.RepoRoot(), "personalities", "clear.md")))
	got := RunAllFor(loc, s, ss, teams)
	assert.Equal(t, "FAIL", snapshotRow(t, got, "Agent file currency").Status)
	complete := snapshotRow(t, got, "Repo-only completeness")
	assert.Equal(t, "FAIL", complete.Status, complete.Detail)
	assert.Contains(t, complete.Detail, "personalities/clear")
}

func TestDoctorConfigSnapshotAbsentAndLegacyRows(t *testing.T) {
	for _, source := range []string{"absent", "legacy"} {
		for _, caller := range []bool{false, true} {
			t.Run(source+map[bool]string{false: "/RunAllFor", true: "/RunAllForCaller"}[caller], func(t *testing.T) {
				loc, s, teams := configSnapshotFixture(t, "repo-only")
				path := filepath.Join(loc.Store, ".punt-labs", "ethos.yaml")
				if source == "legacy" {
					data, err := os.ReadFile(path)
					require.NoError(t, err)
					snapshotWrite(t, filepath.Join(loc.Ethos, "config.yaml"), string(data))
				}
				require.NoError(t, os.Remove(path))
				cfg, err := resolve.ConfigOf(loc.Store)
				require.NoError(t, err)
				loc.Config = cfg
				if source == "absent" {
					require.Nil(t, cfg)
					// Absence selects layered stores for this original request.
					s = identity.NewLayeredStore(identity.NewStore(s.RepoRoot()), identity.NewStore(s.GlobalRoot()))
					teams = team.NewLayeredStore(s.RepoRoot(), s.GlobalRoot())
				}
				ss := session.NewStore(physicalAgentRoot(t, t.TempDir()))
				control := snapshotDoctor(loc, s, ss, teams, caller)
				snapshotWrite(t, path, "agent: newly-configured\nteam: missing\nrecord: full\n")
				got := snapshotDoctor(loc, s, ss, teams, caller)
				for _, name := range configSnapshotRows() {
					assert.Equal(t, snapshotRow(t, control, name), snapshotRow(t, got, name), name)
				}
			})
		}
	}
}

func TestDoctorConfigSnapshotUnavailableStoreDoesNotReadCWD(t *testing.T) {
	loc, original, _ := configSnapshotFixture(t, "repo-only")
	loc.StoreErr = errors.New("captured store unavailable")
	// The integration supplies only available layers, not a new repository
	// store discovered from the process's working directory.
	s := identity.NewLayeredStoreWithBundle(nil, nil, identity.NewStore(original.GlobalRoot()), false)
	teams := team.NewLayeredStore("", original.GlobalRoot())
	ss := session.NewStore(physicalAgentRoot(t, t.TempDir()))
	decoy := physicalAgentRoot(t, t.TempDir())
	t.Chdir(decoy)
	control := RunAllFor(loc, s, ss, teams)
	snapshotWrite(t, filepath.Join(decoy, ".punt-labs", "ethos.yaml"), "agent: cwd-decoy\nteam: engineering\nresolution: repo-only\n")
	got := RunAllFor(loc, s, ss, teams)
	for _, name := range configSnapshotRows() {
		r := snapshotRow(t, got, name)
		assert.Equal(t, snapshotRow(t, control, name), r, name)
		assert.NotEqual(t, "PASS", r.Status, name)
		assert.NotContains(t, r.Detail, "cwd-decoy")
	}
	assert.Equal(t, "FAIL", snapshotRow(t, got, "Repository store").Status)
}

func TestDoctorConfigSnapshotConcurrentRequests(t *testing.T) {
	loc, s, teams := configSnapshotFixture(t, "repo-only")
	other := loc
	cfg := *loc.Config
	cfg.Agent, cfg.Resolution, cfg.Record = "second-leader", resolve.ResolutionLayered, "full"
	other.Config = &cfg
	otherS := identity.NewLayeredStore(identity.NewStore(s.RepoRoot()), identity.NewStore(s.GlobalRoot()))
	otherTeams := team.NewLayeredStore(s.RepoRoot(), s.GlobalRoot())
	ss := session.NewStore(physicalAgentRoot(t, t.TempDir()))
	firstControl := snapshotDoctor(loc, s, ss, teams, true)
	secondControl := snapshotDoctor(other, otherS, ss, otherTeams, true)
	require.NotEqual(t, snapshotRow(t, firstControl, "Default agent"), snapshotRow(t, secondControl, "Default agent"))
	snapshotWrite(t, filepath.Join(loc.Store, ".punt-labs", "ethos.yaml"), "record: invalid\n")
	first, second := make(chan []Result, 1), make(chan []Result, 1)
	go func() { first <- snapshotDoctor(loc, s, ss, teams, true) }()
	go func() { second <- snapshotDoctor(other, otherS, ss, otherTeams, true) }()
	a, b := <-first, <-second
	for _, name := range configSnapshotRows() {
		assert.Equal(t, snapshotRow(t, firstControl, name), snapshotRow(t, a, name), name)
		assert.Equal(t, snapshotRow(t, secondControl, name), snapshotRow(t, b, name), name)
	}
	assert.Equal(t, "leader", loc.Config.Agent, "neither request mutates shared configuration")
	assert.Equal(t, "second-leader", other.Config.Agent)
}
