package hooks

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSubagentStopTimeoutCoversHandoffBudget(t *testing.T) {
	data, err := os.ReadFile("hooks.json")
	require.NoError(t, err)
	var config struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Timeout int `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(data, &config))
	groups := config.Hooks["SubagentStop"]
	require.Len(t, groups, 1)
	require.Len(t, groups[0].Hooks, 1)
	timeout := time.Duration(groups[0].Hooks[0].Timeout) * time.Second
	require.GreaterOrEqual(t, timeout, 30*time.Minute, "SubagentStop timeout must cover the handoff stage cap")
}
