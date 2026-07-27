package app

import (
	"time"

	"github.com/mAbduqayum/ccswitch/internal/claude"
)

// renewSoonWindow is how close to refresh-token expiry an account gets
// flagged for renewal — switching to it lets Claude Code rotate the token.
const renewSoonWindow = 7 * 24 * time.Hour

// tokenHealth grades an account snapshot without surfacing token values. The
// string form is what `list --json` and the TUI's TOKEN column publish, so
// these values are part of ccswitch's output contract.
type tokenHealth string

const (
	tokenMissing   tokenHealth = "missing"
	tokenInvalid   tokenHealth = "invalid"
	tokenUnknown   tokenHealth = "unknown" // no recorded refresh-token expiry
	tokenExpired   tokenHealth = "expired"
	tokenRenewSoon tokenHealth = "renew-soon"
	tokenOK        tokenHealth = "ok"
)

// refreshHealth grades how much life the snapshot's refresh token has left.
// Doctor shares it so the two reports can never disagree on what "renew-soon"
// means.
func refreshHealth(meta claude.CredentialMeta, now time.Time) tokenHealth {
	switch left := meta.RefreshExpiry().Sub(now); {
	case meta.RefreshTokenExpiresAt == 0:
		return tokenUnknown
	case left <= 0:
		return tokenExpired
	case left < renewSoonWindow:
		return tokenRenewSoon
	default:
		return tokenOK
	}
}

// TokenStatus classifies an account snapshot's refresh-token health for
// display. plan is the subscription type when the snapshot is parseable.
func (a *App) TokenStatus(uuid string) (status, plan string) {
	raw, err := a.Store.ReadSnapshot(uuid)
	if err != nil {
		return string(tokenMissing), ""
	}
	meta, err := claude.ParseCredentials(raw)
	if err != nil {
		return string(tokenInvalid), ""
	}
	if access, refresh := claude.HasTokens(raw); !access || !refresh {
		return string(tokenInvalid), meta.SubscriptionType
	}
	return string(refreshHealth(meta, a.Now())), meta.SubscriptionType
}
