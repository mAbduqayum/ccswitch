package store

import "time"

const stateVersion = 1

// State is the persisted account registry (state.json).
type State struct {
	Version int `json:"version"`
	// Active is the UUID of the account whose credentials are live.
	Active string `json:"active,omitempty"`
	// Accounts in rotation order: `switch` with no argument moves to the
	// entry after Active, wrapping around.
	Accounts []Account `json:"accounts"`
}

// Account is one managed Claude Code account.
type Account struct {
	UUID    string    `json:"uuid"`
	Email   string    `json:"email"`
	Alias   string    `json:"alias,omitempty"`
	AddedAt time.Time `json:"addedAt"`
}

// IndexByUUID returns the position of the account with the given UUID, or -1.
func (s State) IndexByUUID(uuid string) int {
	for i, a := range s.Accounts {
		if a.UUID == uuid {
			return i
		}
	}
	return -1
}

// IndexByEmail returns the position of the account with the given email, or -1.
func (s State) IndexByEmail(email string) int {
	for i, a := range s.Accounts {
		if a.Email == email {
			return i
		}
	}
	return -1
}
