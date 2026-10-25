package hook

import (
	"errors"
	"fmt"

	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// storeUnlocatedNote explains why a session has no repository persona or team
// when the store could not be located: an unreadable .git, a refused
// ETHOS_REPO_ROOT. It is empty when the store was located, and when the working
// directory is simply outside a repository.
func storeUnlocatedNote(err error) string {
	if err == nil || errors.Is(err, resolve.ErrNotInRepo) {
		return ""
	}
	return fmt.Sprintf("Ethos: the repository store could not be located (%v), so this repo's agent persona and team were not loaded.", err)
}
