// Package githook chains a marker-delimited section carrying an ethos hook
// script into a repo's git hook, coexisting with any host hook already
// there, and unchains it again. It ports every protection the v4.1.1
// install.sh shell installer carried — marker sections, positional line-2
// standalone identification, non-shell skip-and-warn, unterminated-marker
// abort, symlink-target resolution, temp-fail-loud, host-status
// preservation, and the exit/exec tail warning — into Go, and adds the
// Unchain inverse the shell never had.
//
// Marker scanning is heredoc-aware (internal/textscan): a marker-shaped line
// inside a here-document body is host-owned text, never a real section
// boundary, so Chain/Unchain never delete host data that merely looks like a
// marker. As defense in depth, stripSection refuses to remove a region whose
// line after BEGIN does not carry the ethos ident fingerprint.
package githook

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/internal/textscan"
)

// Result reports the outcome of a Chain or Unchain call.
type Result struct {
	// Path is the file operated on — the symlink target when the hook was a
	// symlink, the hook path otherwise.
	Path string
	// Action is one of: installed, chained, refreshed, skipped-non-shell
	// (Chain); removed, reduced, noop, absent (Unchain).
	Action string
	// Warnings holds advisory messages (symlink redirection, non-shell host,
	// unconditional exit/exec tail).
	Warnings []string
}

// Chain installs the marker section carrying src into the hook at destPath.
//
//   - no hook present: write the standalone marker form (shebang + section)
//   - our section present: strip and re-append in place (idempotent upgrade)
//   - our pre-marker standalone (ident on line 2, no markers): replace with
//     the marker form
//   - a foreign or hybrid shell hook: append our section, preserving the host
//   - a non-shell host: leave it untouched with a warning
//
// tag is the marker tag (e.g. "ETHOS DES-058 SEAL"); ident is the header
// line that positively identifies our own section — both our pre-marker
// standalone (on line 2) and, immediately after BEGIN, the fingerprint that
// guards stripSection. src is the hook script; its shebang is dropped when
// emitted inside a section.
func Chain(destPath string, src []byte, tag, ident string) (Result, error) {
	res := Result{Path: destPath}

	target, warns, err := resolveHookSymlink(destPath)
	if err != nil {
		return res, err
	}
	res.Warnings = append(res.Warnings, warns...)
	destPath = target
	res.Path = target

	data, err := os.ReadFile(destPath)
	if err != nil {
		if os.IsNotExist(err) {
			if err := writeExec(destPath, markerForm(tag, src)); err != nil {
				return res, err
			}
			res.Action = "installed"
			return res, nil
		}
		return res, fmt.Errorf("reading %s: %w", destPath, err)
	}

	// A host ending inside an unterminated heredoc is malformed: its trailing
	// lines — including our own append point — are masked opaque, so any edit
	// would be blind. Refuse rather than guess (parity with claudemd's open
	// fence).
	if textscan.HeredocOpenAtEOF(data) {
		return res, fmt.Errorf("%s ends inside an unterminated here-document — close it and re-run", destPath)
	}

	hadMarker := hasBegin(data, tag)
	if !hadMarker && isOurStandalone(data, ident) {
		if err := writeExec(destPath, markerForm(tag, src)); err != nil {
			return res, err
		}
		res.Action = "refreshed"
		return res, nil
	}

	// Only chain into a shell host. git runs a mixed file with the host's
	// interpreter, so appending a POSIX-sh section to a Python/Node/binary
	// hook would break the host and never run the section. Leaving it
	// untouched is better; doctor then FAILs on the missing seal.
	if !isShellHook(data) {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s has a non-shell shebang — leaving it untouched; the ethos section was not chained in", destPath))
		res.Action = "skipped-non-shell"
		return res, nil
	}

	// A BEGIN with no matching END is a hand-truncated section; stripping it
	// would delete everything after BEGIN. Abort and let the operator fix it.
	if hasBegin(data, tag) && !hasEnd(data, tag) {
		return res, fmt.Errorf(
			"%s has a %q BEGIN marker with no matching END — refusing to edit a truncated hook; fix it by hand", destPath, tag)
	}

	stripped, err := stripSection(data, tag, ident)
	if err != nil {
		return res, fmt.Errorf("%s: %w", destPath, err)
	}
	if w := warnTail(lastEffectiveLine(stripped)); w != "" {
		res.Warnings = append(res.Warnings, destPath+" "+w)
	}

	// Match the host's EOL for the appended section so a CRLF host does not
	// gain LF-only lines (reviewer N1).
	eol := textscan.DetectEOL(stripped)
	section := sectionBytes(tag, src)
	if !bytes.Equal(eol, []byte("\n")) {
		section = bytes.ReplaceAll(section, []byte("\n"), eol)
	}

	out := make([]byte, 0, len(stripped)+len(section)+len(eol))
	out = append(out, stripped...)
	if len(out) > 0 && !endsWithTerminator(out) {
		out = append(out, eol...)
	}
	out = append(out, section...)
	if err := writeExec(destPath, out); err != nil {
		return res, err
	}
	if hadMarker {
		res.Action = "refreshed"
	} else {
		res.Action = "chained"
	}
	return res, nil
}

// Unchain removes our tag's marker section from the hook at destPath. If
// stripping leaves only our standalone (a shebang and nothing else) the file
// is removed; a hook with remaining host content is kept, reduced. A hook
// without our section, or no hook at all, is a no-op. ident is the fingerprint
// stripSection verifies before removing the region. This is the inverse of
// Chain.
func Unchain(destPath, tag, ident string) (Result, error) {
	res := Result{Path: destPath}

	target, _, err := resolveHookSymlink(destPath)
	if err != nil {
		return res, err
	}
	destPath = target
	res.Path = target

	data, err := os.ReadFile(destPath)
	if err != nil {
		if os.IsNotExist(err) {
			res.Action = "absent"
			return res, nil
		}
		return res, fmt.Errorf("reading %s: %w", destPath, err)
	}

	// Refuse a host ending inside an unterminated heredoc: our section could
	// be masked opaque, so "noop" would be a lie and a strip would be blind.
	if textscan.HeredocOpenAtEOF(data) {
		return res, fmt.Errorf("%s ends inside an unterminated here-document — close it and re-run", destPath)
	}

	if !hasBegin(data, tag) {
		res.Action = "noop"
		return res, nil
	}
	if !hasEnd(data, tag) {
		return res, fmt.Errorf(
			"%s has a %q BEGIN marker with no matching END — refusing to edit a truncated hook; fix it by hand", destPath, tag)
	}

	stripped, err := stripSection(data, tag, ident)
	if err != nil {
		return res, fmt.Errorf("%s: %w", destPath, err)
	}
	if isBareShebang(stripped) {
		if err := os.Remove(destPath); err != nil {
			return res, fmt.Errorf("removing %s: %w", destPath, err)
		}
		res.Action = "removed"
		return res, nil
	}
	if err := writeExec(destPath, stripped); err != nil {
		return res, err
	}
	res.Action = "reduced"
	return res, nil
}

// resolveHookSymlink follows one level of symlink so a rename does not
// flatten a dotfile-manager link. It returns the path to operate on and any
// warning about the redirection. A non-symlink path is returned unchanged.
func resolveHookSymlink(destPath string) (string, []string, error) {
	fi, err := os.Lstat(destPath)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return destPath, nil, nil
	}
	target, err := os.Readlink(destPath)
	if err != nil {
		return destPath, nil, fmt.Errorf("resolving symlink %s: %w", destPath, err)
	}
	if target == "" {
		return destPath, nil, fmt.Errorf("empty symlink target for %s", destPath)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(destPath), target)
	}
	warn := fmt.Sprintf("%s is a symlink — updating its target %s instead", destPath, target)
	return target, []string{warn}, nil
}

// markerForm returns a fresh hook: a shebang plus the marker-delimited
// section. Every state Chain creates is marker-managed, so a re-run always
// resolves through the marker branch.
func markerForm(tag string, src []byte) []byte {
	var b bytes.Buffer
	b.WriteString("#!/bin/sh\n")
	b.Write(sectionBytes(tag, src))
	return b.Bytes()
}

// sectionBytes returns src (minus its shebang) fenced by the tag markers.
func sectionBytes(tag string, src []byte) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# --- BEGIN %s ---\n", tag)
	b.Write(stripShebang(src))
	fmt.Fprintf(&b, "# --- END %s ---\n", tag)
	return b.Bytes()
}

// stripShebang drops a leading "#!" line from src.
func stripShebang(src []byte) []byte {
	if !bytes.HasPrefix(src, []byte("#!")) {
		return src
	}
	if i := bytes.IndexByte(src, '\n'); i >= 0 {
		return src[i+1:]
	}
	return nil
}

// stripSection removes the lines from a real BEGIN-tag marker to its matching
// END-tag marker, inclusive, preserving every other byte. "Real" excludes any
// marker-shaped line inside a heredoc body (host-owned text). A versioned
// marker (e.g. "ETHOS DES-058 SEAL v4.1.0") matches by prefix, so an interim
// versioned section is replaced.
//
// Defense in depth: before removing a region it verifies the line immediately
// after BEGIN carries the ethos ident fingerprint. A region that does not is
// not an ethos-written section — stripSection refuses with a named error
// rather than delete host data, so even a scanner gap degrades to a loud
// refusal instead of silent deletion.
func stripSection(data []byte, tag, ident string) ([]byte, error) {
	lines := textscan.SplitKeepEnds(data)
	mask := textscan.HeredocMask(data)
	begin := "# --- BEGIN " + tag
	end := "# --- END " + tag
	out := make([]byte, 0, len(data))
	for i := 0; i < len(lines); {
		if !mask[i] && strings.HasPrefix(textscan.StripTerminator(lines[i]), begin) {
			if i+1 >= len(lines) || !strings.Contains(textscan.StripTerminator(lines[i+1]), ident) {
				return nil, fmt.Errorf(
					"%q section does not carry the ethos fingerprint %q — refusing to remove a region that is not an ethos-written section; fix it by hand", tag, ident)
			}
			i++
			for i < len(lines) {
				last := !mask[i] && strings.HasPrefix(textscan.StripTerminator(lines[i]), end)
				i++
				if last {
					break
				}
			}
			continue
		}
		out = append(out, lines[i]...)
		i++
	}
	return out, nil
}

// hasBegin reports whether data carries a real (non-heredoc) BEGIN marker for tag.
func hasBegin(data []byte, tag string) bool {
	return hasLinePrefix(data, "# --- BEGIN "+tag)
}

// hasEnd reports whether data carries a real (non-heredoc) END marker for tag.
func hasEnd(data []byte, tag string) bool {
	return hasLinePrefix(data, "# --- END "+tag)
}

// hasAnyBegin reports whether data carries any real (non-heredoc) BEGIN marker.
func hasAnyBegin(data []byte) bool {
	return hasLinePrefix(data, "# --- BEGIN ")
}

// hasLinePrefix reports whether any non-heredoc line has the given prefix.
func hasLinePrefix(data []byte, prefix string) bool {
	lines := textscan.SplitKeepEnds(data)
	mask := textscan.HeredocMask(data)
	for i, raw := range lines {
		if !mask[i] && strings.HasPrefix(textscan.StripTerminator(raw), prefix) {
			return true
		}
	}
	return false
}

// isOurStandalone reports whether data is our own pre-marker standalone: its
// header ident sits on line 2 and it carries no marker section anywhere.
// Checking line 2 specifically distinguishes our standalone from a
// `cat hook.sh >> hook` hybrid, whose host content pushes our header mid-file.
func isOurStandalone(data []byte, ident string) bool {
	if hasAnyBegin(data) {
		return false
	}
	lines := textscan.SplitKeepEnds(data)
	if len(lines) < 2 {
		return false
	}
	return strings.Contains(textscan.StripTerminator(lines[1]), ident)
}

// isShellHook reports whether data's shebang names a shell-family
// interpreter, or there is no shebang (git runs such a hook via sh).
func isShellHook(data []byte) bool {
	first := data
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		first = data[:i]
	}
	line := strings.TrimRight(string(first), "\r")
	if !strings.HasPrefix(line, "#!") {
		return true
	}
	fields := strings.Fields(line[2:])
	if len(fields) == 0 {
		return true
	}
	interp := filepath.Base(fields[0])
	if interp == "env" && len(fields) > 1 {
		interp = filepath.Base(fields[1])
	}
	switch interp {
	case "sh", "bash", "dash", "ksh", "zsh", "mksh", "ash":
		return true
	}
	return false
}

// isBareShebang reports whether data is only a shebang line and blank lines —
// nothing of the host's own remains.
func isBareShebang(data []byte) bool {
	first := true
	for _, raw := range textscan.SplitKeepEnds(data) {
		c := textscan.StripTerminator(raw)
		if first {
			first = false
			if strings.HasPrefix(c, "#!") {
				continue
			}
		}
		if strings.TrimSpace(c) == "" {
			continue
		}
		return false
	}
	return true
}

// lastEffectiveLine returns the last non-blank, non-comment line, trimmed. A
// trailing comment after an exit must not hide it.
func lastEffectiveLine(data []byte) string {
	last := ""
	for _, raw := range textscan.SplitKeepEnds(data) {
		c := textscan.StripTerminator(raw)
		if strings.TrimSpace(c) == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimLeft(c, " \t"), "#") {
			continue
		}
		last = strings.TrimSpace(c)
	}
	return last
}

// warnTail returns a warning when last is an unconditional exit or an exec of
// a program — either bypasses the appended section. An exec with an fd
// redirection target (exec 3>&1, exec >log) is an fd builtin that does not
// replace the shell, so those forms are not flagged.
func warnTail(last string) string {
	switch {
	case last == "exit" || strings.HasPrefix(last, "exit ") || strings.HasPrefix(last, "exit;"):
		return "ends in an unconditional 'exit' — the ethos section may not run"
	case strings.HasPrefix(last, "exec "):
		rest := last[len("exec "):]
		if rest == "" {
			return ""
		}
		switch rest[0] {
		case '<', '>', '&':
			return ""
		}
		if rest[0] >= '0' && rest[0] <= '9' {
			return ""
		}
		return "ends in an unconditional 'exec' — the ethos section may not run"
	}
	return ""
}

// writeExec writes data to a temp file in dest's own directory, renames it
// over dest, and makes it executable. A temp-file failure aborts loudly.
func writeExec(dest string, data []byte) error {
	dir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dest)+".*")
	if err != nil {
		return fmt.Errorf("cannot create a temp file next to %s — hook not installed: %w", dest, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", name, err)
	}
	if err := os.Chmod(name, 0o755); err != nil {
		return fmt.Errorf("setting mode on %s: %w", name, err)
	}
	if err := os.Rename(name, dest); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", name, dest, err)
	}
	return nil
}

// endsWithTerminator reports whether data's last byte is a line terminator.
func endsWithTerminator(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	last := data[len(data)-1]
	return last == '\n' || last == '\r'
}
