// Command e2e-attribute reads one L4 capture file and reports its byte-level
// size and attribution: total bytes, system-prompt bytes (sliced by the
// persona-block markers internal/hook/persona.go defines), tool-schema bytes
// per tool, and message-history bytes.
//
// Tokenizing and diffing against a committed baseline are out of scope for
// this initial land — the tokenizer choice (design §6) isn't settled yet.
// The report's token fields are always "TODO: awaiting tokenizer decision
// (calibrate-tokens target)".
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func main() {
	var capturePath, outPath string
	flag.StringVar(&capturePath, "capture", "", "path to a single L4 capture file (required)")
	flag.StringVar(&outPath, "out", "", "path to write the JSON report (default: stdout)")
	flag.Parse()

	if capturePath == "" {
		fmt.Fprintln(os.Stderr, "e2e-attribute: -capture is required")
		os.Exit(2)
	}

	raw, err := os.ReadFile(capturePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e-attribute: reading %s: %v\n", capturePath, err)
		os.Exit(1)
	}

	report, err := BuildReport(capturePath, raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e-attribute: building report for %s: %v\n", capturePath, err)
		os.Exit(1)
	}

	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e-attribute: encoding report: %v\n", err)
		os.Exit(1)
	}
	out = append(out, '\n')

	if outPath == "" {
		os.Stdout.Write(out)
		return
	}
	if err := os.WriteFile(outPath, out, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "e2e-attribute: writing %s: %v\n", outPath, err)
		os.Exit(1)
	}
}
