//go:build mutant_teamleak

package fs

// This file is a DELIBERATE DEFECT, compiled only with -tags
// mutant_teamleak. It exists so the conformance suite can prove it catches
// the defect: P1-6's mutants_test.go asserts that this build fails exactly
// C-12 and C-26 (team isolation, 4.5.6) and no other case. The normal
// build contains none of this code — see store_list.go for the real twin.

// teamSession is one stored session together with the team it belongs to.
type teamSession struct {
	teamRef string
	file    *sessionFile
}

// collectSessions leaks: it walks EVERY team's sessions instead of the
// profile's own. Everything else the real twin does — hiding the sessions
// of members that are not active — is kept, so the only observable
// difference is the team scope of 4.5.6.
func collectSessions(s *store, _ string) ([]teamSession, error) {
	teams, err := listNames(s.teamsDir())
	if err != nil {
		return nil, err
	}
	var out []teamSession
	for _, team := range teams {
		if !safeRef(team) {
			continue
		}
		found, err := s.sessionsOfTeam(team)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	return out, nil
}
