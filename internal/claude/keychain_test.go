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

func TestKeychainReadErrorClassification(t *testing.T) {
	t.Run("missing item means not logged in", func(t *testing.T) {
		run := func([]byte, string, ...string) ([]byte, error) {
			return nil, errors.New("security: the specified item could not be found in the keychain")
		}
		ks := &keychainStore{run: run, account: "ali"}
		_, err := ks.Read()
		if !errors.Is(err, ErrNotLoggedIn) {
			t.Errorf("Read error = %v, want ErrNotLoggedIn in chain", err)
		}
	})
	t.Run("other failures are not misreported as absence", func(t *testing.T) {
		run := func([]byte, string, ...string) ([]byte, error) {
			return nil, errors.New("security: user interaction is not allowed (keychain locked)")
		}
		ks := &keychainStore{run: run, account: "ali"}
		_, err := ks.Read()
		if err == nil || errors.Is(err, ErrNotLoggedIn) {
			t.Errorf("Read error = %v, must not wrap ErrNotLoggedIn", err)
		}
	})
}

func TestKeychainWriteRejectsControlCharacters(t *testing.T) {
	called := false
	run := func([]byte, string, ...string) ([]byte, error) {
		called = true
		return nil, nil
	}
	ks := &keychainStore{run: run, account: "ali"}
	// A newline would end the quoted -w string early and let the rest of
	// the payload be parsed as further security(1) commands.
	for _, raw := range []string{"{\"a\":\"b\"}\n", "{\"a\":\r\"b\"}", "{\"a\":\"b\x00\"}"} {
		if err := ks.Write([]byte(raw)); err == nil {
			t.Errorf("Write(%q) accepted a control character", raw)
		}
	}
	if called {
		t.Error("runner must not be invoked for rejected payloads")
	}
}

func TestKeychainWriteError(t *testing.T) {
	run := func([]byte, string, ...string) ([]byte, error) {
		return nil, errors.New("security: could not create the item")
	}
	ks := &keychainStore{run: run, account: "ali"}
	if err := ks.Write([]byte(`{}`)); err == nil {
		t.Error("Write must propagate runner errors")
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
