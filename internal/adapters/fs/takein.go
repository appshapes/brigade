package fs

import (
	"errors"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// ErrTeamRefUnstorable is TakeIn's refusal of a join secret whose team_ref
// this store cannot keep: a store keeps a team in a directory named after
// its reference, so the reference is letters, digits, `-` and `_`, 64 at
// most (safeRef).
var ErrTeamRefUnstorable = errors.New("the team reference cannot be a directory name of the store")

// TakeIn writes, into the store at root, the record of a team that lives
// on another backend: the team_ref inside joinSecret, teamName, and the
// digest of the whole secret, exactly as `team create` writes one, with no
// creator and no member. A `team join` with that secret then finds the
// team. It reports whether it wrote the record: a team of that reference
// that is already in the store is left as it is, whatever its secret.
//
// This adapter does NOT advertise team.take_in and has no command that
// takes a team in (4.7): a team's creator is the only authority it knows,
// and a team that was taken in has none. TakeIn is the door of the
// stand-in REST server (internal/adapters/rest/stub), which answers the
// contract's take-in over this store and keeps the team's owner itself.
// It takes the store's lock, as every command does.
func TakeIn(root, teamName, joinSecret string, now time.Time) (bool, error) {
	secret, err := protocol.ParseJoinSecret(joinSecret)
	if err != nil {
		return false, err
	}
	if !safeRef(secret.TeamRef()) {
		return false, ErrTeamRefUnstorable
	}
	st, err := openStore(root, func() time.Time { return now })
	if err != nil {
		return false, err
	}
	defer st.close()
	if _, held, err := st.loadTeam(secret.TeamRef()); err != nil || held {
		return false, err
	}
	team := &teamFile{
		TeamRef: secret.TeamRef(), TeamName: teamName, JoinSecretSHA26: sha256hex(secret.Secret()),
		CreatedAt: now.UTC(),
	}
	if err := writeJSON(st.teamPath(team.TeamRef), team); err != nil {
		return false, err
	}
	return true, nil
}
