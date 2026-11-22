package schema

import (
	"encoding/json"
	"fmt"
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

func TestAdapterSchemaRuntimeParity(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	data, err := json.Marshal(CheckSet.JSONSchema())
	require.NoError(t, err)
	var document any
	require.NoError(t, json.Unmarshal(data, &document))
	require.NoError(t, compiler.AddResource("urn:adapters", document))
	schema, err := compiler.Compile("urn:adapters")
	require.NoError(t, err)
	validate := func(t *testing.T, fields map[string]any, valid bool) {
		t.Helper()
		c := map[string]any{"name": "probe", "stage": "edit", "files": []string{"**"}}
		for k, v := range fields {
			c[k] = v
		}
		data, err := json.Marshal(map[string]any{"name": "adapters", "checks": []any{c}})
		require.NoError(t, err)
		var value any
		require.NoError(t, json.Unmarshal(data, &value))
		_, runtimeErr := checkset.Decode(data)
		require.Equal(t, valid, runtimeErr == nil, "runtime: %v, %s", runtimeErr, data)
		require.Equal(t, valid, schema.Validate(value) == nil, "schema: %v, %s", schema.Validate(value), data)
	}
	fields := map[string]map[string]any{
		"exec": {"run": []string{"lint", "{file}"}},
		"make": {"target": "check-file", "vars": map[string]string{"FLAGS": "literal value"}},
		"go":   {"go": "vet"}, "uv": {"uv": []string{"ruff", "{file}"}}, "npm": {"script": "lint:types"},
	}
	for adapter, params := range fields {
		t.Run(adapter, func(t *testing.T) {
			c := map[string]any{"adapter": adapter}
			for k, v := range params {
				c[k] = v
			}
			validate(t, c, true)
			if adapter == "exec" {
				c["adapter"] = ""
				validate(t, c, true)
				delete(c, "adapter")
				validate(t, c, true)
				c["adapter"] = "exec"
			}
			for _, stage := range []string{"commit", "handoff"} {
				all := map[string]any{"adapter": adapter, "stage": stage, "files": "all"}
				for k, v := range params {
					all[k] = v
				}
				if adapter == "exec" {
					all["run"] = []string{"lint", "{files}"}
				}
				if adapter == "uv" {
					all["uv"] = []string{"ruff", "{files}"}
				}
				validate(t, all, true)
			}
			for _, key := range []string{"run", "target", "vars", "go", "uv", "script"} {
				if _, ok := params[key]; ok {
					continue
				}
				bad := map[string]any{}
				for k, v := range c {
					bad[k] = v
				}
				bad[key] = map[string]any{"run": []string{"true"}, "target": "check", "vars": map[string]string{"OK": "x"}, "go": "vet", "uv": []string{"ruff"}, "script": "lint"}[key]
				validate(t, bad, false)
			}
		})
	}
	for _, c := range []map[string]any{
		{"adapter": "npm", "script": "{file}"},
		{"adapter": "npm", "script": "{root}", "stage": "handoff", "files": "all"},
	} {
		t.Run(fmt.Sprint(c), func(t *testing.T) { validate(t, c, true) })
	}
	for _, c := range []map[string]any{
		{}, {"adapter": "exec"}, {"adapter": "go"}, {"adapter": "go", "go": "run"},
		{"adapter": "make"}, {"adapter": "uv"}, {"adapter": "npm"}, {"adapter": "other", "run": []string{"true"}},
		{"adapter": "make", "target": "--eval=x"}, {"adapter": "make", "target": "a b"}, {"adapter": "make", "target": 123},
		{"adapter": "make", "target": "check", "vars": map[string]string{"BAD-NAME": "x"}},
		{"adapter": "make", "target": "check", "vars": map[string]string{"OK": "$HOME"}},
		{"adapter": "make", "target": "check", "vars": map[string]string{"OK": "{file}"}},
		{"adapter": "make", "target": "check", "vars": map[string]any{"OK": 42}},
		{"adapter": "make", "target": "check", "vars": nil},
		{"adapter": "uv", "uv": []string{}}, {"adapter": "uv", "uv": []string{""}},
		{"adapter": "uv", "uv": []any{"ruff", 42}}, {"adapter": "uv", "uv": []string{"ruff", "x{file}"}},
		{"adapter": "uv", "uv": []string{"ruff", "{file}"}, "stage": "commit"},
		{"adapter": "npm", "script": ""}, {"adapter": "npm", "script": 42}, {"adapter": "npm", "script": "x{file}"}, {"adapter": "npm", "script": "{"},
		{"run": []string{}}, {"run": []string{" "}}, {"run": []string{"true", "x{file}"}}, {"run": []string{"true", "{bad}"}},
		{"run": []string{"true", "{file}"}, "stage": "handoff"},
		{"run": []string{"true"}, "adapter": nil},
	} {
		t.Run(fmt.Sprint(c), func(t *testing.T) { validate(t, c, false) })
	}
}
