package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/enable"
	"github.com/spf13/cobra"
)

// guideCommands returns every `ethos ...` command line the guide shows, as a
// code-fence line or an inline code span, with a trailing "# comment" removed.
func guideCommands(guide string) []string {
	var out []string
	span := regexp.MustCompile("`([^`\n]+)`")
	fenced := false
	for _, line := range strings.Split(guide, "\n") {
		if strings.HasPrefix(line, "```") {
			fenced = !fenced
			continue
		}
		var cands []string
		if fenced {
			cands = []string{line}
		} else {
			for _, m := range span.FindAllStringSubmatch(line, -1) {
				cands = append(cands, m[1])
			}
		}
		for _, c := range cands {
			if i := strings.Index(c, " #"); i >= 0 {
				c = c[:i]
			}
			c = strings.TrimSpace(c)
			if c == "ethos" || strings.HasPrefix(c, "ethos ") {
				out = append(out, c)
			}
		}
	}
	return out
}

// hasFlag reports whether cmd defines the flag, long ("--x") or short ("-x"),
// directly or by inheritance.
func hasFlag(cmd *cobra.Command, tok string) bool {
	name := strings.TrimLeft(tok, "-")
	if i := strings.Index(name, "="); i >= 0 {
		name = name[:i]
	}
	if name == "help" || name == "h" {
		return true
	}
	if strings.HasPrefix(tok, "--") {
		return cmd.Flags().Lookup(name) != nil || cmd.InheritedFlags().Lookup(name) != nil
	}
	return cmd.Flags().ShorthandLookup(name) != nil || cmd.InheritedFlags().ShorthandLookup(name) != nil
}

// TestSetupGuideNamesOnlyRealCommandsAndFlags fails when the setup guide tells
// an agent to run an ethos command or flag the CLI does not define. The guide
// is the text `ethos enable` deposits, so a wrong flag there reaches every repo.
func TestSetupGuideNamesOnlyRealCommandsAndFlags(t *testing.T) {
	cmds := guideCommands(string(enable.Setup))
	if len(cmds) == 0 {
		t.Fatal("found no ethos commands in the setup guide; the extractor is broken")
	}
	for _, line := range cmds {
		toks := strings.Fields(line)[1:]
		cmd, rest, err := rootCmd.Find(toks)
		if err != nil {
			t.Errorf("guide runs %q: %v", line, err)
			continue
		}
		if cmd == rootCmd && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			t.Errorf("guide runs %q: ethos has no command %q", line, rest[0])
			continue
		}
		for _, tok := range rest {
			if strings.HasPrefix(tok, "-") && !hasFlag(cmd, tok) {
				t.Errorf("guide runs %q: `ethos %s` has no flag %s", line, strings.TrimSpace(strings.TrimPrefix(cmd.CommandPath(), "ethos")), tok)
			}
		}
	}
}
