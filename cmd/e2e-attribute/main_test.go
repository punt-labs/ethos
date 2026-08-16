package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestBuildReportRoundTrip(t *testing.T) {
	path := "testdata/sample-capture.jsonl"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	report, err := BuildReport(path, raw)
	if err != nil {
		t.Fatalf("BuildReport: %v", err)
	}

	if report.CaptureFile != path {
		t.Errorf("CaptureFile = %q, want %q", report.CaptureFile, path)
	}
	if report.ScenarioID != "empty-repo" {
		t.Errorf("ScenarioID = %q, want %q", report.ScenarioID, "empty-repo")
	}
	if report.TotalBytes != len(raw) {
		t.Errorf("TotalBytes = %d, want %d", report.TotalBytes, len(raw))
	}
	if report.TotalTokens != tokensTODO {
		t.Errorf("TotalTokens = %q, want the tokenizer TODO placeholder", report.TotalTokens)
	}
	if report.MessagesBytes == 0 {
		t.Error("MessagesBytes = 0, want > 0 (fixture has one message)")
	}

	wantTools := []string{"Read", "Write"}
	if len(report.Tools) != len(wantTools) {
		t.Fatalf("Tools = %d entries, want %d", len(report.Tools), len(wantTools))
	}
	toolsTotal := 0
	for i, want := range wantTools {
		if report.Tools[i].Name != want {
			t.Errorf("Tools[%d].Name = %q, want %q", i, report.Tools[i].Name, want)
		}
		if report.Tools[i].Bytes == 0 {
			t.Errorf("Tools[%d].Bytes = 0, want > 0", i)
		}
		toolsTotal += report.Tools[i].Bytes
	}
	if report.ToolsBytes != toolsTotal {
		t.Errorf("ToolsBytes = %d, want sum of per-tool bytes %d", report.ToolsBytes, toolsTotal)
	}

	wantLabels := []string{"preamble", markerPersonality, markerWritingStyle, markerTalents}
	if len(report.Attribution) != len(wantLabels) {
		t.Fatalf("Attribution = %v, want %d sections labeled %v", report.Attribution, len(wantLabels), wantLabels)
	}
	attrTotal := 0
	for i, want := range wantLabels {
		if report.Attribution[i].Label != want {
			t.Errorf("Attribution[%d].Label = %q, want %q", i, report.Attribution[i].Label, want)
		}
		attrTotal += report.Attribution[i].Bytes
	}
	if attrTotal != report.SystemBytes {
		t.Errorf("sum of Attribution bytes = %d, want SystemBytes %d", attrTotal, report.SystemBytes)
	}

	if report.MessagesTextBytes == 0 {
		t.Error("MessagesTextBytes = 0, want > 0 (fixture has a hook-injected message)")
	}
	wantMsgLabels := []string{"other", markerPersonality, markerWritingStyle, markerTalents, "## Team"}
	if len(report.MessagesAttribution) != len(wantMsgLabels) {
		t.Fatalf("MessagesAttribution = %v, want %d sections labeled %v",
			report.MessagesAttribution, len(wantMsgLabels), wantMsgLabels)
	}
	msgAttrTotal := 0
	for i, want := range wantMsgLabels {
		if report.MessagesAttribution[i].Label != want {
			t.Errorf("MessagesAttribution[%d].Label = %q, want %q", i, report.MessagesAttribution[i].Label, want)
		}
		msgAttrTotal += report.MessagesAttribution[i].Bytes
	}
	if msgAttrTotal != report.MessagesTextBytes {
		t.Errorf("sum of MessagesAttribution bytes = %d, want MessagesTextBytes %d", msgAttrTotal, report.MessagesTextBytes)
	}
}

func TestSystemTextRejectsNull(t *testing.T) {
	_, err := systemText([]byte("null"))
	if err == nil {
		t.Fatal("systemText(null) = nil error, want an error distinguishing null from absent")
	}
}

func TestSystemTextAbsentIsEmpty(t *testing.T) {
	got, err := systemText(nil)
	if err != nil {
		t.Fatalf("systemText(nil) = %v, want no error", err)
	}
	if got != "" {
		t.Errorf("systemText(nil) = %q, want empty string", got)
	}
}

func TestToolSizesRejectsEmptyName(t *testing.T) {
	tools := []json.RawMessage{[]byte(`{"name": ""}`)}
	_, _, err := toolSizes(tools)
	if err == nil {
		t.Fatal("toolSizes with empty name = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "missing name") {
		t.Errorf("toolSizes error = %v, want it to mention the missing name", err)
	}
}

func TestMessagesTextConcatenatesContentBlocks(t *testing.T) {
	raw := []byte(`[
		{"role": "user", "content": "hello"},
		{"role": "system", "content": [{"type": "text", "text": "world"}]}
	]`)
	got, err := messagesText(raw)
	if err != nil {
		t.Fatalf("messagesText: %v", err)
	}
	if want := "helloworld"; got != want {
		t.Errorf("messagesText = %q, want %q", got, want)
	}
}

func TestMessagesTextAbsentIsEmpty(t *testing.T) {
	got, err := messagesText(nil)
	if err != nil {
		t.Fatalf("messagesText(nil) = %v, want no error", err)
	}
	if got != "" {
		t.Errorf("messagesText(nil) = %q, want empty string", got)
	}
}

func TestAttributeMessages(t *testing.T) {
	text := "plain reply" + markerPersonality + "\nbody"
	got := attributeMessages(text)
	want := []string{"other", markerPersonality}
	if len(got) != len(want) {
		t.Fatalf("attributeMessages(%q) = %v, want labels %v", text, got, want)
	}
	for i, label := range want {
		if got[i].Label != label {
			t.Errorf("section %d label = %q, want %q", i, got[i].Label, label)
		}
	}
}

func TestAttributeSystem(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string // expected labels, in order
	}{
		{"empty", "", nil},
		{"no markers", "plain sdk system prompt", []string{"preamble"}},
		{
			"starts at marker",
			markerPersonality + "\n\nbody",
			[]string{markerPersonality},
		},
		{
			"preamble then all three markers",
			"sdk header\n" + markerPersonality + "\nx" + markerWritingStyle + "\ny" + markerTalents + "\nz",
			[]string{"preamble", markerPersonality, markerWritingStyle, markerTalents},
		},
		{
			"team marker uses generic label",
			"pre" + markerTeamPrefix + " Engineering\ncontent",
			[]string{"preamble", "## Team"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := attributeSystem(tt.text)
			if len(got) != len(tt.want) {
				t.Fatalf("attributeSystem(%q) = %v, want labels %v", tt.text, got, tt.want)
			}
			for i, label := range tt.want {
				if got[i].Label != label {
					t.Errorf("section %d label = %q, want %q", i, got[i].Label, label)
				}
			}
		})
	}
}
