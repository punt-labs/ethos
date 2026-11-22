package checkset

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestGoAdapterRoundTrip(t *testing.T) {
	set, err := Decode([]byte("name: go-checks\nchecks:\n  - name: vet\n    stage: edit\n    files: ['**/*.go']\n    adapter: go\n    go: vet\n"))
	require.NoError(t, err)
	require.Empty(t, set.Checks[0].Run)
	for _, marshal := range []func(any) ([]byte, error){yaml.Marshal, json.Marshal} {
		data, err := marshal(set)
		require.NoError(t, err)
		again, err := Decode(data)
		require.NoError(t, err, "%s", data)
		require.Empty(t, again.Checks[0].Run)
	}
}
