package hook

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

// VacuumCrossCheckWithPolicy probes the shared store, not a roster's historical
// checkout. Removing a linked checkout no longer implies a lost live file.
func VacuumCrossCheckWithPolicy(p layout.Policy, globalRoot string, active []ActiveSession, w io.Writer) error {
	checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
	repoID, err := audit.RepoIdentity(checkout)
	if err != nil {
		return err
	}
	tombstones, err := audit.ListTombstones(filepath.Join(globalRoot, "sessions"), w)
	if err != nil {
		return err
	}
	flagged := map[string]bool{}
	for _, t := range tombstones {
		if t.Repo == repoID && t.Flagged() {
			flagged[t.Session] = true
		}
	}
	for _, a := range active {
		if _, ok := flagged[a.Session]; !ok {
			flagged[a.Session] = false
		}
	}
	for id, purged := range flagged {
		s := audit.Stream{Namespace: audit.SessionNS, Session: id}
		if err := probeSharedLive(p, s, w); err != nil {
			return err
		}
		bound, err := mission.SessionBoundMissions(globalRoot, filepath.Dir(filepath.Dir(p.RecordRoot())), id)
		if err != nil {
			return err
		}
		for _, missionID := range bound {
			if err := probeSharedLive(p, audit.Stream{Namespace: audit.MissionNS, Session: id, Mission: missionID}, w); err != nil {
				return err
			}
		}
		if purged {
			fmt.Fprintf(w, "session %s: retained purge loss recorded; the storage upgrade retires it on the next ethos run\n", id)
		}
	}
	return nil
}

func probeSharedLive(p layout.Policy, s audit.Stream, w io.Writer) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if _, err := os.Stat(s.LivePath(p)); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := audit.ReadStream(p, s)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "warning: stream %s has no shared live file; %d archived/published line(s) retained, but a deleted unarchived tail cannot be ruled out\n", s.Key(), len(data.Lines))
	return nil
}
