package cli

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/mAbduqayum/ccswitch/internal/claude"
	"github.com/mAbduqayum/ccswitch/internal/store"
)

// accountView is the exported shape of one account. Metadata only — token
// values never enter this struct.
type accountView struct {
	Number      int       `json:"number"`
	UUID        string    `json:"uuid"`
	Email       string    `json:"email"`
	Alias       string    `json:"alias,omitempty"`
	Active      bool      `json:"active"`
	Plan        string    `json:"plan,omitempty"`
	TokenStatus string    `json:"tokenStatus"`
	AddedAt     time.Time `json:"addedAt,omitzero"`
}

type statusView struct {
	Active      *accountView `json:"active,omitempty"`
	Accounts    int          `json:"accounts"`
	Credentials string       `json:"credentials"`
	Config      string       `json:"config"`
	Store       string       `json:"store"`
}

func (r *runner) accountViews(st store.State) []accountView {
	views := make([]accountView, 0, len(st.Accounts))
	for i, acc := range st.Accounts {
		status, plan := r.tokenStatus(acc.UUID)
		views = append(views, accountView{
			Number:      i + 1,
			UUID:        acc.UUID,
			Email:       acc.Email,
			Alias:       acc.Alias,
			Active:      acc.UUID == st.Active,
			Plan:        plan,
			TokenStatus: status,
			AddedAt:     acc.AddedAt,
		})
	}
	return views
}

// tokenStatus classifies a snapshot's refresh-token health for display,
// without ever surfacing the tokens themselves.
func (r *runner) tokenStatus(uuid string) (status, plan string) {
	raw, err := r.app.Store.ReadSnapshot(uuid)
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
	switch left := meta.RefreshExpiry().Sub(r.app.Now()); {
	case meta.RefreshTokenExpiresAt == 0:
		return "unknown", meta.SubscriptionType
	case left <= 0:
		return "expired", meta.SubscriptionType
	case left < 7*24*time.Hour:
		return "renew soon", meta.SubscriptionType
	default:
		return "ok", meta.SubscriptionType
	}
}

func (r *runner) list(asJSON bool) error {
	st, err := r.app.Store.LoadState()
	if err != nil {
		return err
	}
	views := r.accountViews(st)
	if asJSON {
		return writeJSON(r.io.Out, views)
	}
	if len(views) == 0 {
		fmt.Fprintln(r.io.Out, "no accounts registered — log in with `claude /login` and run `ccswitch` to add the account")
		return nil
	}
	w := tabwriter.NewWriter(r.io.Out, 2, 0, 2, ' ', 0)
	fmt.Fprintln(w, "\t#\tACCOUNT\tALIAS\tPLAN\tTOKEN")
	for _, v := range views {
		marker := ""
		if v.Active {
			marker = "▶"
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\n", marker, v.Number, v.Email, v.Alias, v.Plan, v.TokenStatus)
	}
	return w.Flush()
}

func (r *runner) status(asJSON bool) error {
	st, err := r.app.Store.LoadState()
	if err != nil {
		return err
	}
	view := statusView{
		Accounts:    len(st.Accounts),
		Credentials: r.app.Creds.Location(),
		Config:      r.app.Env.ConfigPath(),
		Store:       r.app.Store.Dir(),
	}
	for _, v := range r.accountViews(st) {
		if v.Active {
			view.Active = &v
			break
		}
	}
	if asJSON {
		return writeJSON(r.io.Out, view)
	}
	w := tabwriter.NewWriter(r.io.Out, 2, 0, 2, ' ', 0)
	if v := view.Active; v != nil {
		label := v.Email
		if v.Alias != "" {
			label = fmt.Sprintf("%s (%s)", v.Email, v.Alias)
		}
		detail := "token " + v.TokenStatus
		if v.Plan != "" {
			detail = v.Plan + ", " + detail
		}
		fmt.Fprintf(w, "account:\t%s — %s\n", label, detail)
	} else {
		fmt.Fprintf(w, "account:\tnone active — run `ccswitch switch`\n")
	}
	fmt.Fprintf(w, "accounts:\t%d managed\n", view.Accounts)
	fmt.Fprintf(w, "credentials:\t%s\n", view.Credentials)
	fmt.Fprintf(w, "config:\t%s\n", view.Config)
	fmt.Fprintf(w, "store:\t%s\n", view.Store)
	return w.Flush()
}
