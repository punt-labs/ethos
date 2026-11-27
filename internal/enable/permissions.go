package enable

// PermissionRules are the broad production grants owned by ethos enable.
var PermissionRules = map[string][]string{
	"allow": {
		"Bash(ethos:*)",
		"mcp__plugin_ethos_self__*",
		"Skill(ethos:*)",
	},
}

// legacyPermissionRules are exact ask rules installed by older versions.
// Enable and disable remove these values without disturbing other rules.
var legacyPermissionRules = map[string][]string{
	"ask": {
		"Bash(ethos -*)",
		"Bash(ethos setup:*)",
		"Bash(ethos seed:*)", // msgguard: allow permission rule value, never printed
		"Bash(ethos uninstall:*)",
		"Bash(ethos enable:*)", // msgguard: allow permission rule value, never printed
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
		"Skill(ethos:ext)",
		"Skill(ethos:identity)",
		"Skill(ethos:personality)",
		"Skill(ethos:role)",
		"Skill(ethos:session)",
		"Skill(ethos:talent)",
		"Skill(ethos:team)",
		"Skill(ethos:writing-style)",
		"Skill(ethos:baseline-ops)",
		"Skill(ethos:create-from-project)", // msgguard: allow permission rule value, never printed
		"Skill(ethos:gstack-debug)",
		"Skill(ethos:gstack-design)",
		"Skill(ethos:gstack-plan)",
		"Skill(ethos:gstack-qa)",
		"Skill(ethos:gstack-review)",
		"Skill(ethos:gstack-ship)",
		"Skill(ethos:mission)",
		"Skill(ethos:optimize-team)",
	},
}

// disableRules is what disable removes in one settings write: the current set
// plus the retired rules, so a failed write never leaves disable half done.
var disableRules = map[string][]string{
	"allow": PermissionRules["allow"],
	"ask":   legacyPermissionRules["ask"],
}

// EnvDefaults are the environment variables owned by ethos enable in
// .claude/settings.json. GIT_OPTIONAL_LOCKS=0 prevents background git status
// and git diff calls in agent sessions from taking .git/index.lock, which
// otherwise causes spurious deferrals in live-verified handoff gates (DES-086).
var EnvDefaults = map[string]string{
	"GIT_OPTIONAL_LOCKS": "0",
}
