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
	wantAsk := []string{
		"Bash(ethos -*)",
		"Bash(ethos setup:*)",
		"Bash(ethos seed:*)",
		"Bash(ethos uninstall:*)",
		"Bash(ethos enable:*)",
		"Bash(ethos disable:*)",
		"Bash(ethos serve:*)",
		"Bash(ethos hook:*)",
		"Bash(ethos import:*)",
		"Bash(ethos export:*)",
		"Bash(ethos adr:*)",
		"Bash(ethos audit:*)",
		"Bash(ethos ext:*)",
		"Bash(ethos identity:*)",
		"Bash(ethos mission:*)",
		"Bash(ethos personality:*)",
		"Bash(ethos role:*)",
		"Bash(ethos session:*)",
		"Bash(ethos talent:*)",
		"Bash(ethos team:*)",
		"Bash(ethos vendor:*)",
		"Bash(ethos writing-style:*)",
		"mcp__plugin_ethos_self__adr",
		"mcp__plugin_ethos_self__ext",
		"mcp__plugin_ethos_self__identity",
		"mcp__plugin_ethos_self__mission",
		"mcp__plugin_ethos_self__personality",
		"mcp__plugin_ethos_self__role",
		"mcp__plugin_ethos_self__session",
		"mcp__plugin_ethos_self__talent",
		"mcp__plugin_ethos_self__team",
		"mcp__plugin_ethos_self__vendor",
		"mcp__plugin_ethos_self__writing_style",
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".md")
		if name == e.Name() || strings.HasSuffix(name, "-dev") {
			continue
		}
		wantAsk = append(wantAsk, "Skill(ethos:"+name+")")
	}
	skills, err := os.ReadDir(filepath.Join("..", "..", "plugin", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	for _, skill := range skills {
		if skill.IsDir() {
			wantAsk = append(wantAsk, "Skill(ethos:"+skill.Name()+")")
		}
	}
	wantAllow := []string{"Bash(ethos:*)", "mcp__plugin_ethos_self__*", "Skill(ethos:*)"}
	if !slices.Equal(PermissionRules["allow"], wantAllow) {
		t.Errorf("PermissionRules[allow] = %v, want %v", PermissionRules["allow"], wantAllow)
	}
	gotAsk := slices.Clone(PermissionRules["ask"])
	slices.Sort(gotAsk)
	slices.Sort(wantAsk)
	if !slices.Equal(gotAsk, wantAsk) {
		t.Errorf("PermissionRules[ask] = %v, want %v", gotAsk, wantAsk)
	}
	for _, rules := range PermissionRules {
		for _, rule := range rules {
			if strings.Contains(rule, "-dev") {
				t.Errorf("dev rule granted: %s", rule)
			}
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
	if got, want := permissionList(t, settings, "allow"), append([]string{"Read"}, PermissionRules["allow"]...); !slices.Equal(got, want) {
		t.Errorf("after enable allow = %v, want %v", got, want)
	}
	if got := permissionList(t, settings, "ask"); !slices.Equal(got, PermissionRules["ask"]) {
		t.Errorf("after enable ask = %v, want %v", got, PermissionRules["ask"])
	}
	rep, err := Disable(dir, false)
	if err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if got := permissionList(t, settings, "allow"); !slices.Equal(got, []string{"Read"}) {
		t.Errorf("after disable allow = %v, want [Read]", got)
	}
	if got := permissionList(t, settings, "ask"); len(got) != 0 {
		t.Errorf("after disable ask = %v, want []", got)
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
	for _, path := range []string{
		filepath.Join(dir, ".punt-labs", "ethos", "enabled"),
		filepath.Join(dir, ".punt-labs", "ethos", "CLAUDE.md"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("failed enable wrote %s", path)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Error("failed enable created CLAUDE.md")
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

func permissionList(t *testing.T, path, tier string) []string {
	t.Helper()
	var doc struct {
		Permissions map[string][]string `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(readFile(t, path)), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Permissions[tier]
}
