// Command coverage-check compares per-package statement coverage from a Go
// coverprofile against a committed baseline file.
//
// Baseline format (coverage-baseline.txt), one entry per line:
//
//	# comment                     preserved verbatim by -write
//	overall-target 85.0           minimum overall coverage; preserved by -write
//	overall-current 81.8          informational; rewritten by -write
//	internal/audit 80.2           package path and statement coverage percent
//
//	# unmeasured: internal/x ...  package whose tests fail on the generating host;
//	                              kept out of the baseline, the gate, and overall
//	# os: darwin                  GOOS the baseline was measured on
//
// check fails when a package falls more than 0.1 point below its baseline, when
// overall coverage is below overall-target, when a baselined package is absent
// from the profile, or when internal/autorepair (once it has test files) is
// below 81.0, the figure the shipped engine measured on linux (the plan's 95
// is the goal, raised as the steps gain tests). internal/autorepair cannot be
// listed as unmeasured.
//
// OS: statement counts differ per GOOS (platform files, build tags, runtime.GOOS
// branches), so no tolerance can be both sound and tight. -write records the
// GOOS it ran for as exactly one "# os:" line; check fails when that differs
// from the host's GOOS. Regenerate the baseline on the OS that runs the gate
// (make coverage-baseline-from-ci for linux).
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

const (
	modulePrefix     = "github.com/punt-labs/ethos/v4/"
	tolerance        = 0.1
	osPrefix         = "# os:"
	autorepairPkg    = "internal/autorepair"
	autorepairFloor  = 81.0
	overallTargetKey = "overall-target"
	overallCurrent   = "overall-current"
	epsilon          = 1e-9
)

type tally struct{ stmts, covered int }

func (t tally) pct() float64 {
	if t.stmts == 0 {
		return 100
	}
	return 100 * float64(t.covered) / float64(t.stmts)
}

type baseline struct {
	comments []string
	target   float64
	hasTgt   bool
	pkgs     map[string]float64
	os       string // GOOS the baseline was measured on; empty when unrecorded
	sawOS    bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("coverage-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profilePath := fs.String("profile", "coverage.out", "Go coverprofile to read")
	basePath := fs.String("baseline", "coverage-baseline.txt", "baseline file")
	root := fs.String("root", ".", "repository root (to detect internal/autorepair test files)")
	write := fs.Bool("write", false, "regenerate the baseline from the profile")
	goos := fs.String("os", runtime.GOOS, "GOOS to compare against (and, with -write, to record in) the baseline")
	exclude := fs.String("exclude", "", "with -write: comma-separated packages whose tests fail on this host; recorded as unmeasured, left out of the baseline and the overall figure")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *write && !knownGOOS[*goos] {
		fmt.Fprintf(stderr, "coverage-check: -os %q is not a known GOOS\n", *goos)
		return 2
	}
	pkgs, overall, err := readProfile(*profilePath)
	if err != nil {
		fmt.Fprintf(stderr, "coverage-check: %v\n", err)
		return 1
	}
	if *write {
		var skipped []string
		for _, p := range strings.Split(*exclude, ",") {
			if p = strings.TrimSpace(p); p != "" {
				skipped = append(skipped, p)
			}
		}
		if prev, err := readBaseline(*basePath); err == nil {
			// Existing unmeasured comments are an implicit exclude list, so a
			// regeneration without -exclude never writes a line for them.
			for _, u := range prev.unmeasured() {
				if _, ok := pkgs[u]; ok {
					fmt.Fprintf(stderr, "WARN %s has coverage data but is listed unmeasured; remove its comment to baseline it\n", u)
				}
				skipped = append(skipped, u)
			}
		}
		pkgs, overall = withoutPackages(pkgs, skipped)
		if err := writeBaseline(*basePath, pkgs, overall, skipped, *goos); err != nil {
			fmt.Fprintf(stderr, "coverage-check: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "wrote %s (overall %.1f%%, %d packages)\n", *basePath, overall.pct(), len(pkgs))
		return 0
	}
	base, err := readBaseline(*basePath)
	if err != nil {
		fmt.Fprintf(stderr, "coverage-check: %v\n", err)
		return 1
	}
	return check(stdout, pkgs, overall, base, hasTestFiles(filepath.Join(*root, autorepairPkg)), *goos, *root)
}

// unmeasured returns the packages named by "# unmeasured: <pkg> ..." comments.
func (b baseline) unmeasured() []string {
	const prefix = "# unmeasured: "
	var out []string
	for _, c := range b.comments {
		if rest, ok := strings.CutPrefix(c, prefix); ok {
			if f := strings.Fields(rest); len(f) > 0 {
				out = append(out, f[0])
			}
		}
	}
	return out
}

func check(out io.Writer, pkgs map[string]tally, overall tally, base baseline, autorepairHasTests bool, goos, root string) int {
	var failures []string
	skipped := base.unmeasured()
	for _, s := range skipped {
		if s == autorepairPkg {
			fmt.Fprintf(out, "FAIL %s cannot be unmeasured: it has a %.1f floor that an unmeasured entry would bypass\n", autorepairPkg, autorepairFloor)
			return 1
		}
	}
	if base.os == "" {
		fmt.Fprintln(out, "FAIL baseline records no \"# os:\" line; regenerate it with -write")
		return 1
	}
	if base.os != goos {
		fmt.Fprintf(out, "FAIL baseline was generated on %s; this host is %s; regenerate the baseline on %s\n", base.os, goos, base.os)
		return 1
	}
	if len(skipped) > 0 {
		pkgs, overall = withoutPackages(pkgs, skipped)
		for _, s := range skipped {
			fmt.Fprintf(out, "%-45s unmeasured (excluded from the gate)\n", s)
		}
	}
	names := map[string]bool{}
	for p := range pkgs {
		names[p] = true
	}
	for p := range base.pkgs {
		names[p] = true
	}
	sorted := make([]string, 0, len(names))
	for p := range names {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)

	fmt.Fprintf(out, "%-45s %8s %8s\n", "package", "current", "baseline")
	for _, p := range sorted {
		cur, have := pkgs[p]
		want, baselined := base.pkgs[p]
		switch {
		case !have:
			fmt.Fprintf(out, "%-45s %8s %7.1f%%  MISSING\n", p, "-", want)
			failures = append(failures, fmt.Sprintf("%s: baselined at %.1f%% but absent from the profile", p, want))
		case !baselined:
			fmt.Fprintf(out, "%-45s %7.1f%% %8s  new\n", p, cur.pct(), "-")
			fmt.Fprintf(out, "WARN %s is not in the baseline and is ungated until the next baseline\n", p)
		default:
			mark := ""
			if cur.pct() < want-tolerance-epsilon {
				mark = "  BELOW BASELINE"
				failures = append(failures, fmt.Sprintf("%s: %.1f%% is below baseline %.1f%% minus %.1f", p, cur.pct(), want, tolerance))
			}
			fmt.Fprintf(out, "%-45s %7.1f%% %7.1f%%%s\n", p, cur.pct(), want, mark)
		}
	}
	mark := ""
	if base.hasTgt && overall.pct() < base.target-epsilon {
		mark = "  BELOW TARGET"
		failures = append(failures, fmt.Sprintf("overall: %.1f%% is below overall target %.1f%%", overall.pct(), base.target))
	}
	fmt.Fprintf(out, "%-45s %7.1f%% %7.1f%%%s\n", "overall (target)", overall.pct(), base.target, mark)
	if !base.hasTgt {
		failures = append(failures, "baseline has no "+overallTargetKey+" line")
	}
	if cur, ok := pkgs[autorepairPkg]; ok && autorepairHasTests && cur.pct() < autorepairFloor-epsilon {
		failures = append(failures, fmt.Sprintf("%s: %.1f%% is below the %.1f floor", autorepairPkg, cur.pct(), autorepairFloor))
	}
	for _, f := range failures {
		fmt.Fprintf(out, "FAIL %s\n", f)
	}
	if len(failures) > 0 {
		return 1
	}
	fmt.Fprintln(out, "coverage-check: ok")
	return 0
}

func readProfile(p string) (map[string]tally, tally, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, tally{}, fmt.Errorf("profile not found; make test writes it: %w", err)
	}
	defer f.Close()
	type block struct {
		stmts int
		count int
	}
	blocks := map[string]block{} // key: file:range; merged by max count
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			first = false
			if !strings.HasPrefix(line, "mode:") {
				return nil, tally{}, fmt.Errorf("%s: not a coverprofile (first line %q)", p, line)
			}
			continue
		}
		if line == "" {
			continue
		}
		sp := strings.Fields(line)
		if len(sp) != 3 {
			return nil, tally{}, fmt.Errorf("%s: malformed line %q", p, line)
		}
		n, err1 := strconv.Atoi(sp[1])
		c, err2 := strconv.Atoi(sp[2])
		if err1 != nil || err2 != nil || n < 0 || c < 0 {
			return nil, tally{}, fmt.Errorf("%s: malformed counts in %q", p, line)
		}
		b := blocks[sp[0]]
		b.stmts = n
		if c > b.count {
			b.count = c
		}
		blocks[sp[0]] = b
	}
	if err := sc.Err(); err != nil {
		return nil, tally{}, err
	}
	pkgs := map[string]tally{}
	var overall tally
	for key, b := range blocks {
		file := key[:strings.LastIndex(key, ":")]
		pkg := path.Dir(strings.TrimPrefix(file, modulePrefix))
		t := pkgs[pkg]
		t.stmts += b.stmts
		overall.stmts += b.stmts
		if b.count > 0 {
			t.covered += b.stmts
			overall.covered += b.stmts
		}
		pkgs[pkg] = t
	}
	if overall.stmts == 0 {
		return nil, tally{}, fmt.Errorf("%s: no statements in profile", p)
	}
	return pkgs, overall, nil
}

func readBaseline(p string) (baseline, error) {
	b := baseline{pkgs: map[string]float64{}}
	f, err := os.Open(p)
	if err != nil {
		return b, fmt.Errorf("baseline not found; make coverage-baseline writes it: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if rest, ok := strings.CutPrefix(line, osPrefix); ok {
				if b.os != "" || b.sawOS {
					return b, fmt.Errorf("%s: duplicate %q line %q", p, osPrefix, line)
				}
				b.os, b.sawOS = strings.TrimSpace(rest), true
				if !knownGOOS[b.os] {
					return b, fmt.Errorf("%s: %q names %q, not a known GOOS", p, line, b.os)
				}
			}
			b.comments = append(b.comments, line)
			continue
		}
		sp := strings.Fields(line)
		if len(sp) != 2 {
			return b, fmt.Errorf("%s: malformed line %q", p, line)
		}
		v, err := strconv.ParseFloat(sp[1], 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 100 {
			return b, fmt.Errorf("%s: bad percentage in %q (want a number in [0,100])", p, line)
		}
		switch sp[0] {
		case overallTargetKey:
			if b.hasTgt {
				return b, fmt.Errorf("%s: duplicate %s line %q", p, overallTargetKey, line)
			}
			b.target, b.hasTgt = v, true
		case overallCurrent:
		default:
			b.pkgs[sp[0]] = v
		}
	}
	return b, sc.Err()
}

// withoutPackages drops the named packages and recomputes the overall tally
// from what remains.
func withoutPackages(pkgs map[string]tally, skip []string) (map[string]tally, tally) {
	drop := map[string]bool{}
	for _, s := range skip {
		drop[s] = true
	}
	kept := map[string]tally{}
	var overall tally
	for p, t := range pkgs {
		if drop[p] {
			continue
		}
		kept[p] = t
		overall.stmts += t.stmts
		overall.covered += t.covered
	}
	return kept, overall
}

func writeBaseline(p string, pkgs map[string]tally, overall tally, skipped []string, goos string) error {
	prev, err := readBaseline(p)
	if err != nil && !os.IsNotExist(unwrap(err)) {
		return err
	}
	have := map[string]bool{}
	for _, c := range prev.comments {
		have[c] = true
	}
	declared := map[string]bool{}
	for _, u := range prev.unmeasured() {
		declared[u] = true
	}
	for _, s := range skipped {
		c := "# unmeasured: " + s + " (tests fail on the host that generated this baseline; no floor enforced)"
		if !have[c] && !declared[s] {
			prev.comments = append(prev.comments, c)
			have[c] = true
		}
	}
	target := prev.target
	if !prev.hasTgt {
		// Current floor to one decimal: the gate starts at "no regression" and
		// the owner raises it toward 85.0 as coverage improves.
		target = math.Floor(overall.pct()*10+epsilon) / 10
	}
	var b strings.Builder
	b.WriteString("# Generated by `make coverage-baseline`; checked by `make coverage-check`.\n")
	b.WriteString("# A package may drop at most 0.1 point below its line. Goal for overall-target: 85.0.\n")
	for _, c := range prev.comments {
		if strings.HasPrefix(c, "# Generated by") || strings.HasPrefix(c, "# A package may drop") || strings.HasPrefix(c, osPrefix) {
			continue
		}
		b.WriteString(c + "\n")
	}
	b.WriteString(osPrefix + " " + goos + "\n")
	fmt.Fprintf(&b, "%s %.1f\n%s %.1f\n", overallTargetKey, target, overallCurrent, overall.pct())
	names := make([]string, 0, len(pkgs))
	for n := range pkgs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(&b, "%s %.1f\n", n, pkgs[n].pct())
	}
	return os.WriteFile(p, []byte(b.String()), 0o644)
}

func unwrap(err error) error {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		err = u.Unwrap()
	}
}

func hasTestFiles(dir string) bool {
	m, _ := filepath.Glob(filepath.Join(dir, "*_test.go"))
	return len(m) > 0
}

// knownGOOS lists the GOOS values Go recognises in a _<goos>.go file name.
var knownGOOS = map[string]bool{
	"aix": true, "android": true, "darwin": true, "dragonfly": true, "freebsd": true,
	"hurd": true, "illumos": true, "ios": true, "js": true, "linux": true, "nacl": true,
	"netbsd": true, "openbsd": true, "plan9": true, "solaris": true, "wasip1": true,
	"windows": true, "zos": true, "unix": true,
}
