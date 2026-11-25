// Command coverage-check compares Go statement coverage at head and its merge base.
package main

import (
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

const (
	modulePrefix    = "github.com/punt-labs/ethos/v4/"
	tolerance       = 0.1
	autorepairPkg   = "internal/autorepair"
	autorepairFloor = 81.0
	epsilon         = 1e-9
)

type tally struct{ stmts, covered int }

func (t tally) pct() float64 {
	if t.stmts == 0 {
		return 100
	}
	return math.Round(1000*float64(t.covered)/float64(t.stmts)) / 10
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("coverage-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profilePath := fs.String("profile", "coverage.out", "head Go statement coverage profile")
	basePath := fs.String("base-profile", "", "merge-base Go statement coverage profile")
	floorOnly := fs.Bool("autorepair-floor-only", false, "check only the independent autorepair statement coverage floor")
	merge := fs.Bool("merge", false, "write the statement coverage union of the given profiles to stdout")
	root := fs.String("root", ".", "head repository root (to detect internal/autorepair test files)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *floorOnly && (*merge || *basePath != "" || fs.NArg() != 0) {
		fmt.Fprintln(stderr, "coverage-check: -autorepair-floor-only does not accept a base, merge mode, or positional arguments")
		return 2
	}
	if *merge {
		if err := mergeProfiles(stdout, fs.Args()); err != nil {
			fmt.Fprintf(stderr, "coverage-check: %v\n", err)
			return 1
		}
		return 0
	}
	if !*floorOnly && (*basePath == "" || fs.NArg() != 0) {
		fmt.Fprintln(stderr, "coverage-check: -base-profile is required and no positional arguments are allowed")
		return 2
	}
	head, err := readProfile(*profilePath)
	if err != nil {
		fmt.Fprintf(stderr, "coverage-check: head profile: %v\n", err)
		return 1
	}
	autorepairHasTests := hasTestFiles(filepath.Join(*root, autorepairPkg))
	if *floorOnly {
		pkgs, _ := head.tallies()
		var failures []string
		if failure := autorepairFailure(pkgs, autorepairHasTests); failure != "" {
			failures = append(failures, failure)
		}
		return reportFailures(stdout, failures)
	}
	base, err := readProfile(*basePath)
	if err != nil {
		fmt.Fprintf(stderr, "coverage-check: base profile: %v\n", err)
		return 1
	}
	if head.mode != base.mode {
		fmt.Fprintf(stderr, "coverage-check: coverage modes differ: head %s, merge base %s\n", head.mode, base.mode)
		return 1
	}
	return check(stdout, head, base, autorepairHasTests)
}

func check(out io.Writer, head, base coverProfile, autorepairHasTests bool) int {
	pkgs, overall := head.tallies()
	basePkgs, baseOverall := base.tallies()
	names := make([]string, 0, len(pkgs)+len(basePkgs))
	for p := range pkgs {
		names = append(names, p)
	}
	for p := range basePkgs {
		if _, ok := pkgs[p]; !ok {
			names = append(names, p)
		}
	}
	slices.Sort(names)
	var failures []string
	fmt.Fprintf(out, "%-45s %8s %10s\n", "statement coverage by package", "head", "merge base")
	for _, p := range names {
		cur, have := pkgs[p]
		want, measured := basePkgs[p]
		switch {
		case !have:
			fmt.Fprintf(out, "%-45s %8s %9.1f%%  MISSING\n", p, "-", want.pct())
			failures = append(failures, fmt.Sprintf("%s: measured at merge base but absent from head profile", p))
		case !measured:
			fmt.Fprintf(out, "%-45s %7.1f%% %10s  new\n", p, cur.pct(), "-")
		default:
			fmt.Fprintf(out, "%-45s %7.1f%% %9.1f%%\n", p, cur.pct(), want.pct())
			if cur.pct() < want.pct()-tolerance-epsilon {
				failures = append(failures, fmt.Sprintf("%s: %.1f%% is below merge base %.1f%% minus %.1f", p, cur.pct(), want.pct(), tolerance))
			}
		}
	}
	fmt.Fprintf(out, "%-45s %7.1f%% %9.1f%%\n", "overall", overall.pct(), baseOverall.pct())
	if overall.pct() < baseOverall.pct()-epsilon {
		failures = append(failures, fmt.Sprintf("overall: %.1f%% is below merge base %.1f%%", overall.pct(), baseOverall.pct()))
	}
	if failure := autorepairFailure(pkgs, autorepairHasTests); failure != "" {
		failures = append(failures, failure)
	}
	return reportFailures(out, failures)
}

func autorepairFailure(pkgs map[string]tally, hasTests bool) string {
	if !hasTests {
		return ""
	}
	cur, ok := pkgs[autorepairPkg]
	if !ok {
		return autorepairPkg + ": absent from head profile"
	}
	if cur.pct() < autorepairFloor-epsilon {
		return fmt.Sprintf("%s: %.1f%% is below the %.1f floor", autorepairPkg, cur.pct(), autorepairFloor)
	}
	return ""
}

func reportFailures(out io.Writer, failures []string) int {
	for _, failure := range failures {
		fmt.Fprintf(out, "FAIL %s\n", failure)
	}
	if len(failures) > 0 {
		return 1
	}
	fmt.Fprintln(out, "coverage-check: ok")
	return 0
}

func (p coverProfile) tallies() (map[string]tally, tally) {
	pkgs := map[string]tally{}
	var overall tally
	for key, b := range p.blocks {
		pkg := path.Dir(strings.TrimPrefix(key[:strings.LastIndexByte(key, ':')], modulePrefix))
		t := pkgs[pkg]
		t.stmts += b.stmts
		overall.stmts += b.stmts
		if b.covered {
			t.covered += b.stmts
			overall.covered += b.stmts
		}
		pkgs[pkg] = t
	}
	return pkgs, overall
}

func hasTestFiles(dir string) bool {
	m, _ := filepath.Glob(filepath.Join(dir, "*_test.go"))
	return len(m) > 0
}
