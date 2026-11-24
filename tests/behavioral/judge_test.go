//go:build behavioral

package behavioral

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type judgeTransport func(*http.Request) (*http.Response, error)

func (f judgeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestJudgeUsesConfiguredModel(t *testing.T) {
	t.Setenv("ETHOS_BEHAVIORAL_JUDGE_MODEL", "fixture-judge")
	original := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = original })
	http.DefaultClient = &http.Client{Transport: judgeTransport(func(req *http.Request) (*http.Response, error) {
		var request apiRequest
		require.NoError(t, json.NewDecoder(req.Body).Decode(&request))
		require.Equal(t, "fixture-judge", request.Model)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"content":[{"text":"{\"violated\":false,\"confidence\":1}"}]}`)),
		}, nil
	})}
	_, err := JudgePersonaCompliance(t.Context(), JudgeOpts{})
	require.NoError(t, err)
}
