package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mAbduqayum/ccswitch/internal/store"
)

// switchWorld is a two-account setup: A is live with tokens fresher than its
// stored snapshot (Claude Code refreshed them since the last switch), B waits
// in the store.
type switchWorld struct {
	a            *App
	acctA, acctB store.Account
	snapA, snapB []byte
	liveA        []byte
}

func newSwitchWorld(t *testing.T) *switchWorld {
	t.Helper()
	w := &switchWorld{
		a:     newTestApp(t),
		acctA: store.Account{UUID: "uuid-a", Email: "a@x.com", AddedAt: testNow},
		acctB: store.Account{UUID: "uuid-b", Email: "b@x.com", AddedAt: testNow},
		snapA: credsJSON("a-old", staleExpiry, refreshOK),
		snapB: credsJSON("b", freshExpiry, refreshOK),
		liveA: credsJSON("a-refreshed", freshExpiry, refreshOK),
	}
	saveState(t, w.a, store.State{Active: "uuid-a", Accounts: []store.Account{w.acctA, w.acctB}})
	writeSnapshot(t, w.a, "uuid-a", w.snapA)
	writeSnapshot(t, w.a, "uuid-b", w.snapB)
	writeProfile(t, w.a, "uuid-a", profileJSON("uuid-a", "a@x.com"))
	writeProfile(t, w.a, "uuid-b", profileJSON("uuid-b", "b@x.com"))
	writeLiveCreds(t, w.a, w.liveA)
	writeLiveConfig(t, w.a, profileJSON("uuid-a", "a@x.com"))
	return w
}

// TestSwitchSnapshotsLiveBeforeRestore proves acceptance criterion #3: the
// live credentials (refreshed since A's snapshot was taken) land in A's slot
// before B's snapshot goes live, so token rotation is never lost.
func TestSwitchSnapshotsLiveBeforeRestore(t *testing.T) {
	w := newSwitchWorld(t)
	res, err := w.a.Switch(w.acctB, false)
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}

	if got := readSnapshot(t, w.a, "uuid-a"); !bytes.Equal(got, w.liveA) {
		t.Error("A's snapshot does not hold the refreshed live tokens — the refresh was lost")
	}
	if got := readLiveCreds(t, w.a); !bytes.Equal(got, w.snapB) {
		t.Error("live credentials are not B's snapshot")
	}
	if got := readSnapshot(t, w.a, "uuid-b"); !bytes.Equal(got, w.snapB) {
		t.Error("B's snapshot changed during the switch")
	}
	if st := loadState(t, w.a); st.Active != "uuid-b" {
		t.Errorf("Active = %q, want uuid-b", st.Active)
	}
	if res.From.UUID != "uuid-a" || res.To.UUID != "uuid-b" {
		t.Errorf("result = from %q to %q", res.From.UUID, res.To.UUID)
	}
	if !res.ProfilePatched {
		t.Error("profile was not patched")
	}
	if len(res.Warnings) != 0 {
		t.Errorf("unexpected warnings: %+v", res.Warnings)
	}

	info, err := os.Stat(w.a.Env.CredentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("live credentials perm = %o, want 600", perm)
	}
}

// TestSwitchPreservesUnrelatedConfig covers acceptance criterion #4 at the
// app level: after a switch, only oauthAccount changed in the claude config.
func TestSwitchPreservesUnrelatedConfig(t *testing.T) {
	w := newSwitchWorld(t)
	if _, err := w.a.Switch(w.acctB, false); err != nil {
		t.Fatalf("Switch: %v", err)
	}

	data, err := os.ReadFile(w.a.Env.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatalf("config no longer valid JSON: %v", err)
	}
	if got := string(top["theme"]); got != `"dark-daltonized"` {
		t.Errorf("theme = %s, want it byte-identical", got)
	}
	if got := string(top["numStartups"]); got != "42" {
		t.Errorf("numStartups = %s, want 42", got)
	}
	var p struct {
		AccountUUID  string `json:"accountUuid"`
		EmailAddress string `json:"emailAddress"`
	}
	if err := json.Unmarshal(top["oauthAccount"], &p); err != nil {
		t.Fatal(err)
	}
	if p.AccountUUID != "uuid-b" || p.EmailAddress != "b@x.com" {
		t.Errorf("oauthAccount = %+v, want B's profile", p)
	}
}

func TestSwitchMissingSnapshotAbortsWithZeroSideEffects(t *testing.T) {
	w := newSwitchWorld(t)
	if err := os.Remove(filepath.Join(w.a.Store.Dir(), "accounts", "uuid-b", "credentials.json")); err != nil {
		t.Fatal(err)
	}
	_, err := w.a.Switch(w.acctB, false)
	if err == nil || !strings.Contains(err.Error(), "no credentials snapshot") {
		t.Fatalf("error = %v, want missing-snapshot message", err)
	}

	if got := readLiveCreds(t, w.a); !bytes.Equal(got, w.liveA) {
		t.Error("live credentials changed despite the abort")
	}
	if got := readSnapshot(t, w.a, "uuid-a"); !bytes.Equal(got, w.snapA) {
		t.Error("A's snapshot changed despite the abort")
	}
	if st := loadState(t, w.a); st.Active != "uuid-a" {
		t.Errorf("Active = %q, want untouched uuid-a", st.Active)
	}
}

// TestSwitchUnregisteredTargetAborts: the target was resolved before the
// lock; a concurrent remove must abort the switch, not restore a leftover
// snapshot and dangle the active marker.
func TestSwitchUnregisteredTargetAborts(t *testing.T) {
	w := newSwitchWorld(t)
	ghost := store.Account{UUID: "uuid-ghost", Email: "g@x.com"}
	_, err := w.a.Switch(ghost, false)
	if err == nil || !strings.Contains(err.Error(), "no longer registered") {
		t.Fatalf("error = %v, want unregistered-target abort", err)
	}
	if got := readLiveCreds(t, w.a); !bytes.Equal(got, w.liveA) {
		t.Error("live credentials changed despite the abort")
	}
	if st := loadState(t, w.a); st.Active != "uuid-a" {
		t.Errorf("Active = %q, want untouched uuid-a", st.Active)
	}
}

func TestSwitchUnknownLiveLogin(t *testing.T) {
	t.Run("aborts without force", func(t *testing.T) {
		w := newSwitchWorld(t)
		writeLiveConfig(t, w.a, profileJSON("uuid-stranger", "s@x.com"))
		_, err := w.a.Switch(w.acctB, false)
		if !errors.Is(err, ErrUnsavedLogin) {
			t.Fatalf("error = %v, want ErrUnsavedLogin", err)
		}
		if got := readLiveCreds(t, w.a); !bytes.Equal(got, w.liveA) {
			t.Error("live credentials changed despite the abort")
		}
	})
	t.Run("force discards with a warning", func(t *testing.T) {
		w := newSwitchWorld(t)
		writeLiveConfig(t, w.a, profileJSON("uuid-stranger", "s@x.com"))
		res, err := w.a.Switch(w.acctB, true)
		if err != nil {
			t.Fatalf("Switch --force: %v", err)
		}
		if got := readLiveCreds(t, w.a); !bytes.Equal(got, w.snapB) {
			t.Error("live credentials are not B's snapshot")
		}
		if res.From.UUID != "" {
			t.Errorf("From = %q, want zero value for an unidentified login", res.From.UUID)
		}
		if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "discarded") {
			t.Errorf("warnings = %v, want a discard warning", res.Warnings)
		}
	})
}

func TestSwitchToLiveAccountKeepsFreshestTokens(t *testing.T) {
	w := newSwitchWorld(t)
	res, err := w.a.Switch(w.acctA, false)
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}
	if got := readLiveCreds(t, w.a); !bytes.Equal(got, w.liveA) {
		t.Error("switching to the live account must keep the live tokens, not restore the stale snapshot")
	}
	if st := loadState(t, w.a); st.Active != "uuid-a" {
		t.Errorf("Active = %q, want uuid-a", st.Active)
	}
	if res.From.UUID != "uuid-a" {
		t.Errorf("From = %q, want uuid-a", res.From.UUID)
	}
}

func TestSwitchNotLoggedIn(t *testing.T) {
	w := newSwitchWorld(t)
	if err := os.Remove(w.a.Env.CredentialsPath()); err != nil {
		t.Fatal(err)
	}
	res, err := w.a.Switch(w.acctB, false)
	if err != nil {
		t.Fatalf("Switch with nothing live: %v", err)
	}
	if got := readLiveCreds(t, w.a); !bytes.Equal(got, w.snapB) {
		t.Error("live credentials are not B's snapshot")
	}
	if res.From.UUID != "" {
		t.Errorf("From = %q, want zero value", res.From.UUID)
	}
	// A's snapshot must be untouched — there was nothing live to preserve.
	if got := readSnapshot(t, w.a, "uuid-a"); !bytes.Equal(got, w.snapA) {
		t.Error("A's snapshot changed")
	}
}

func TestSwitchIdentifiesLiveByActiveMarker(t *testing.T) {
	w := newSwitchWorld(t)
	// No usable profile: identifyLive must fall back to the active marker.
	if err := os.Remove(w.a.Env.ConfigPath()); err != nil {
		t.Fatal(err)
	}
	res, err := w.a.Switch(w.acctB, false)
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}
	if got := readSnapshot(t, w.a, "uuid-a"); !bytes.Equal(got, w.liveA) {
		t.Error("live tokens were not snapshotted into the active account's slot")
	}
	if res.From.UUID != "uuid-a" {
		t.Errorf("From = %q, want uuid-a via the active marker", res.From.UUID)
	}
	// With no config file the profile patch fails — that is only a warning.
	if res.ProfilePatched {
		t.Error("ProfilePatched = true with no config file")
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "claude config") {
		t.Errorf("warnings = %v, want a config-patch warning", res.Warnings)
	}
}

// TestSwitchMarkerFallbackKeepsFresherSnapshot: when identifyLive falls back
// to the active marker, the live file may belong to someone else entirely —
// live tokens older than the marked account's snapshot must not overwrite it.
func TestSwitchMarkerFallbackKeepsFresherSnapshot(t *testing.T) {
	w := newSwitchWorld(t)
	if err := os.Remove(w.a.Env.ConfigPath()); err != nil {
		t.Fatal(err)
	}
	writeLiveCreds(t, w.a, credsJSON("foreign", olderExpiry, refreshOK))

	res, err := w.a.Switch(w.acctB, false)
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}
	if got := readSnapshot(t, w.a, "uuid-a"); !bytes.Equal(got, w.snapA) {
		t.Error("older live tokens clobbered A's fresher snapshot")
	}
	if got := readLiveCreds(t, w.a); !bytes.Equal(got, w.snapB) {
		t.Error("live credentials are not B's snapshot")
	}
	if res.From.UUID != "uuid-a" {
		t.Errorf("From = %q, want uuid-a via the active marker", res.From.UUID)
	}
}

func TestSwitchMalformedLiveCredsAborts(t *testing.T) {
	w := newSwitchWorld(t)
	writeLiveCreds(t, w.a, []byte("junk"))
	_, err := w.a.Switch(w.acctB, false)
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("error = %v, want a malformed-credentials abort", err)
	}
	// Zero side effects: nothing restored, nothing snapshotted, marker intact.
	if got := readLiveCreds(t, w.a); !bytes.Equal(got, []byte("junk")) {
		t.Error("live credentials changed despite the abort")
	}
	if got := readSnapshot(t, w.a, "uuid-a"); !bytes.Equal(got, w.snapA) {
		t.Error("A's snapshot changed despite the abort")
	}
	if st := loadState(t, w.a); st.Active != "uuid-a" {
		t.Errorf("Active = %q, want untouched uuid-a", st.Active)
	}
}

func TestSwitchWithoutStoredProfileSkipsPatch(t *testing.T) {
	w := newSwitchWorld(t)
	if err := os.Remove(filepath.Join(w.a.Store.Dir(), "accounts", "uuid-b", "profile.json")); err != nil {
		t.Fatal(err)
	}
	res, err := w.a.Switch(w.acctB, false)
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}
	if res.ProfilePatched {
		t.Error("ProfilePatched = true without a stored profile")
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings = %v, want none — a missing profile is normal", res.Warnings)
	}
	// The config still shows A; discovery will reconcile after Claude Code
	// refetches the profile.
	data, err := os.ReadFile(w.a.Env.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("a@x.com")) {
		t.Error("config was rewritten although no profile was available")
	}
}

func TestSwitchCredWriteErrorPreservesSnapshot(t *testing.T) {
	w := newSwitchWorld(t)
	w.a.Creds = &fakeCreds{raw: w.liveA, writeErr: errors.New("disk full")}
	_, err := w.a.Switch(w.acctB, false)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("error = %v, want the write error", err)
	}
	// Step 1 completed before the failure: the refreshed tokens are safe in
	// A's slot, and the active marker still points at the truth.
	if got := readSnapshot(t, w.a, "uuid-a"); !bytes.Equal(got, w.liveA) {
		t.Error("A's snapshot does not hold the live tokens after the failed switch")
	}
	if st := loadState(t, w.a); st.Active != "uuid-a" {
		t.Errorf("Active = %q, want uuid-a — the switch did not happen", st.Active)
	}
}
