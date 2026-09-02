//go:build !mutant_teamleak

package fs

// teamSession is one stored session together with the team it belongs to,
// so the caller can render it and look its member label up without walking
// the tree again.
type teamSession struct {
	teamRef string
	file    *sessionFile
}

// collectSessions returns the sessions `session list` may report: those of
// the PROFILE'S TEAM ONLY (4.5.6 — a session outside the team must not be
// listed, C-12, C-26), and only those whose principal is still an active
// member (a revoked member's sessions vanish, C-08).
//
// Its twin in store_list_mutant.go walks every team instead. That mutant
// must fail exactly C-12 and C-26 and nothing else, which is why the
// membership filter below is repeated there verbatim: the mutation is the
// team scope and nothing but the team scope.
func collectSessions(s *store, teamRef string) ([]teamSession, error) {
	return s.sessionsOfTeam(teamRef)
}
