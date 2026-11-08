package hook

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

var commitMissionID = regexp.MustCompile(`^m-[0-9]{4}-[0-9]{2}-[0-9]{2}-[0-9]{3}$`)
var commitDelegationID = regexp.MustCompile(`^d-[0-9]{4}-[0-9]{2}-[0-9]{2}-[0-9]{3,}$`)

// ApplyCommitMessage adds missing Mission/Delegation trailers to a Git message.
// The caller supplies one located repository policy; there is no global or
// standalone fallback. Rendering uses bounded Git with stdin/stdout in memory.
// Cooperating calls for the same physical message serialize through an S-local
// lock. Git or an editor writing the message itself does not take this lock;
// the hook runs after those writers, as required by Git's commit-msg protocol.
func ApplyCommitMessage(p layout.Policy, path, missionID, delegationID string) error {
	return applyCommitMessage(p, path, missionID, delegationID, os.Rename)
}

func applyCommitMessage(p layout.Policy, path, missionID, delegationID string, rename func(string, string) error) error {
	if p.LocalRoot() == "" || p.CheckoutRecordRoot() == "" {
		return fmt.Errorf("commit message requires a repository policy")
	}
	for _, id := range []struct {
		field, value string
		pattern      *regexp.Regexp
	}{
		{"MISSION_ID", missionID, commitMissionID},
		{"DELEGATION_ID", delegationID, commitDelegationID},
	} {
		if id.value != "" && !id.pattern.MatchString(id.value) {
			return &layout.ValidationError{Field: id.field, Message: "invalid trailer ID"}
		}
	}
	if path == "" {
		return &layout.ValidationError{Field: "message file", Message: "must not be empty"}
	}
	// Resolve host aliases before choosing a lock and a rename target. Staging
	// remains under S even when the message is a symlink to a host file.
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolving commit message: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("locating commit message: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("examining commit message: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("commit message is not a regular file")
	}
	if missionID == "" && delegationID == "" {
		return nil
	}
	lock, err := p.LockPath("commit-message", path)
	if err != nil {
		return err
	}
	if err := layout.EnsureLocalDir(p, filepath.Dir(lock)); err != nil {
		return fmt.Errorf("preparing commit message lock: %w", err)
	}
	if info, err := os.Lstat(lock); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("commit message lock is not a regular file; refusing redirected lock")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("examining commit message lock: %w", err)
	}
	return audit.WithLock(lock, func() error {
		original, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading commit message: %w", err)
		}
		// Git, not a prefix search through the last paragraph, decides which
		// keys already exist. Keep its parser and renderer on the same checkout
		// and environment, including comment and trailer configuration.
		args := []string{"--if-exists=doNothing", "--if-missing=add"}
		for _, trailer := range []struct{ key, value string }{{"Mission", missionID}, {"Delegation", delegationID}} {
			if trailer.value != "" {
				// '=' is always accepted on the command line, even with configured
				// trailer separators. Git chooses the matching output separator.
				args = append(args, "--trailer", trailer.key+"="+trailer.value)
			}
		}
		checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
		env := gitenv.Parsed()
		input := original
		if len(input) != 0 && input[len(input)-1] != '\n' {
			// Without a final newline Git can place an added trailer directly
			// in the subject paragraph, where its own parser cannot recognize it.
			input = append(bytes.Clone(original), '\n')
		}
		rendered, err := interpretCommitTrailers(checkout, env, input, args...)
		if err != nil {
			return err
		}
		if bytes.Equal(original, rendered) {
			return confirmCommitMessageDirectory(path)
		}
		// Git may normalize existing trailers or line endings even when no key
		// was added. Compare parsed trailers, not message-line prefixes, so a
		// true no-op preserves the original bytes (including comment footers).
		existing, err := interpretCommitTrailers(checkout, env, original, "--parse")
		if err != nil {
			return err
		}
		updated, err := interpretCommitTrailers(checkout, env, rendered, "--parse")
		if err != nil {
			return err
		}
		if bytes.Equal(existing, updated) {
			return confirmCommitMessageDirectory(path)
		}
		return installCommitMessage(p, path, rendered, info.Mode().Perm(), rename)
	})
}

// Matching bytes do not prove that an earlier rename's directory sync completed.
// Confirm the resolved physical destination without rewriting or normalizing it.
func confirmCommitMessageDirectory(path string) error {
	if err := syncAgentDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("commit message already contains trailers; crash durability unconfirmed: %w", err)
	}
	return nil
}

func interpretCommitTrailers(checkout string, env []string, message []byte, args ...string) ([]byte, error) {
	var diagnostics bytes.Buffer
	output, err := gitenv.Run(gitenv.Spec{
		Git: "git", Dir: checkout, Env: env, Args: append([]string{"interpret-trailers"}, args...),
		Stdin: bytes.NewReader(message), Stderr: &diagnostics,
	})
	if err != nil {
		return nil, fmt.Errorf("git interpret-trailers failed: %w: %s", err, strings.TrimSpace(diagnostics.String()))
	}
	return output, nil
}

func installCommitMessage(p layout.Policy, path string, data []byte, mode os.FileMode, rename func(string, string) error) (err error) {
	f, err := layout.CreateLocalTemp(p, "commit-message", "")
	if err != nil {
		return fmt.Errorf("staging commit message: %w", err)
	}
	defer func() {
		if removeErr := os.Remove(f.Name()); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("removing commit message stage: %w", removeErr))
		}
	}()
	if err := f.Chmod(mode); err != nil {
		return errors.Join(fmt.Errorf("setting commit message mode: %w", err), f.Close())
	}
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return errors.Join(fmt.Errorf("writing commit message stage: %w", err), f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(fmt.Errorf("syncing commit message stage: %w", err), f.Close())
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing commit message stage: %w", err)
	}
	if err := rename(f.Name(), path); err != nil {
		return fmt.Errorf("installing commit message (no copy or append fallback): %w", err)
	}
	// Rename is the commit point. A directory-sync failure cannot truthfully be
	// reported as an unchanged message; distinguish installed-but-not-durable.
	if err := syncAgentDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("commit message installed, but syncing its directory failed: %w", err)
	}
	return nil
}
