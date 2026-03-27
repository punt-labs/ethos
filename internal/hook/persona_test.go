package hook

import (
	"strings"
	"testing"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/stretchr/testify/assert"
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

func TestBuildCondensedPersona_Full(t *testing.T) {
	id := &identity.Identity{
		Name:                "Claude Agento",
		Handle:              "claude",
		Kind:                "agent",
		Personality:         "friendly-direct",
		WritingStyle:        "direct-with-quips",
		PersonalityContent:  "# Friendly Direct\n\nCOO / VP Engineering.\n\n## Core\n\n- Direct but warm\n- Data over adjectives\n- Short answers\n- Ask clarifying questions\n- No hedge stacking",
		WritingStyleContent: "# Direct With Quips\n\nWriting rules.\n\n## Rules\n\n- Under 30 words\n- Lead with answer\n- Replace adjectives with data\n- No filler transitions\n- Concrete over abstract",
		Talents:             []string{"product-strategy", "formal-methods"},
	}

	got := BuildCondensedPersona(id)

	assert.Contains(t, got, "Active persona: Claude Agento (claude)")
	assert.Contains(t, got, "Personality: friendly-direct")
	assert.Contains(t, got, "Writing: direct-with-quips")
	assert.Contains(t, got, "Talents: product-strategy, formal-methods")
}

func TestBuildCondensedPersona_NoPersonality(t *testing.T) {
	id := &identity.Identity{
		Name:                "Alice",
		Handle:              "alice",
		Kind:                "human",
		WritingStyle:        "concise",
		WritingStyleContent: "# Concise\n\nShort.\n\n- No wasted words\n- Data first\n- Active voice",
	}

	got := BuildCondensedPersona(id)

	assert.Contains(t, got, "Active persona: Alice (alice)")
	assert.NotContains(t, got, "Personality:")
	assert.Contains(t, got, "Writing: concise")
}

func TestBuildCondensedPersona_Empty(t *testing.T) {
	id := &identity.Identity{
		Name:   "Empty",
		Handle: "empty",
		Kind:   "agent",
	}

	got := BuildCondensedPersona(id)
	assert.Equal(t, "", got)
}

func TestBuildCondensedPersona_Nil(t *testing.T) {
	got := BuildCondensedPersona(nil)
	assert.Equal(t, "", got)
}
