package main

import (
	"os"
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
