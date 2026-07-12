package claude

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestKeychainRead(t *testing.T) {
	var gotName string
	var gotArgs []string
	run := func(stdin []byte, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args
		if stdin != nil {
			t.Error("Read must not pass stdin")
		}
		return []byte("PAYLOAD\n"), nil
	}
	ks := &keychainStore{run: run, account: "ali"}
	out, err := ks.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(out) != "PAYLOAD" {
		t.Errorf("Read = %q, want trailing newline trimmed", out)
	}
	if gotName != "security" {
		t.Errorf("command = %q, want security", gotName)
	}
	want := []string{"find-generic-password", "-s", "Claude Code-credentials", "-w"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("args = %v, want %v", gotArgs, want)
	}
}

func TestKeychainReadFailureIsNotLoggedIn(t *testing.T) {
	run := func([]byte, string, ...string) ([]byte, error) {
		return nil, errors.New("security: item could not be found in the keychain (exit 44)")
	}
	ks := &keychainStore{run: run, account: "ali"}
	_, err := ks.Read()
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("Read error = %v, want ErrNotLoggedIn in chain", err)
	}
}

func TestKeychainWrite(t *testing.T) {
	var gotStdin []byte
	var gotArgs []string
	run := func(stdin []byte, _ string, args ...string) ([]byte, error) {
		gotStdin, gotArgs = stdin, args
		return nil, nil
	}
	ks := &keychainStore{run: run, account: "ali"}
	raw := `{"claudeAiOauth":{"accessToken":"a\"b"}}`
	if err := ks.Write([]byte(raw)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// The secret travels on stdin of `security -i`, never in argv.
	if !reflect.DeepEqual(gotArgs, []string{"-i"}) {
		t.Errorf("args = %v, want [-i]", gotArgs)
	}
	stdin := string(gotStdin)
	want := `add-generic-password -U -a "ali" -s "Claude Code-credentials" -w "{\"claudeAiOauth\":{\"accessToken\":\"a\\\"b\"}}"` + "\n"
	if stdin != want {
		t.Errorf("stdin =\n%s\nwant\n%s", stdin, want)
	}
	if strings.Contains(strings.Join(gotArgs, " "), "accessToken") {
		t.Error("secret leaked into argv")
	}
}

func TestQuoteSecurity(t *testing.T) {
	tests := []struct{ in, want string }{
		{`plain`, `"plain"`},
		{`a"b`, `"a\"b"`},
		{`a\b`, `"a\\b"`},
		{`a\"b`, `"a\\\"b"`},
	}
	for _, tt := range tests {
		if got := quoteSecurity(tt.in); got != tt.want {
			t.Errorf("quoteSecurity(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}
