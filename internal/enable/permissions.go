package enable

// PermissionRules are the allow rules enable adds to the repo's
// .claude/settings.json and disable removes by exact value: the ethos CLI, the
// plugin's MCP server (key "self"), and one Skill rule per shipped command. No
// ethos-dev rule: the dev plugin only exists in a maintainer's checkout. A rule
// retired here strands on disable, so it stays until no enabled repo carries it.
var PermissionRules = []string{
	"Bash(ethos:*)",
	"mcp__plugin_ethos_self__*",
	"Skill(ethos:ext)",
	"Skill(ethos:identity)",
	"Skill(ethos:personality)",
	"Skill(ethos:role)",
	"Skill(ethos:session)",
	"Skill(ethos:talent)",
	"Skill(ethos:team)",
	"Skill(ethos:writing-style)",
}
