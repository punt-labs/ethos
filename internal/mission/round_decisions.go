package mission

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// RoundDecisions is the compact, versioned round-advance authority. It carries
// neither prose nor duplicate budget/evaluator authority.
type RoundDecisions struct {
	Version int             `yaml:"version" json:"version" redact:"keep"`
	Entries []RoundDecision `yaml:"entries" json:"entries"`
}

type RoundDecision struct {
	ID             string `yaml:"id" json:"id" redact:"keep"`
	Round          int    `yaml:"round" json:"round" redact:"keep"`
	CreatedAt      string `yaml:"created_at" json:"created_at" redact:"keep"`
	Author         string `yaml:"author" json:"author" redact:"handle"`
	Recommendation string `yaml:"recommendation" json:"recommendation" redact:"keep"`
}

var operationIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (c *Contract) decision(round int) *RoundDecision {
	if c.RoundDecisions != nil {
		for i := range c.RoundDecisions.Entries {
			if c.RoundDecisions.Entries[i].Round == round {
				return &c.RoundDecisions.Entries[i]
			}
		}
	}
	return nil
}

func (c *Contract) validateDecisions() error {
	d := c.RoundDecisions
	if d == nil {
		return nil // Legacy authority, not initialized empty authority.
	}
	if d.Version != 1 || d.Entries == nil || len(d.Entries) > maxRounds {
		return fmt.Errorf("round_decisions: version must be 1 and entries must be a non-null sequence of at most %d entries", maxRounds)
	}
	seen := map[string]bool{}
	for i, e := range d.Entries {
		if !operationIDPattern.MatchString(e.ID) || seen[e.ID] {
			return fmt.Errorf("round_decisions.entries[%d]: invalid or duplicate id", i)
		}
		seen[e.ID] = true
		// Every crossed round must appear, in order. The current round is
		// the only round which may be missing.
		if e.Round != i+1 || e.Round > c.CurrentRound {
			return fmt.Errorf("round_decisions.entries[%d]: rounds must form the crossed-round prefix through at most current_round", i)
		}
		if strings.TrimSpace(e.Author) == "" || containsControlChar(e.Author) || !validRecommendations[e.Recommendation] {
			return fmt.Errorf("round_decisions.entries[%d]: invalid author or recommendation", i)
		}
		if _, err := time.Parse(time.RFC3339, e.CreatedAt); err != nil {
			return fmt.Errorf("round_decisions.entries[%d]: invalid created_at: %w", i, err)
		}
		if e.Round < c.CurrentRound && IsTerminalRecommendation(e.Recommendation) {
			return fmt.Errorf("round_decisions: crossed round %d has terminal recommendation", e.Round)
		}
	}
	if len(d.Entries) < c.CurrentRound-1 {
		return fmt.Errorf("round_decisions: missing decision for a crossed round")
	}
	return nil
}

// validateAuthorityYAML inspects presence and node kinds before pointer decode:
// yaml.v3 otherwise maps both an omitted block and explicit null to nil.
func validateAuthorityYAML(data []byte) error {
	var doc yaml.Node
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&doc); err != nil {
		return err
	}
	if err := rejectDuplicateYAMLKeys(&doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 {
		return nil
	}
	node := doc.Content[0]
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("contract must be a mapping")
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value != "round_decisions" {
			continue
		}
		d := node.Content[i+1]
		if d.Kind != yaml.MappingNode {
			return fmt.Errorf("round_decisions must be a non-null mapping")
		}
		entries, version := false, false
		for j := 0; j < len(d.Content); j += 2 {
			switch d.Content[j].Value {
			case "version":
				version = d.Content[j+1].ShortTag() == "!!int" && d.Content[j+1].Value == "1"
			case "entries":
				entries = d.Content[j+1].Kind == yaml.SequenceNode
				for _, entry := range d.Content[j+1].Content {
					if entry.Kind != yaml.MappingNode || len(entry.Content) != 10 {
						return fmt.Errorf("round_decisions: each entry requires exactly id, round, created_at, author, recommendation")
					}
					for k := 0; k < len(entry.Content); k += 2 {
						v := entry.Content[k+1]
						tag := "!!str"
						if entry.Content[k].Value == "round" {
							tag = "!!int"
						}
						if v.Kind != yaml.ScalarNode || v.ShortTag() != tag {
							return fmt.Errorf("round_decisions: invalid %s node", entry.Content[k].Value)
						}
					}
				}
			}
		}
		if !version || !entries {
			return fmt.Errorf("round_decisions requires version: 1 and non-null entries")
		}
	}
	return nil
}

func rejectDuplicateYAMLKeys(n *yaml.Node) error {
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Kind != yaml.ScalarNode || seen[key.Value] {
				return fmt.Errorf("duplicate or non-scalar YAML key %q", key.Value)
			}
			seen[key.Value] = true
		}
	}
	// Reject aliases rather than recursively following an untrusted graph.
	if n.Kind == yaml.AliasNode {
		return fmt.Errorf("YAML aliases are not supported in mission records")
	}
	for _, child := range n.Content {
		if err := rejectDuplicateYAMLKeys(child); err != nil {
			return err
		}
	}
	return nil
}
