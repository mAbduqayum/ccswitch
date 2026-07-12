package claude

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// keychainService is the macOS Keychain generic-password service Claude Code
// stores its credentials under.
const keychainService = "Claude Code-credentials"

// ExecRunner runs an external command and returns its stdout. Injectable so
// the keychain logic is unit-testable off-macOS.
type ExecRunner func(stdin []byte, name string, args ...string) ([]byte, error)

// RealExecRunner executes the command with os/exec.
func RealExecRunner(stdin []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s: %s: %w", name, msg, err)
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return stdout.Bytes(), nil
}

type keychainStore struct {
	run     ExecRunner
	account string
}

func (s *keychainStore) Read() ([]byte, error) {
	out, err := s.run(nil, "security", "find-generic-password", "-s", keychainService, "-w")
	if err != nil {
		// Only a missing item means "not logged in". Other failures (locked
		// keychain, denied access) mean credentials exist but are
		// unreachable — misreporting those as absence could later make a
		// caller skip snapshotting live tokens before overwriting them.
		if isKeychainNotFound(err) {
			return nil, fmt.Errorf("keychain item %q: %w: %w", keychainService, ErrNotLoggedIn, err)
		}
		return nil, fmt.Errorf("read keychain item %q: %w", keychainService, err)
	}
	return bytes.TrimSuffix(out, []byte("\n")), nil
}

// isKeychainNotFound recognizes security(1)'s item-not-found failure: exit
// code 44 (errSecItemNotFound), or its stderr wording for fakes and older
// versions.
func isKeychainNotFound(err error) bool {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 44 {
		return true
	}
	return strings.Contains(err.Error(), "could not be found")
}

func (s *keychainStore) Write(raw []byte) error {
	// The command is fed to `security -i` on stdin so the secret never
	// appears in the process argument list. -U updates an existing item.
	// security -i is line-oriented: a control character in the payload
	// would terminate the quoted string early and let the remainder be
	// parsed as further commands, so reject it outright — real credentials
	// JSON is a single line.
	if bytes.ContainsFunc(raw, func(r rune) bool { return r < 0x20 }) {
		return fmt.Errorf("refusing to write keychain item %q: payload contains control characters", keychainService)
	}
	cmd := fmt.Sprintf("add-generic-password -U -a %s -s %s -w %s\n",
		quoteSecurity(s.account), quoteSecurity(keychainService), quoteSecurity(string(raw)))
	if _, err := s.run([]byte(cmd), "security", "-i"); err != nil {
		return fmt.Errorf("write keychain item %q: %w", keychainService, err)
	}
	return nil
}

func (s *keychainStore) Location() string {
	return fmt.Sprintf("macOS Keychain (service %q)", keychainService)
}

// quoteSecurity wraps s for security(1)'s interactive command tokenizer:
// double quotes with backslash escapes.
func quoteSecurity(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
