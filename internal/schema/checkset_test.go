package schema

import (
	"encoding/json"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/checkset"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func TestCheckSetSchemaRuntimeParity(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	resource, err := json.Marshal(CheckSet.JSONSchema())
	require.NoError(t, err)
	var document any
	require.NoError(t, json.Unmarshal(resource, &document))
	require.NoError(t, compiler.AddResource("urn:checks", document))
	schema, err := compiler.Compile("urn:checks")
	require.NoError(t, err)
	for _, tc := range []struct {
		name, stage, timeout string
		files                any
		valid                bool
	}{
		{"edit list", "edit", "10s", []string{"**"}, true},
		{"edit all", "edit", "10s", "all", false},
		{"missing stage", "", "10s", "all", false},
		{"commit all", "commit", "2m", "all", true},
		{"handoff all", "handoff", "10m", "all", true},
		{"bad timeout", "edit", "tomorrow", []string{"**"}, false},
		{"negative", "edit", "-1s", []string{"**"}, false},
		{"zero", "edit", "0s", []string{"**"}, false},
		{"fractional zero", "edit", "0.000s", []string{"**"}, false},
		{"composite zero", "edit", "0h0m0s", []string{"**"}, false},
		{"fraction", "edit", ".5s", []string{"**"}, true},
		{"trailing decimal", "edit", "1.s", []string{"**"}, true},
		{"composite", "edit", "+0m1.5s", []string{"**"}, true},
		{"microseconds", "edit", "2µs", []string{"**"}, true},
		{"greek microseconds", "edit", "2μs", []string{"**"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := map[string]any{"name": "probe", "files": tc.files, "run": []string{"true"}, "timeout": tc.timeout}
			if tc.stage != "" {
				c["stage"] = tc.stage
			}
			data, err := json.Marshal(map[string]any{"name": "probe", "checks": []any{c}})
			require.NoError(t, err)
			var value any
			require.NoError(t, json.Unmarshal(data, &value))
			_, runtimeErr := checkset.Decode(data)
			require.Equal(t, tc.valid, runtimeErr == nil, "runtime: %v", runtimeErr)
			schemaErr := schema.Validate(value)
			require.Equal(t, tc.valid, schemaErr == nil, "schema: %v", schemaErr)
		})
	}
}
