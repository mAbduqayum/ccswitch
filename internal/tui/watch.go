package tui

import (
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fsnotify/fsnotify"
)

// debounce swallows the event burst an atomic write (temp + rename) causes
// before a single rediscovery runs.
const debounce = 300 * time.Millisecond

// watcher watches the directory holding the live credentials. Watching the
// file itself would break: atomic renames replace the inode.
type watcher struct {
	fs       *fsnotify.Watcher
	relevant map[string]bool // base names that trigger rediscovery
}

// startWatchCmd creates the watcher off the UI thread. Failure is reported
// as a note, never as a fatal error — the TUI works without it.
func (m Model) startWatchCmd() tea.Cmd {
	return func() tea.Msg {
		credPath := m.app.Env.CredentialsPath()
		fs, err := fsnotify.NewWatcher()
		if err != nil {
			return watchStartedMsg{err: err}
		}
		if err := fs.Add(filepath.Dir(credPath)); err != nil {
			_ = fs.Close()
			return watchStartedMsg{err: err}
		}
		w := &watcher{
			fs: fs,
			relevant: map[string]bool{
				filepath.Base(credPath):               true,
				filepath.Base(m.app.Env.ConfigPath()): true,
			},
		}
		return watchStartedMsg{w: w}
	}
}

// waitCmd blocks until a relevant filesystem event, debounces the burst,
// and reports one credsChangedMsg. The Update handler re-arms it.
func (w *watcher) waitCmd() tea.Cmd {
	return func() tea.Msg {
		for {
			select {
			case ev, ok := <-w.fs.Events:
				if !ok {
					return nil
				}
				if !w.relevant[filepath.Base(ev.Name)] {
					continue
				}
				time.Sleep(debounce)
				for {
					select {
					case <-w.fs.Events:
					default:
						return credsChangedMsg{}
					}
				}
			case _, ok := <-w.fs.Errors:
				if !ok {
					return nil
				}
			}
		}
	}
}
