package hook

import (
	"strings"
	"testing"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/role"
	"github.com/punt-labs/ethos/internal/team"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildPersonaBlock_Full(t *testing.T) {
	id := &identity.Identity{
		Name:   "Claude Agento",
		Handle: "claude",
		Kind:   "agent",
		PersonalityContent: "# Friendly Direct\n\nCOO / VP Engineering for Punt Labs.\n\n## Core Traits\n\n- Direct but warm\n- Data over adjectives",
		WritingStyleContent: "# Direct With Quips\n\nWriting style for Claude.\n\n## Rules\n\n- Keep sentences under 30 words\n- Lead with the answer",
		Talents:             []string{"product-strategy", "formal-methods"},
	}

	got := BuildPersonaBlock(id)

	assert.Contains(t, got, "You are Claude Agento (claude)")
	assert.Contains(t, got, "COO / VP Engineering for Punt Labs.")
	assert.Contains(t, got, "## Personality")
	assert.Contains(t, got, "Direct but warm")
	assert.Contains(t, got, "## Writing Style")
	assert.Contains(t, got, "Keep sentences under 30 words")
	assert.Contains(t, got, "## Talents")
	assert.Contains(t, got, "product-strategy, formal-methods")
}

func TestBuildPersonaBlock_NoPersonality(t *testing.T) {
	id := &identity.Identity{
		Name:                "Alice",
		Handle:              "alice",
		Kind:                "human",
		WritingStyleContent: "# Concise\n\nShort and clear.\n\n- No wasted words",
		Talents:             []string{"engineering"},
	}

	got := BuildPersonaBlock(id)

	assert.Contains(t, got, "You are Alice (alice).")
	assert.NotContains(t, got, "## Personality")
	assert.Contains(t, got, "## Writing Style")
	assert.Contains(t, got, "No wasted words")
	assert.Contains(t, got, "## Talents")
	assert.Contains(t, got, "engineering")
}

func TestBuildPersonaBlock_NoWritingStyle(t *testing.T) {
	id := &identity.Identity{
		Name:               "Bob",
		Handle:             "bob",
		Kind:               "agent",
		PersonalityContent: "# Methodical\n\nQuiet and patient.\n\n- Think before acting",
		Talents:            []string{"debugging"},
	}

	got := BuildPersonaBlock(id)

	assert.Contains(t, got, "You are Bob (bob)")
	assert.Contains(t, got, "## Personality")
	assert.Contains(t, got, "Think before acting")
	assert.NotContains(t, got, "## Writing Style")
	assert.Contains(t, got, "## Talents")
}

func TestBuildPersonaBlock_NoTalents(t *testing.T) {
	id := &identity.Identity{
		Name:               "Carol",
		Handle:             "carol",
		Kind:               "human",
		PersonalityContent: "# Calm\n\nCalm engineer.\n\n- Stay focused",
	}

	got := BuildPersonaBlock(id)

	assert.Contains(t, got, "You are Carol (carol)")
	assert.Contains(t, got, "## Personality")
	assert.NotContains(t, got, "## Talents")
}

func TestBuildPersonaBlock_EmptyContent(t *testing.T) {
	id := &identity.Identity{
		Name:   "Empty",
		Handle: "empty",
		Kind:   "agent",
	}

	got := BuildPersonaBlock(id)

	// No personality or writing style -- should return empty.
	assert.Equal(t, "", got)
}

func TestBuildPersonaBlock_NilIdentity(t *testing.T) {
	got := BuildPersonaBlock(nil)
	assert.Equal(t, "", got)
}

func TestBuildPersonaBlock_FirstLineExtraction(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string // substring expected in the "You are" line
	}{
		{
			name:    "heading then blank then description",
			content: "# Friendly\n\nA warm and direct communicator.\n\n- Rule one",
			want:    "A warm and direct communicator.",
		},
		{
			name:    "heading then description no blank",
			content: "# Friendly\nA warm communicator.\n- Rule one",
			want:    "A warm communicator.",
		},
		{
			name:    "no heading just text",
			content: "A warm communicator.\n\n- Rule one",
			want:    "A warm communicator.",
		},
		{
			name:    "only heading",
			content: "# Friendly",
			want:    ".", // just the period after handle
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := &identity.Identity{
				Name:               "Test",
				Handle:             "test",
				Kind:               "agent",
				PersonalityContent: tt.content,
			}
			got := BuildPersonaBlock(id)
			// The first line should contain the expected substring.
			first := strings.SplitN(got, "\n", 2)[0]
			assert.Contains(t, first, tt.want)
		})
	}
}

func TestFirstContentSentence(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "single line opening",
			content: "# Friendly\n\nCOO / VP Engineering for Punt Labs.\n\n## Core\n\n- Rule one",
			want:    "COO / VP Engineering for Punt Labs.",
		},
		{
			name:    "multi-line wrapping before period",
			content: "# McIlroy\n\nCLI specialist sub-agent. Principles from the Unix philosophy and\nMcIlroy's work on software componentization.\n\n## Core\n\n- Rule one",
			want:    "CLI specialist sub-agent. Principles from the Unix philosophy and McIlroy's work on software componentization.",
		},
		{
			name:    "stops at blank line",
			content: "# Heading\n\nFirst paragraph line one\nline two\n\nSecond paragraph.",
			want:    "First paragraph line one line two",
		},
		{
			name:    "heading only",
			content: "# Heading",
			want:    "",
		},
		{
			name:    "empty content",
			content: "",
			want:    "",
		},
		{
			name:    "period mid-sentence continues to end",
			content: "# Title\n\nDr. Smith wrote the spec and\nimplemented it.\n\nMore text.",
			want:    "Dr. Smith wrote the spec and implemented it.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := firstContentSentence(tt.content)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildTeamContext_Full(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	rs := role.NewLayeredStore("", dir)

	// Create identities.
	require.NoError(t, s.Save(&identity.Identity{
		Name:   "Jim Freeman",
		Handle: "jfreeman",
		Kind:   "human",
	}))
	require.NoError(t, s.Save(&identity.Identity{
		Name:   "Claude Agento",
		Handle: "claude",
		Kind:   "agent",
	}))

	// Create roles.
	require.NoError(t, rs.Save(&role.Role{
		Name:             "ceo",
		Responsibilities: []string{"Sets strategic direction", "Makes go/no-go decisions"},
	}))
	require.NoError(t, rs.Save(&role.Role{
		Name:             "coo",
		Responsibilities: []string{"Execution quality and velocity"},
	}))

	tm := &team.Team{
		Name: "punt-labs",
		Members: []team.Member{
			{Identity: "jfreeman", Role: "ceo"},
			{Identity: "claude", Role: "coo"},
		},
		Collaborations: []team.Collaboration{
			{From: "coo", To: "ceo", Type: "reports_to"},
		},
	}

	got := BuildTeamContext(tm, rs, s)

	assert.Contains(t, got, "## Team: punt-labs")
	assert.Contains(t, got, "Jim Freeman (jfreeman) — ceo")
	assert.Contains(t, got, "Sets strategic direction")
	assert.Contains(t, got, "Makes go/no-go decisions")
	assert.Contains(t, got, "Claude Agento (claude) — coo")
	assert.Contains(t, got, "Execution quality and velocity")
	assert.Contains(t, got, "### Collaborations")
	assert.Contains(t, got, "coo → ceo (reports_to)")
}

func TestBuildTeamContext_NilTeam(t *testing.T) {
	got := BuildTeamContext(nil, nil, nil)
	assert.Equal(t, "", got)
}

func TestBuildTeamContext_NoRoles(t *testing.T) {
	tm := &team.Team{
		Name: "test-team",
		Members: []team.Member{
			{Identity: "alice", Role: "dev"},
		},
	}

	// No role store — should still emit member name.
	got := BuildTeamContext(tm, nil, nil)

	assert.Contains(t, got, "## Team: test-team")
	assert.Contains(t, got, "alice — dev")
}
