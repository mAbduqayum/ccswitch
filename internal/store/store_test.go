package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testState() State {
	return State{
		Active: "uuid-a",
		Accounts: []Account{
			{UUID: "uuid-a", Email: "a@x.com", AddedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)},
			{UUID: "uuid-b", Email: "b@x.com", Alias: "work", AddedAt: time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)},
		},
	}
}

func TestStateRoundTrip(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "ccswitch"))
	want := testState()
	if err := s.SaveState(want); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, err := s.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	want.Version = stateVersion // SaveState stamps it
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}

	info, err := os.Stat(filepath.Join(s.Dir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("state.json perm = %o, want 600", perm)
	}
	dirInfo, err := os.Stat(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("store dir perm = %o, want 700", perm)
	}
}

func TestLoadStateMissingIsEmptyV1(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "nope"))
	st, err := s.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.Version != stateVersion || st.Active != "" || len(st.Accounts) != 0 {
		t.Errorf("got %+v, want empty v1 state", st)
	}
}

func TestLoadStateErrors(t *testing.T) {
	t.Run("corrupt json", func(t *testing.T) {
		s := New(t.TempDir())
		if err := os.WriteFile(filepath.Join(s.Dir(), "state.json"), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.LoadState(); err == nil || !strings.Contains(err.Error(), "corrupt") {
			t.Errorf("error = %v, want corrupt-state error", err)
		}
	})
	t.Run("future schema version", func(t *testing.T) {
		s := New(t.TempDir())
		if err := os.WriteFile(filepath.Join(s.Dir(), "state.json"), []byte(`{"version": 2}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.LoadState(); err == nil || !strings.Contains(err.Error(), "upgrade ccswitch") {
			t.Errorf("error = %v, want upgrade hint", err)
		}
	})
	t.Run("missing version field rejected", func(t *testing.T) {
		s := New(t.TempDir())
		if err := os.WriteFile(filepath.Join(s.Dir(), "state.json"), []byte(`{"accounts": []}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.LoadState(); err == nil {
			t.Error("state without a version field must be rejected")
		}
	})
}

func TestSaveStateWritesAccountsArrayNotNull(t *testing.T) {
	s := New(t.TempDir())
	if err := s.SaveState(State{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"accounts": null`) {
		t.Errorf("state.json contains accounts:null, schema promises an array:\n%s", data)
	}
	if !strings.Contains(string(data), `"accounts": []`) {
		t.Errorf("state.json missing empty accounts array:\n%s", data)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	raw := []byte(`{"claudeAiOauth":{"accessToken":"x"}}`)
	if err := s.WriteSnapshot("uuid-a", raw); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	got, err := s.ReadSnapshot("uuid-a")
	if err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}
	if string(got) != string(raw) {
		t.Error("snapshot bytes altered")
	}
	info, err := os.Stat(filepath.Join(s.Dir(), "accounts", "uuid-a", "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("snapshot perm = %o, want 600", perm)
	}
}

func TestReadSnapshotMissing(t *testing.T) {
	s := New(t.TempDir())
	_, err := s.ReadSnapshot("ghost")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error = %v, want fs.ErrNotExist in chain", err)
	}
}

func TestProfileAbsenceIsNil(t *testing.T) {
	s := New(t.TempDir())
	raw, err := s.ReadProfile("uuid-a")
	if err != nil || raw != nil {
		t.Errorf("got %s, %v; want nil, nil", raw, err)
	}
	if err := s.WriteProfile("uuid-a", []byte(`{"accountUuid":"uuid-a"}`)); err != nil {
		t.Fatal(err)
	}
	raw, err = s.ReadProfile("uuid-a")
	if err != nil || raw == nil {
		t.Errorf("after write: got %s, %v", raw, err)
	}
}

func TestRemoveAccount(t *testing.T) {
	s := New(t.TempDir())
	if err := s.WriteSnapshot("uuid-a", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveAccount("uuid-a"); err != nil {
		t.Fatalf("RemoveAccount: %v", err)
	}
	if _, err := s.ReadSnapshot("uuid-a"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("snapshot still readable after remove: %v", err)
	}
	// Removing a non-existent account is a no-op, not an error.
	if err := s.RemoveAccount("uuid-a"); err != nil {
		t.Errorf("second RemoveAccount: %v", err)
	}
}

func TestOrphanDirs(t *testing.T) {
	s := New(t.TempDir())
	if err := s.WriteSnapshot("uuid-a", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSnapshot("uuid-orphan", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	st := State{Accounts: []Account{{UUID: "uuid-a"}}}
	orphans, err := s.OrphanDirs(st)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(orphans, []string{"uuid-orphan"}) {
		t.Errorf("orphans = %v, want [uuid-orphan]", orphans)
	}

	empty := New(filepath.Join(t.TempDir(), "nope"))
	orphans, err = empty.OrphanDirs(st)
	if err != nil || orphans != nil {
		t.Errorf("empty store: got %v, %v", orphans, err)
	}
}

func TestUUIDPathTraversalRejected(t *testing.T) {
	s := New(t.TempDir())
	for _, uuid := range []string{"", ".", "..", "../evil", "a/b", `a\b`} {
		if err := s.WriteSnapshot(uuid, []byte("{}")); err == nil {
			t.Errorf("WriteSnapshot(%q) accepted a bad uuid", uuid)
		}
		if _, err := s.ReadSnapshot(uuid); err == nil {
			t.Errorf("ReadSnapshot(%q) accepted a bad uuid", uuid)
		}
		if err := s.WriteProfile(uuid, []byte("{}")); err == nil {
			t.Errorf("WriteProfile(%q) accepted a bad uuid", uuid)
		}
		if _, err := s.ReadProfile(uuid); err == nil {
			t.Errorf("ReadProfile(%q) accepted a bad uuid", uuid)
		}
		if err := s.RemoveAccount(uuid); err == nil {
			t.Errorf("RemoveAccount(%q) accepted a bad uuid", uuid)
		}
	}
}

func TestLockExcludesSecondHolder(t *testing.T) {
	// The store dir doesn't exist yet — Lock must create it.
	s := New(filepath.Join(t.TempDir(), "fresh", "store"))
	unlock, err := s.Lock()
	if err != nil {
		t.Fatalf("first Lock: %v", err)
	}
	if _, err := s.Lock(); err == nil || !strings.Contains(err.Error(), "another ccswitch instance") {
		t.Errorf("second Lock error = %v, want contention message", err)
	}
	unlock()
	unlock2, err := s.Lock()
	if err != nil {
		t.Fatalf("Lock after unlock: %v", err)
	}
	// Double unlock must be a no-op — in particular it must not release a
	// lock acquired in between.
	unlock2()
	unlock2()
	unlock3, err := s.Lock()
	if err != nil {
		t.Fatalf("Lock after double unlock: %v", err)
	}
	unlock2() // stale unlock while unlock3 holds the lock
	if _, err := s.Lock(); err == nil {
		t.Error("stale unlock released a lock it does not own")
	}
	unlock3()
}

func TestIndexLookups(t *testing.T) {
	st := testState()
	if i := st.IndexByUUID("uuid-b"); i != 1 {
		t.Errorf("IndexByUUID = %d, want 1", i)
	}
	if i := st.IndexByUUID("ghost"); i != -1 {
		t.Errorf("IndexByUUID(ghost) = %d, want -1", i)
	}
	if i := st.IndexByEmail("a@x.com"); i != 0 {
		t.Errorf("IndexByEmail = %d, want 0", i)
	}
	if i := st.IndexByEmail("ghost@x.com"); i != -1 {
		t.Errorf("IndexByEmail(ghost) = %d, want -1", i)
	}
}
