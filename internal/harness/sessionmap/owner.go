package sessionmap

import (
	"os"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// The strict reader's privacy facts are adapterkit's (card 133): the
// symlink-refusing open and the mode and owner checks each have a unix
// and a windows implementation there, so this package reads no mode bit
// and no stat structure itself.

// openNoFollow opens path read-only without following a symlink at the
// last component: a symlinked map is refused rather than resolved,
// because the map's privacy is judged on the file itself.
func openNoFollow(path string) (*os.File, error) {
	return adapterkit.OpenNoFollow(path, os.O_RDONLY, 0)
}

// isSymlinkRefusal reports whether an openNoFollow error means "that was
// a symlink".
func isSymlinkRefusal(err error) bool {
	return adapterkit.IsSymlinkRefusal(err)
}
