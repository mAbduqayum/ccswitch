package app

import (
	"time"

	"github.com/mAbduqayum/ccswitch/internal/claude"
)

// renewSoonWindow is how close to refresh-token expiry an account gets
// flagged for renewal — switching to it lets Claude Code rotate the token.
const renewSoonWindow = 7 * 24 * time.Hour

// TokenStatus classifies an account snapshot's refresh-token health for
// display, never surfacing token values: "missing", "invalid", "unknown"
// (no recorded expiry), "expired", "renew-soon", or "ok". plan is the
// subscription type when the snapshot is parseable.
func (a *App) TokenStatus(uuid string) (status, plan string) {
	raw, err := a.Store.ReadSnapshot(uuid)
	if err != nil {
		return "missing", ""
	}
	meta, err := claude.ParseCredentials(raw)
	if err != nil {
		return "invalid", ""
	}
	if access, refresh := claude.HasTokens(raw); !access || !refresh {
		return "invalid", meta.SubscriptionType
	}
	switch left := meta.RefreshExpiry().Sub(a.Now()); {
	case meta.RefreshTokenExpiresAt == 0:
		return "unknown", meta.SubscriptionType
	case left <= 0:
		return "expired", meta.SubscriptionType
	case left < renewSoonWindow:
		return "renew-soon", meta.SubscriptionType
	default:
		return "ok", meta.SubscriptionType
	}
}
