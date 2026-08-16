package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/internal/hook"
)

// Markers persona.go writes into the system prompt. Sourced from
// internal/hook so the two copies cannot drift — renaming a heading there
// renames it here automatically. "## Team:" is a prefix, not a fixed
// string — the team name follows it.
const (
	markerPersonality  = hook.MarkerPersonality
	markerWritingStyle = hook.MarkerWritingStyle
	markerTalents      = hook.MarkerTalents
	markerTeamPrefix   = hook.MarkerTeamPrefix
)

// tokensTODO is the placeholder every report's token fields carry until the
// tokenizer decision lands.
const tokensTODO = "TODO: awaiting tokenizer decision (calibrate-tokens target)"

// envelope is what e2e's custom_callbacks.py writes to a capture file.
type envelope struct {
	TimestampNS        int64  `json:"timestamp_ns"`
	Model              string `json:"model"`
	ProxyServerRequest struct {
		Body json.RawMessage `json:"body"`
	} `json:"proxy_server_request"`
}

// messagesBody is the slice of the Anthropic Messages request this tool
// attributes bytes against. Unknown fields are ignored.
type messagesBody struct {
	System   json.RawMessage   `json:"system"`
	Tools    []json.RawMessage `json:"tools"`
	Messages json.RawMessage   `json:"messages"`
}

// message is one entry of body.Messages — role plus content, the shape
// this tool needs to pull hook-injected text back out. Anthropic's
// Messages API carries no field for host-injected context; Claude Code
// instead appends it as an extra message (observed role "system", but
// this tool does not depend on that — every message's content is
// scanned).
type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// Section is one attributed slice of the system prompt.
type Section struct {
	Label string `json:"label"`
	Bytes int    `json:"bytes"`
}

// ToolSize is one tool's schema size.
type ToolSize struct {
	Name  string `json:"name"`
	Bytes int    `json:"bytes"`
}

// Report is the JSON e2e-attribute prints: byte-level size and attribution
// for one capture. Token counts are not yet computed — see tokensTODO.
type Report struct {
	CaptureFile   string    `json:"capture_file"`
	ScenarioID    string    `json:"scenario_id"`
	TotalBytes    int       `json:"total_bytes"`
	SystemBytes   int       `json:"system_bytes"`
	ToolsBytes    int       `json:"tools_bytes"`
	MessagesBytes int       `json:"messages_bytes"`
	Attribution   []Section `json:"system_attribution"`
	// MessagesTextBytes and MessagesAttribution cover the plain text
	// Claude Code folds into body.Messages — this is where a hermetic:
	// false scenario's SessionStart hook output (ethos's persona block
	// included) actually lands. The Anthropic Messages API has no
	// top-level field for host-injected context, so the CLI appends it
	// as extra messages instead of extending "system"; a marker never
	// appearing in Attribution above does not mean it never fired.
	MessagesTextBytes   int        `json:"messages_text_bytes"`
	MessagesAttribution []Section  `json:"messages_attribution"`
	Tools               []ToolSize `json:"tools"`
	TotalTokens         string     `json:"total_tokens"`
}

// BuildReport parses a capture file's raw bytes and computes its report.
// capturePath is recorded for the reader's benefit only; it is not reread.
func BuildReport(capturePath string, raw []byte) (Report, error) {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Report{}, fmt.Errorf("parsing capture envelope: %w", err)
	}

	var body messagesBody
	if err := json.Unmarshal(env.ProxyServerRequest.Body, &body); err != nil {
		return Report{}, fmt.Errorf("parsing proxy_server_request.body: %w", err)
	}

	systemText, err := systemText(body.System)
	if err != nil {
		return Report{}, fmt.Errorf("parsing system prompt: %w", err)
	}

	msgsText, err := messagesText(body.Messages)
	if err != nil {
		return Report{}, fmt.Errorf("parsing messages: %w", err)
	}

	tools, toolsBytes, err := toolSizes(body.Tools)
	if err != nil {
		return Report{}, fmt.Errorf("parsing tools: %w", err)
	}

	return Report{
		CaptureFile:         capturePath,
		ScenarioID:          env.Model,
		TotalBytes:          len(raw),
		SystemBytes:         len(systemText),
		ToolsBytes:          toolsBytes,
		MessagesBytes:       len(body.Messages),
		Attribution:         attributeSystem(systemText),
		MessagesTextBytes:   len(msgsText),
		MessagesAttribution: attributeMessages(msgsText),
		Tools:               tools,
		TotalTokens:         tokensTODO,
	}, nil
}

// systemText returns the concatenation of every system content block's
// text. The Anthropic Messages API accepts "system" as either a plain
// string or a list of content blocks; both shapes are handled.
func systemText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	if string(raw) == "null" {
		return "", fmt.Errorf("system is JSON null, not absent or a string/content-block list")
	}

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString, nil
	}

	var blocks []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", fmt.Errorf("system is neither a string nor a content-block list: %w", err)
	}
	var b strings.Builder
	for _, block := range blocks {
		b.WriteString(block.Text)
	}
	return b.String(), nil
}

// messagesText concatenates the plain text of every message's content
// blocks, in array order. Claude Code has no top-level field for
// host-injected context, so hook additionalContext (ethos's SessionStart
// persona block included) arrives here as an ordinary message rather than
// as part of "system".
func messagesText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}

	var msgs []message
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return "", fmt.Errorf("messages is not a list of role/content objects: %w", err)
	}

	var b strings.Builder
	for _, m := range msgs {
		text, err := systemText(m.Content)
		if err != nil {
			return "", fmt.Errorf("message (role %q): %w", m.Role, err)
		}
		b.WriteString(text)
	}
	return b.String(), nil
}

// toolSizes returns each tool's name and marshaled-JSON byte size, plus
// their combined size (each tool's raw JSON length, summed).
func toolSizes(tools []json.RawMessage) ([]ToolSize, int, error) {
	sizes := make([]ToolSize, 0, len(tools))
	total := 0
	for _, raw := range tools {
		var named struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &named); err != nil {
			return nil, 0, fmt.Errorf("parsing tool entry: %w", err)
		}
		if named.Name == "" {
			return nil, 0, fmt.Errorf("tool entry missing name: %s", raw)
		}
		sizes = append(sizes, ToolSize{Name: named.Name, Bytes: len(raw)})
		total += len(raw)
	}
	return sizes, total, nil
}

// attributeSystem slices systemText by the persona-block markers
// internal/hook/persona.go writes, in the order they appear. Bytes before
// the first marker (the SDK/base system prompt ethos didn't add) are
// labeled "preamble".
func attributeSystem(text string) []Section {
	return attributeMarkers(text, "preamble")
}

// attributeMessages slices messagesText the same way attributeSystem
// slices the system prompt. Bytes before the first marker are labeled
// "other" rather than "preamble" — this text is ordinary conversation
// and other hooks' additionalContext, not an SDK preamble.
func attributeMessages(text string) []Section {
	return attributeMarkers(text, "other")
}

// attributeMarkers slices text by the persona-block markers
// internal/hook/persona.go writes, in the order they appear. Bytes before
// the first marker are labeled otherLabel.
func attributeMarkers(text string, otherLabel string) []Section {
	type hit struct {
		offset int
		label  string
	}

	var hits []hit
	for _, marker := range []string{markerPersonality, markerWritingStyle, markerTalents} {
		if i := strings.Index(text, marker); i >= 0 {
			hits = append(hits, hit{offset: i, label: marker})
		}
	}
	for start := 0; ; {
		i := strings.Index(text[start:], markerTeamPrefix)
		if i < 0 {
			break
		}
		hits = append(hits, hit{offset: start + i, label: "## Team"})
		start += i + len(markerTeamPrefix)
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].offset < hits[j].offset })

	var sections []Section
	firstOffset := len(text)
	if len(hits) > 0 {
		firstOffset = hits[0].offset
	}
	if firstOffset > 0 {
		sections = append(sections, Section{Label: otherLabel, Bytes: firstOffset})
	}
	for i, h := range hits {
		end := len(text)
		if i+1 < len(hits) {
			end = hits[i+1].offset
		}
		sections = append(sections, Section{Label: h.label, Bytes: end - h.offset})
	}
	return sections
}
