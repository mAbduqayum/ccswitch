package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mAbduqayum/ccswitch/internal/store"
)

func TestDiscoverNotLoggedIn(t *testing.T) {
	a := newTestApp(t)
	d, err := a.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if d.Status != NotLoggedIn {
		t.Errorf("status = %v, want NotLoggedIn", d.Status)
	}
}

func TestDiscoverCredsReadError(t *testing.T) {
	a := newTestApp(t)
	readErr := errors.New("keychain locked")
	a.Creds = &fakeCreds{readErr: readErr}
	if _, err := a.Discover(); !errors.Is(err, readErr) {
		t.Errorf("Discover error = %v, want the credential read error", err)
	}
}

func TestDiscoverMalformedCreds(t *testing.T) {
	a := newTestApp(t)
	writeLiveCreds(t, a, []byte("not json"))
	if _, err := a.Discover(); err == nil {
		t.Fatal("malformed credentials must be an error, not a status")
	}
}

func TestDiscoverNoProfile(t *testing.T) {
	tests := []struct {
		name   string
		config []byte // nil = no config file at all
	}{
		{"no config file", nil},
		{"config without oauthAccount", []byte(`{"theme": "dark"}`)},
		{"corrupt config", []byte("{")},
		{"profile without uuid", []byte(`{"oauthAccount": {"emailAddress": "a@x.com"}}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestApp(t)
			live := credsJSON("a", freshExpiry, refreshOK)
			writeLiveCreds(t, a, live)
			if tt.config != nil {
				if err := os.WriteFile(a.Env.ConfigPath(), tt.config, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			d, err := a.Discover()
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if d.Status != NoProfile {
				t.Errorf("status = %v, want NoProfile", d.Status)
			}
			if !bytes.Equal(d.RawCreds, live) {
				t.Error("RawCreds must carry the live bytes even without a profile")
			}
		})
	}
}

func TestDiscoverUnknown(t *testing.T) {
	a := newTestApp(t)
	writeLiveCreds(t, a, credsJSON("a", freshExpiry, refreshOK))
	writeLiveConfig(t, a, profileJSON("uuid-a", "a@x.com"))

	d, err := a.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if d.Status != Unknown {
		t.Fatalf("status = %v, want Unknown", d.Status)
	}
	if d.Profile.AccountUUID != "uuid-a" || d.Profile.EmailAddress != "a@x.com" {
		t.Errorf("profile = %+v", d.Profile)
	}
	if !bytes.Equal(d.RawProfile, profileJSON("uuid-a", "a@x.com")) {
		t.Errorf("RawProfile = %s", d.RawProfile)
	}
}

func TestDiscoverKnown(t *testing.T) {
	t.Run("matched by uuid", func(t *testing.T) {
		a := newTestApp(t)
		writeLiveCreds(t, a, credsJSON("a", freshExpiry, refreshOK))
		writeLiveConfig(t, a, profileJSON("uuid-a", "a@x.com"))
		saveState(t, a, store.State{Accounts: []store.Account{{UUID: "uuid-a", Email: "a@x.com"}}})

		d, err := a.Discover()
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		if d.Status != Known || d.Account.UUID != "uuid-a" {
			t.Errorf("got status %v account %+v", d.Status, d.Account)
		}
	})
	t.Run("matched by email fallback", func(t *testing.T) {
		a := newTestApp(t)
		writeLiveCreds(t, a, credsJSON("a", freshExpiry, refreshOK))
		writeLiveConfig(t, a, profileJSON("uuid-new", "a@x.com"))
		// The store knows the email under an older uuid.
		saveState(t, a, store.State{Accounts: []store.Account{{UUID: "uuid-old", Email: "a@x.com"}}})

		d, err := a.Discover()
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		if d.Status != Known || d.Account.UUID != "uuid-old" {
			t.Errorf("got status %v account %+v, want Known uuid-old", d.Status, d.Account)
		}
	})
}

func TestDiscoverCorruptStateErrors(t *testing.T) {
	a := newTestApp(t)
	writeLiveCreds(t, a, credsJSON("a", freshExpiry, refreshOK))
	writeLiveConfig(t, a, profileJSON("uuid-a", "a@x.com"))
	if err := os.MkdirAll(a.Store.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Store.Dir(), "state.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Discover(); err == nil {
		t.Fatal("a corrupt state file must surface as an error")
	}
}

func TestAddCurrent(t *testing.T) {
	a := newTestApp(t)
	live := credsJSON("a", freshExpiry, refreshOK)
	writeLiveCreds(t, a, live)
	writeLiveConfig(t, a, profileJSON("uuid-a", "a@x.com"))

	d, err := a.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	acct, err := a.AddCurrent(d)
	if err != nil {
		t.Fatalf("AddCurrent: %v", err)
	}
	if acct.UUID != "uuid-a" || acct.Email != "a@x.com" || !acct.AddedAt.Equal(testNow) {
		t.Errorf("account = %+v", acct)
	}

	st := loadState(t, a)
	if st.Active != "uuid-a" || len(st.Accounts) != 1 {
		t.Errorf("state = %+v", st)
	}
	if got := readSnapshot(t, a, "uuid-a"); !bytes.Equal(got, live) {
		t.Error("snapshot bytes differ from live credentials")
	}
	p, err := a.Store.ReadProfile("uuid-a")
	if err != nil || !bytes.Equal(p, profileJSON("uuid-a", "a@x.com")) {
		t.Errorf("stored profile = %s, %v", p, err)
	}
}

func TestAddCurrentRequiresUnknown(t *testing.T) {
	a := newTestApp(t)
	if _, err := a.AddCurrent(Discovery{Status: NotLoggedIn}); err == nil {
		t.Error("AddCurrent must reject a non-Unknown discovery")
	}
}

func TestAddCurrentConcurrentAddIsIdempotent(t *testing.T) {
	a := newTestApp(t)
	writeLiveCreds(t, a, credsJSON("a", freshExpiry, refreshOK))
	writeLiveConfig(t, a, profileJSON("uuid-a", "a@x.com"))
	d, err := a.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	// Another process registers the account between Discover and AddCurrent.
	raced := store.Account{UUID: "uuid-a", Email: "a@x.com", Alias: "kept", AddedAt: testNow}
	saveState(t, a, store.State{Active: "uuid-a", Accounts: []store.Account{raced}})

	acct, err := a.AddCurrent(d)
	if err != nil {
		t.Fatalf("AddCurrent: %v", err)
	}
	if acct.Alias != "kept" {
		t.Errorf("got %+v, want the already-registered account back", acct)
	}
	if st := loadState(t, a); len(st.Accounts) != 1 {
		t.Errorf("account duplicated: %+v", st.Accounts)
	}
}

// syncBaseline builds a world where SyncKnown has nothing to do; each test
// perturbs exactly one dimension.
func syncBaseline(t *testing.T) (*App, []byte) {
	t.Helper()
	a := newTestApp(t)
	live := credsJSON("a", staleExpiry, refreshOK)
	writeLiveCreds(t, a, live)
	writeLiveConfig(t, a, profileJSON("uuid-a", "a@x.com"))
	saveState(t, a, store.State{Active: "uuid-a", Accounts: []store.Account{{UUID: "uuid-a", Email: "a@x.com", AddedAt: testNow}}})
	writeSnapshot(t, a, "uuid-a", live)
	writeProfile(t, a, "uuid-a", profileJSON("uuid-a", "a@x.com"))
	return a, live
}

func discoverKnown(t *testing.T, a *App) Discovery {
	t.Helper()
	d, err := a.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if d.Status != Known {
		t.Fatalf("status = %v, want Known", d.Status)
	}
	return d
}

func syncKnown(t *testing.T, a *App, d Discovery) bool {
	t.Helper()
	written, err := a.SyncKnown(d)
	if err != nil {
		t.Fatalf("SyncKnown: %v", err)
	}
	return written
}

func TestSyncKnown(t *testing.T) {
	t.Run("everything in sync writes nothing", func(t *testing.T) {
		a, _ := syncBaseline(t)
		if syncKnown(t, a, discoverKnown(t, a)) {
			t.Error("SyncKnown wrote with nothing to do")
		}
	})

	t.Run("newer live tokens refresh the snapshot", func(t *testing.T) {
		a, _ := syncBaseline(t)
		fresh := credsJSON("a-refreshed", freshExpiry, refreshOK)
		writeLiveCreds(t, a, fresh)
		if !syncKnown(t, a, discoverKnown(t, a)) {
			t.Fatal("SyncKnown did not write")
		}
		if !bytes.Equal(readSnapshot(t, a, "uuid-a"), fresh) {
			t.Error("snapshot was not refreshed with the newer live tokens")
		}
	})

	t.Run("older live tokens never clobber a fresher snapshot", func(t *testing.T) {
		a, _ := syncBaseline(t)
		fresher := credsJSON("a-fresher", freshExpiry, refreshOK)
		writeSnapshot(t, a, "uuid-a", fresher)
		if syncKnown(t, a, discoverKnown(t, a)) {
			t.Error("SyncKnown wrote although the live tokens are older")
		}
		if !bytes.Equal(readSnapshot(t, a, "uuid-a"), fresher) {
			t.Error("the fresher snapshot was clobbered")
		}
	})

	t.Run("corrupt snapshot is replaced", func(t *testing.T) {
		a, live := syncBaseline(t)
		writeSnapshot(t, a, "uuid-a", []byte("junk"))
		if !syncKnown(t, a, discoverKnown(t, a)) {
			t.Fatal("SyncKnown did not heal the corrupt snapshot")
		}
		if !bytes.Equal(readSnapshot(t, a, "uuid-a"), live) {
			t.Error("snapshot not replaced with live bytes")
		}
	})

	t.Run("missing snapshot is healed", func(t *testing.T) {
		a, live := syncBaseline(t)
		if err := os.Remove(filepath.Join(a.Store.Dir(), "accounts", "uuid-a", "credentials.json")); err != nil {
			t.Fatal(err)
		}
		if !syncKnown(t, a, discoverKnown(t, a)) {
			t.Fatal("SyncKnown did not heal the missing snapshot")
		}
		if !bytes.Equal(readSnapshot(t, a, "uuid-a"), live) {
			t.Error("snapshot not recreated from live bytes")
		}
	})

	t.Run("active marker heals", func(t *testing.T) {
		a, _ := syncBaseline(t)
		st := loadState(t, a)
		st.Active = ""
		saveState(t, a, st)
		if !syncKnown(t, a, discoverKnown(t, a)) {
			t.Fatal("SyncKnown did not heal the active marker")
		}
		if got := loadState(t, a).Active; got != "uuid-a" {
			t.Errorf("Active = %q, want uuid-a", got)
		}
	})

	t.Run("email follows the profile", func(t *testing.T) {
		a := newTestApp(t)
		live := credsJSON("a", staleExpiry, refreshOK)
		writeLiveCreds(t, a, live)
		writeLiveConfig(t, a, profileJSON("uuid-a", "new@x.com"))
		saveState(t, a, store.State{Active: "uuid-a", Accounts: []store.Account{{UUID: "uuid-a", Email: "old@x.com", AddedAt: testNow}}})
		writeSnapshot(t, a, "uuid-a", live)
		writeProfile(t, a, "uuid-a", profileJSON("uuid-a", "new@x.com"))

		if !syncKnown(t, a, discoverKnown(t, a)) {
			t.Fatal("SyncKnown did not update the drifted email")
		}
		if got := loadState(t, a).Accounts[0].Email; got != "new@x.com" {
			t.Errorf("email = %q, want new@x.com", got)
		}
	})

	t.Run("profile drift is captured", func(t *testing.T) {
		a, _ := syncBaseline(t)
		writeProfile(t, a, "uuid-a", []byte(`{"accountUuid":"uuid-a","emailAddress":"stale@x.com"}`))
		if !syncKnown(t, a, discoverKnown(t, a)) {
			t.Fatal("SyncKnown did not update the drifted profile")
		}
		p, err := a.Store.ReadProfile("uuid-a")
		if err != nil || !bytes.Equal(p, profileJSON("uuid-a", "a@x.com")) {
			t.Errorf("stored profile = %s, %v", p, err)
		}
	})

	t.Run("non-known discovery is a no-op", func(t *testing.T) {
		a := newTestApp(t)
		written, err := a.SyncKnown(Discovery{Status: Unknown})
		if written || err != nil {
			t.Errorf("got %v, %v; want false, nil", written, err)
		}
	})
}
