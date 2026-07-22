// Package tui is the interactive account switcher: a table of accounts with
// confirm dialogs for adding and removing, inline renaming, and filesystem
// watching so external logins appear without restarting.
package tui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/mAbduqayum/ccswitch/internal/app"
	"github.com/mAbduqayum/ccswitch/internal/store"
)

// Run starts the TUI and blocks until exit.
//
// Terminal background detection (for AdaptiveColor) already happened long
// before this: bubbletea v1's package init queries it with an OSC 11 probe
// at import time. On a pty that never answers, that probe is a one-time ~5s
// stall at process start — for every subcommand, not just the TUI. Removed
// upstream in bubbletea v2.
func Run(a *app.App) error {
	final, err := tea.NewProgram(New(a), tea.WithAltScreen()).Run()
	// bubbletea does not wait for in-flight command goroutines; close the
	// watcher so the one blocked in waitCmd unblocks and the fd is released.
	if m, ok := final.(Model); ok && m.watch != nil {
		m.watch.close()
	}
	return err
}

type mode int

const (
	modeList mode = iota
	modeConfirmAdd
	modeConfirmRemove
	modeRename
)

// accountRow is one table row's backing data — display metadata only.
type accountRow struct {
	account store.Account
	active  bool
	plan    string
	token   string
}

type (
	// loadedMsg carries freshly built rows from the store.
	loadedMsg struct {
		rows []accountRow
		err  error
	}
	// discoveredMsg is the result of a discovery pass (sync already done).
	discoveredMsg struct {
		d   app.Discovery
		err error
	}
	switchedMsg struct {
		res app.SwitchResult
		err error
	}
	// actionDoneMsg reports add/remove/rename completion for the status bar.
	actionDoneMsg struct {
		status string
		err    error
	}
	// credsChangedMsg means ~/.claude changed on disk; rediscover.
	credsChangedMsg struct{}
	// watchStartedMsg delivers the running watcher created off-thread.
	watchStartedMsg struct {
		w   *watcher
		err error
	}
)

type Model struct {
	app  *app.App
	mode mode

	table table.Model
	rows  []accountRow
	input textinput.Model

	pendingAdd   app.Discovery
	queuedAdd    *app.Discovery // unknown login found while a dialog was open
	removeTarget store.Account
	renameTarget store.Account

	status    string // last action result or note, shown in the status bar
	watchNote string // non-fatal watcher problem, shown dimmed
	watch     *watcher
	width     int
}

func New(a *app.App) Model {
	t := table.New(
		table.WithColumns(columns(0)),
		table.WithFocused(true),
		table.WithHeight(1),
	)
	t.SetStyles(tableStyles())
	in := textinput.New()
	in.Placeholder = "alias"
	in.CharLimit = 40
	return Model{app: a, table: t, input: in}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.discoverCmd(), m.startWatchCmd())
}

// Commands — every store/credential touch happens off the UI thread.

func (m Model) loadCmd() tea.Cmd {
	return func() tea.Msg {
		st, err := m.app.Store.LoadState()
		if err != nil {
			return loadedMsg{err: err}
		}
		rows := make([]accountRow, 0, len(st.Accounts))
		for _, acc := range st.Accounts {
			token, plan := m.app.TokenStatus(acc.UUID)
			rows = append(rows, accountRow{
				account: acc,
				active:  acc.UUID == st.Active,
				plan:    plan,
				token:   token,
			})
		}
		return loadedMsg{rows: rows}
	}
}

func (m Model) discoverCmd() tea.Cmd {
	return func() tea.Msg {
		d, err := m.app.Discover()
		if err != nil {
			return discoveredMsg{err: err}
		}
		if d.Status == app.Known {
			if _, err := m.app.SyncKnown(d); err != nil {
				return discoveredMsg{err: err}
			}
		}
		return discoveredMsg{d: d}
	}
}

func (m Model) addCmd(d app.Discovery) tea.Cmd {
	return func() tea.Msg {
		acct, err := m.app.AddCurrent(d)
		if err != nil {
			return actionDoneMsg{err: err}
		}
		return actionDoneMsg{status: "added " + acct.Email}
	}
}

func (m Model) switchCmd(target store.Account) tea.Cmd {
	return func() tea.Msg {
		res, err := m.app.Switch(target, false)
		return switchedMsg{res: res, err: err}
	}
}

func (m Model) removeCmd(target store.Account) tea.Cmd {
	return func() tea.Msg {
		if err := m.app.Remove(target.UUID); err != nil {
			return actionDoneMsg{err: err}
		}
		return actionDoneMsg{status: "removed " + target.Email}
	}
}

func (m Model) renameCmd(target store.Account, alias string) tea.Cmd {
	return func() tea.Msg {
		if err := m.app.SetAlias(target.UUID, alias); err != nil {
			return actionDoneMsg{err: err}
		}
		if alias == "" {
			return actionDoneMsg{status: "cleared alias of " + target.Email}
		}
		return actionDoneMsg{status: fmt.Sprintf("%s is now %q", target.Email, alias)}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.table.SetColumns(columns(msg.Width))
		return m, nil

	case loadedMsg:
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
			return m, nil
		}
		m.setRows(msg.rows)
		return m, nil

	case discoveredMsg:
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
			return m, m.loadCmd()
		}
		switch {
		case msg.d.Status != app.Unknown:
			m.queuedAdd = nil // a fresher discovery supersedes anything queued
		case m.mode == modeList:
			m.mode = modeConfirmAdd
			m.pendingAdd = msg.d
		case m.mode == modeConfirmAdd:
			m.pendingAdd = msg.d // refresh the open dialog with the latest login
		default:
			// Don't stomp an open rename/remove dialog; ask when it closes.
			d := msg.d
			m.queuedAdd = &d
		}
		return m, m.loadCmd()

	case switchedMsg:
		if msg.err != nil {
			if errors.Is(msg.err, app.ErrUnsavedLogin) {
				m.status = "current login is unsaved — accept the add prompt first, or use `ccswitch switch --force`"
			} else {
				m.status = "error: " + msg.err.Error()
			}
			return m, m.loadCmd()
		}
		notes := append([]string{"switched to " + msg.res.To.Email}, msg.res.Warnings...)
		m.status = strings.Join(notes, " · ")
		return m, m.loadCmd()

	case actionDoneMsg:
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
		} else {
			m.status = msg.status
		}
		return m, m.loadCmd()

	case credsChangedMsg:
		cmds := []tea.Cmd{m.discoverCmd()}
		if m.watch != nil {
			cmds = append(cmds, m.watch.waitCmd())
		}
		return m, tea.Batch(cmds...)

	case watchStartedMsg:
		if msg.err != nil {
			m.watchNote = "watch off: " + msg.err.Error()
			return m, nil
		}
		m.watch = msg.w
		return m, msg.w.waitCmd()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeConfirmAdd:
		switch {
		case key.Matches(msg, keys.Confirm):
			m.leaveDialog()
			return m, m.addCmd(m.pendingAdd)
		case key.Matches(msg, keys.Cancel, keys.Quit):
			m.leaveDialog()
			m.status = "login not added — ccswitch will ask again"
			return m, nil
		}
		return m, nil

	case modeConfirmRemove:
		switch {
		case key.Matches(msg, keys.Confirm):
			m.leaveDialog()
			return m, m.removeCmd(m.removeTarget)
		case key.Matches(msg, keys.Cancel, keys.Quit):
			m.leaveDialog()
			m.status = "not removed"
			return m, nil
		}
		return m, nil

	case modeRename:
		switch {
		case key.Matches(msg, keys.Accept):
			m.leaveDialog()
			return m, m.renameCmd(m.renameTarget, strings.TrimSpace(m.input.Value()))
		case key.Matches(msg, keys.CancelInput):
			m.leaveDialog()
			m.status = "rename cancelled"
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	default: // modeList
		switch {
		case key.Matches(msg, keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, keys.Switch):
			if row, ok := m.selected(); ok {
				return m, m.switchCmd(row.account)
			}
		case key.Matches(msg, keys.Remove):
			if row, ok := m.selected(); ok {
				m.mode = modeConfirmRemove
				m.removeTarget = row.account
			}
		case key.Matches(msg, keys.Rename):
			if row, ok := m.selected(); ok {
				m.mode = modeRename
				m.renameTarget = row.account
				m.input.SetValue(row.account.Alias)
				m.input.CursorEnd()
				return m, m.input.Focus()
			}
		default:
			var cmd tea.Cmd
			m.table, cmd = m.table.Update(msg)
			return m, cmd
		}
		return m, nil
	}
}

// leaveDialog returns to the list — or straight into the confirm-add dialog
// when an unknown login was discovered while another dialog was open.
func (m *Model) leaveDialog() {
	if m.queuedAdd != nil {
		m.mode = modeConfirmAdd
		m.pendingAdd = *m.queuedAdd
		m.queuedAdd = nil
		return
	}
	m.mode = modeList
}

func (m *Model) setRows(rows []accountRow) {
	m.rows = rows
	tableRows := make([]table.Row, 0, len(rows))
	for i, r := range rows {
		marker := ""
		if r.active {
			marker = "▶"
		}
		tableRows = append(tableRows, table.Row{
			marker, fmt.Sprintf("%d", i+1), r.account.Email, r.account.Alias, r.plan, r.token,
		})
	}
	m.table.SetRows(tableRows)
	// SetHeight subtracts the header (title line + border) internally;
	// SetRows already clamped the cursor.
	m.table.SetHeight(max(len(rows), 1) + tableHeaderHeight)
}

func (m Model) selected() (accountRow, bool) {
	idx := m.table.Cursor()
	if idx < 0 || idx >= len(m.rows) {
		return accountRow{}, false
	}
	return m.rows[idx], true
}

func (m Model) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("ccswitch"))
	b.WriteString("\n\n")
	if len(m.rows) == 0 {
		b.WriteString(dimStyle.Render("no accounts yet — log in with `claude /login` and they will appear here"))
		b.WriteString("\n")
	} else {
		b.WriteString(m.table.View())
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(m.footer())
	return b.String()
}

func (m Model) footer() string {
	switch m.mode {
	case modeConfirmAdd:
		return dialogStyle.Render(fmt.Sprintf("Add the current login %s as a managed account? [y/N]", m.pendingAdd.Profile.EmailAddress))
	case modeConfirmRemove:
		return dialogStyle.Render(fmt.Sprintf("Remove %s and delete its credential snapshots? [y/N]", m.removeTarget.Email))
	case modeRename:
		return fmt.Sprintf("%s %s\n%s", dialogStyle.Render("alias for "+m.renameTarget.Email+":"), m.input.View(), dimStyle.Render("enter accept · esc cancel"))
	default:
		var lines []string
		if m.status != "" {
			lines = append(lines, statusStyle.Render(m.status))
		}
		if m.watchNote != "" {
			lines = append(lines, dimStyle.Render(m.watchNote))
		}
		lines = append(lines, dimStyle.Render("↑/↓ move · enter switch · r rename · d remove · q quit"))
		return strings.Join(lines, "\n")
	}
}
