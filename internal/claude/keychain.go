package claude

import (
	"bytes"
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
		// security exits non-zero both for a missing item and for real
		// failures; either way there are no usable credentials, so surface
		// ErrNotLoggedIn with the underlying detail attached.
		return nil, fmt.Errorf("keychain item %q: %w: %w", keychainService, ErrNotLoggedIn, err)
	}
	return bytes.TrimSuffix(out, []byte("\n")), nil
}

func (s *keychainStore) Write(raw []byte) error {
	// The command is fed to `security -i` on stdin so the secret never
	// appears in the process argument list. -U updates an existing item.
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
