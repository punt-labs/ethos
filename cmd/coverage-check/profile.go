package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type block struct {
	stmts   int
	covered bool
}

func mergeProfiles(out io.Writer, filenames []string) error {
	if len(filenames) < 2 {
		return fmt.Errorf("merging requires at least two profiles")
	}
	merged, err := readProfile(filenames[0])
	if err != nil {
		return err
	}
	for _, filename := range filenames[1:] {
		p, err := readProfile(filename)
		if err != nil {
			return err
		}
		if merged.mode != p.mode {
			return fmt.Errorf("%s: coverage modes differ: %s and %s", filename, merged.mode, p.mode)
		}
		for key, b := range p.blocks {
			if old, ok := merged.blocks[key]; ok {
				if b.stmts != old.stmts {
					return fmt.Errorf("%s: inconsistent statement counts for %s", filename, key)
				}
				b.covered = b.covered || old.covered
			}
			merged.blocks[key] = b
		}
	}
	keys := make([]string, 0, len(merged.blocks))
	for key := range merged.blocks {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	w := bufio.NewWriter(out)
	fmt.Fprintf(w, "mode: %s\n", merged.mode)
	for _, key := range keys {
		b := merged.blocks[key]
		count := 0
		if b.covered {
			count = 1
		}
		fmt.Fprintf(w, "%s %d %d\n", key, b.stmts, count)
	}
	return w.Flush()
}

type coverProfile struct {
	mode   string
	blocks map[string]block
}

var blockLine = regexp.MustCompile(`^(.+:[[:digit:]]+\.[[:digit:]]+,[[:digit:]]+\.[[:digit:]]+) ([0-9]+) ([0-9]+)$`)

func readProfile(filename string) (coverProfile, error) {
	f, err := os.Open(filename)
	if err != nil {
		return coverProfile{}, err
	}
	defer f.Close()
	p := coverProfile{blocks: map[string]block{}}
	sc := bufio.NewScanner(f)
	if sc.Scan() {
		p.mode = strings.TrimPrefix(sc.Text(), "mode: ")
		if sc.Text() != "mode: "+p.mode || p.mode != "set" && p.mode != "count" && p.mode != "atomic" {
			return p, fmt.Errorf("%s: invalid coverage mode %q", filename, sc.Text())
		}
	}
	hasStatements := false
	for sc.Scan() {
		key, b, err := parseBlock(sc.Text())
		if err != nil {
			return p, fmt.Errorf("%s: %w", filename, err)
		}
		if old, ok := p.blocks[key]; ok {
			if b.stmts != old.stmts {
				return p, fmt.Errorf("%s: inconsistent statement counts for %s", filename, key)
			}
			b.covered = b.covered || old.covered
		}
		p.blocks[key] = b
		hasStatements = hasStatements || b.stmts > 0
	}
	if err := sc.Err(); err != nil {
		return p, err
	}
	if !hasStatements {
		return p, fmt.Errorf("%s: no statements in profile", filename)
	}
	return p, nil
}

func parseBlock(line string) (string, block, error) {
	m := blockLine.FindStringSubmatch(line)
	if m != nil {
		var sl, sc, el, ec int
		coordinates := m[1][strings.LastIndexByte(m[1], ':')+1:]
		_, locationErr := fmt.Sscanf(coordinates, "%d.%d,%d.%d", &sl, &sc, &el, &ec)
		stmts, stmtErr := strconv.Atoi(m[2])
		count, countErr := strconv.Atoi(m[3])
		if locationErr == nil && stmtErr == nil && countErr == nil &&
			sl > 0 && sc > 0 && ec > 0 && (sl < el || sl == el && sc <= ec) {
			return m[1], block{stmts, count > 0}, nil
		}
	}
	return "", block{}, fmt.Errorf("malformed coverage block %q", line)
}
