package behavioral

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func checkFirstMissionRead(output, missionID string) error {
	decoder := json.NewDecoder(strings.NewReader(output))
	var callID string
	for {
		var event struct {
			Type    string
			Message struct {
				Content []struct {
					Type      string
					ID        string
					Name      string
					ToolUseID string `json:"tool_use_id"`
					IsError   bool   `json:"is_error"`
					Input     struct {
						Method    string
						MissionID string `json:"mission_id"`
					}
				}
			}
		}
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("no completed mission show call in the agent transcript")
			}
			return fmt.Errorf("invalid agent transcript: %w", err)
		}
		for _, block := range event.Message.Content {
			switch {
			case event.Type == "assistant" && block.Type == "tool_use":
				if callID != "" || block.Name != "mcp__ethos__mission" ||
					block.Input.Method != "show" || block.Input.MissionID != missionID || block.ID == "" {
					return errors.New("worker began another tool action before successfully reading the assigned mission")
				}
				callID = block.ID
			case event.Type == "user" && block.Type == "tool_result" && callID != "" && block.ToolUseID == callID:
				if block.IsError {
					return errors.New("worker's mission show call returned an error")
				}
				return nil
			}
		}
	}
}

func TestFirstMissionRead(t *testing.T) {
	const show = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"read","name":"mcp__ethos__mission","input":{"method":"show","mission_id":"fixture"}}]}}`
	const result = `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"read","is_error":false}]}}`
	const work = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"edit","name":"Write","input":{}}]}}`

	require.NoError(t, checkFirstMissionRead(show+"\n"+result+"\n"+work, "fixture"))
	for _, transcript := range []string{
		"",
		show,
		work + "\n" + show + "\n" + result,
		show + "\n" + work + "\n" + result,
		show + "\n" + strings.Replace(result, "false", "true", 1),
		strings.Replace(show, "fixture", "other", 1) + "\n" + result,
	} {
		require.Error(t, checkFirstMissionRead(transcript, "fixture"), "%s", transcript)
	}
}
