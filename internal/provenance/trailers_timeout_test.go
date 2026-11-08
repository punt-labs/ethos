//go:build linux || darwin

package provenance

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

func TestProvenanceSourceTrailerCommandsAreBounded(t *testing.T) {
	for _, command := range []string{"config", "interpret-trailers", "log"} {
		t.Run(command, func(t *testing.T) {
			p, _ := repositoryTest(t)
			r := p.Repository
			keys, err := r.sourceTrailerKeys(t.Context())
			require.NoError(t, err)
			testhelpers.ShimGit(t, command)
			r.Env = gitenv.Parsed()
			if command != "log" {
				_, err = r.sourceTrailerKeys(t.Context())
			} else {
				_, err = r.sourceCommitTrailers(t.Context(), "HEAD", keys)
			}
			require.ErrorIs(t, err, gitenv.ErrTimeout)
		})
	}
}
