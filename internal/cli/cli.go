// Package cli implements the ccswitch command tree. All IO is injected via
// Options so every prompt and every output path is testable without a TTY.
package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mAbduqayum/ccswitch/internal/app"
)

// IO carries the process streams. IsTTY gates interactive prompts and the
// TUI: piped invocations must never block waiting for input.
type IO struct {
	In    io.Reader
	Out   io.Writer
	Err   io.Writer
	IsTTY bool
}

type Options struct {
	Version string
	IO      IO
	App     *app.App
	RunTUI  func(*app.App) error // nil = no TUI wired; root falls back to list
}

// Execute runs the CLI and returns the process exit code.
func Execute(opts Options, args []string) int {
	r := &runner{
		app:    opts.App,
		io:     opts.IO,
		in:     bufio.NewReader(opts.IO.In),
		runTUI: opts.RunTUI,
	}
	root := r.rootCmd(opts.Version)
	root.SetArgs(args)
	root.SetIn(opts.IO.In)
	root.SetOut(opts.IO.Out)
	root.SetErr(opts.IO.Err)
	if err := root.Execute(); err != nil {
		if errors.Is(err, errUnhealthy) {
			return 1 // doctor already printed its report
		}
		fmt.Fprintln(opts.IO.Err, "ccswitch:", err)
		return 1
	}
	return 0
}

type runner struct {
	app    *app.App
	io     IO
	in     *bufio.Reader
	runTUI func(*app.App) error
}

// preflight runs auto-discovery before every command except those whose
// output is shell-evaluated (completions, __complete) or that must observe
// the store without touching it (doctor, marked via the skipDiscovery
// annotation).
func (r *runner) preflight(cmd *cobra.Command, _ []string) error {
	if cmd.Annotations["skipDiscovery"] == "true" {
		return nil
	}
	switch cmd.Name() {
	case "help", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
		return nil
	}
	// When the TUI is about to run it does its own discovery with a proper
	// dialog; prompting here would also leave bufio-buffered stdin bytes
	// that bubbletea never sees.
	if !cmd.HasParent() && r.runTUI != nil && r.io.IsTTY {
		return nil
	}
	return r.discover()
}

// discover syncs a known live login silently and offers to add an unknown
// one — y/N prompt on a TTY, a stderr notice otherwise.
func (r *runner) discover() error {
	d, err := r.app.Discover()
	if errors.Is(err, app.ErrLiveCredsMalformed) {
		// Read-only commands still work without a live identity; switch
		// hard-fails on this on its own.
		fmt.Fprintf(r.io.Err, "warning: %v — run `claude /login` to repair\n", err)
		return nil
	}
	if err != nil {
		return err
	}
	switch d.Status {
	case app.Known:
		_, err := r.app.SyncKnown(d)
		return err
	case app.Unknown:
		if !r.io.IsTTY {
			fmt.Fprintf(r.io.Err, "note: the current login %s is not managed by ccswitch — run `ccswitch` in a terminal to add it\n", d.Profile.EmailAddress)
			return nil
		}
		ok, err := r.confirm(fmt.Sprintf("Current login %s is not managed by ccswitch. Add it?", d.Profile.EmailAddress))
		if err != nil || !ok {
			return err
		}
		acct, err := r.app.AddCurrent(d)
		if err != nil {
			return err
		}
		fmt.Fprintf(r.io.Err, "added %s\n", acct.Email)
	}
	return nil
}

// confirm prints a y/N prompt on stderr (stdout may be piped) and reads one
// answer line. EOF or anything but yes means no.
func (r *runner) confirm(prompt string) (bool, error) {
	fmt.Fprintf(r.io.Err, "%s [y/N] ", prompt)
	line, err := r.in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
