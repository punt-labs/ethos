package enable

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPermissionRulesCoverCLIMCPAndEveryShippedCommand(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "..", "plugin", "commands"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Bash(ethos:*)", "mcp__plugin_ethos_self__*"}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".md")
		if name == e.Name() || strings.HasSuffix(name, "-dev") {
			continue
		}
		want = append(want, "Skill(ethos:"+name+")")
	}
	got := slices.Clone(PermissionRules)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("PermissionRules = %v, want %v", got, want)
	}
	for _, r := range PermissionRules {
		if strings.Contains(r, "-dev") {
			t.Errorf("dev rule granted: %s", r)
		}
	}
}

func TestEnableDisableRoundTripsPermissionRules(t *testing.T) {
	dir := gitRepo(t)
	settings := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"permissions":{"allow":["Read"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Enable(dir); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if got, want := allowList(t, settings), append([]string{"Read"}, PermissionRules...); !slices.Equal(got, want) {
		t.Errorf("after enable allow = %v, want %v", got, want)
	}
	rep, err := Disable(dir, false)
	if err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if got := allowList(t, settings); !slices.Equal(got, []string{"Read"}) {
		t.Errorf("after disable allow = %v, want [Read]", got)
	}
	if !slices.ContainsFunc(rep.Steps, func(s StepResult) bool { return s.Step == "permissions" && s.Status == "done" }) {
		t.Errorf("disable report has no permissions step: %+v", rep.Steps)
	}
}

func TestEnableRefusesMalformedSettingsUntouched(t *testing.T) {
	dir := gitRepo(t)
	settings := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Enable(dir); err == nil {
		t.Fatal("Enable succeeded on a malformed settings.json")
	}
	if got := readFile(t, settings); got != "{nope" {
		t.Errorf("settings.json changed: %q", got)
	}
}

func TestRefusedDisableLeavesTheRepoEnabled(t *testing.T) {
	dir := gitRepo(t)
	if _, err := Enable(dir); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	claudeMD := readFile(t, filepath.Join(dir, "CLAUDE.md"))
	settings := filepath.Join(dir, ".claude", "settings.json")
	if err := os.WriteFile(settings, []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Disable(dir, false); err == nil {
		t.Fatal("Disable succeeded on a malformed settings.json")
	}
	if got := readFile(t, filepath.Join(dir, "CLAUDE.md")); got != claudeMD {
		t.Errorf("CLAUDE.md changed by a refused disable: %q", got)
	}
}

func allowList(t *testing.T, path string) []string {
	t.Helper()
	var doc struct {
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(readFile(t, path)), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Permissions.Allow
}
