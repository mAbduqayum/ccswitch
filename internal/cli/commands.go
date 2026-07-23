package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mAbduqayum/ccswitch/internal/app"
	"github.com/mAbduqayum/ccswitch/internal/store"
)

// errUnhealthy makes doctor exit non-zero after its report was already
// printed; Execute recognizes it and stays silent.
var errUnhealthy = errors.New("doctor found failures")

func (r *runner) rootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:   "ccswitch",
		Short: "Switch between Claude Code accounts",
		Long: "ccswitch swaps Claude Code's on-disk login between accounts.\n" +
			"It snapshots the current OAuth credentials before every switch, makes\n" +
			"no network calls, and never displays token values.",
		Version:           version,
		Args:              cobra.NoArgs,
		SilenceUsage:      true,
		SilenceErrors:     true,
		PersistentPreRunE: r.preflight,
		RunE: func(*cobra.Command, []string) error {
			if r.runTUI != nil && r.io.IsTTY {
				return r.runTUI(r.app)
			}
			return r.list(false)
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetVersionTemplate("ccswitch {{.Version}}\n")
	root.AddCommand(
		r.listCmd(),
		r.statusCmd(),
		r.switchCmd(),
		r.removeCmd(),
		r.aliasCmd(),
		r.doctorCmd(),
		r.updateCmd(),
		r.completionsCmd(),
	)
	return root
}

func (r *runner) listCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List managed accounts",
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return r.list(asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output (metadata only, never tokens)")
	return cmd
}

func (r *runner) statusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the active account and where state lives",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return r.status(asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output (metadata only, never tokens)")
	return cmd
}

func (r *runner) switchCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:               "switch [account]",
		Short:             "Switch to the next account in rotation, or to the given one",
		Long:              "Accounts can be addressed by list number, email, alias, or uuid.\nWithout an argument the next account in rotation becomes active.",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: r.completeAccounts,
		RunE: func(_ *cobra.Command, args []string) error {
			return r.switchTo(args, force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "discard the live credentials of an unregistered login")
	return cmd
}

func (r *runner) switchTo(args []string, force bool) error {
	st, err := r.app.Store.LoadState()
	if err != nil {
		return err
	}
	var target store.Account
	if len(args) == 1 {
		target, err = app.ResolveAccount(st, args[0])
	} else {
		target, err = app.RotateTarget(st)
	}
	if err != nil {
		return err
	}
	res, err := r.app.Switch(target, force)
	if err != nil {
		return err
	}
	for _, w := range res.Warnings {
		fmt.Fprintln(r.io.Err, "warning:", w)
	}
	label := target.Email
	if target.Alias != "" {
		label = fmt.Sprintf("%s (%s)", target.Email, target.Alias)
	}
	if res.From.UUID != "" && res.From.UUID != target.UUID {
		fmt.Fprintf(r.io.Out, "switched %s → %s\n", res.From.Email, label)
	} else {
		fmt.Fprintf(r.io.Out, "switched to %s\n", label)
	}
	return nil
}

func (r *runner) removeCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:               "remove <account>",
		Aliases:           []string{"rm"},
		Short:             "Forget an account and delete its credential snapshots",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: r.completeAccounts,
		RunE: func(_ *cobra.Command, args []string) error {
			return r.remove(args[0], yes)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}

func (r *runner) remove(arg string, yes bool) error {
	st, err := r.app.Store.LoadState()
	if err != nil {
		return err
	}
	acct, err := app.ResolveAccount(st, arg)
	if err != nil {
		return err
	}
	if !yes {
		if !r.io.IsTTY {
			return fmt.Errorf("refusing to remove %s without confirmation — pass --yes", acct.Email)
		}
		ok, err := r.confirm(fmt.Sprintf("Remove %s and delete its credential snapshots?", acct.Email))
		if err != nil {
			return err
		}
		if !ok {
			// Non-zero so wrappers checking $? never mistake a declined
			// removal for a completed one.
			return fmt.Errorf("aborted — %s was not removed", acct.Email)
		}
	}
	if err := r.app.Remove(acct.UUID); err != nil {
		return err
	}
	fmt.Fprintf(r.io.Out, "removed %s\n", acct.Email)
	return nil
}

func (r *runner) aliasCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "alias <account> <name>",
		Short:             "Name an account for switching; an empty name clears the alias",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: r.completeAccounts,
		RunE: func(_ *cobra.Command, args []string) error {
			st, err := r.app.Store.LoadState()
			if err != nil {
				return err
			}
			acct, err := app.ResolveAccount(st, args[0])
			if err != nil {
				return err
			}
			if err := r.app.SetAlias(acct.UUID, args[1]); err != nil {
				return err
			}
			if args[1] == "" {
				fmt.Fprintf(r.io.Out, "cleared alias of %s\n", acct.Email)
			} else {
				fmt.Fprintf(r.io.Out, "%s is now %q\n", acct.Email, args[1])
			}
			return nil
		},
	}
}

func (r *runner) doctorCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:         "doctor",
		Short:       "Check the health of the store and the live credentials",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"skipDiscovery": "true"},
		RunE: func(*cobra.Command, []string) error {
			checks := r.app.Doctor()
			if asJSON {
				if err := writeJSON(r.io.Out, checks); err != nil {
					return err
				}
			} else {
				w := tabwriter.NewWriter(r.io.Out, 2, 0, 2, ' ', 0)
				for _, c := range checks {
					fmt.Fprintf(w, "%s\t%s\t%s\n", strings.ToUpper(c.Status.String()), c.Name, c.Detail)
				}
				if err := w.Flush(); err != nil {
					return err
				}
			}
			if !app.Healthy(checks) {
				return errUnhealthy
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

func (r *runner) updateCmd() *cobra.Command {
	var checkOnly, yes bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update ccswitch to the latest release",
		Long: "Downloads the latest release from GitHub, verifies its checksum, and\n" +
			"replaces the running binary. This is the only ccswitch command that makes\n" +
			"a network call, and it never reads or transmits your credentials.",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"skipDiscovery": "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return r.runUpdate(cmd.Context(), checkOnly, yes)
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "report whether an update is available, without installing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "confirm installing a self-managed copy when the current install is package-managed")
	return cmd
}

func (r *runner) runUpdate(ctx context.Context, checkOnly, yes bool) error {
	if r.updater == nil {
		return errors.New("self-update is not wired into this build")
	}
	rel, vc, err := r.updater.Check(ctx, r.version)
	if err != nil {
		return err
	}
	if checkOnly {
		switch {
		case vc.Unknown:
			fmt.Fprintf(r.io.Out, "current version %s could not be compared; latest is %s\n", vc.Current, vc.Latest)
		case vc.Newer:
			fmt.Fprintf(r.io.Out, "update available: %s → %s\n", vc.Current, vc.Latest)
		default:
			fmt.Fprintf(r.io.Out, "ccswitch is up to date (%s)\n", vc.Current)
		}
		return nil
	}
	if !vc.Newer {
		fmt.Fprintf(r.io.Out, "ccswitch is already up to date (%s)\n", vc.Current)
		return nil
	}

	target, err := r.updater.Target()
	if err != nil {
		return err
	}
	if target.Managed {
		fmt.Fprintf(r.io.Err, "warning: this ccswitch is package-managed — %s.\n", target.Reason)
		fmt.Fprintf(r.io.Err, "An in-place update isn't possible; ccswitch can install a self-managed copy to %s that takes PATH precedence.\n", target.Dest)
		if !yes {
			if !r.io.IsTTY {
				return errors.New("refusing to install a self-managed copy without confirmation — re-run with --yes, or update with your package manager")
			}
			ok, err := r.confirm("Install the self-managed copy and let ccswitch manage updates from now on?")
			if err != nil {
				return err
			}
			if !ok {
				// Non-zero so wrappers checking $? never mistake a decline for
				// a completed update.
				return errors.New("aborted — update with your package manager instead")
			}
		}
	}

	if err := r.updater.Apply(ctx, rel, target.Dest); err != nil {
		return err
	}
	fmt.Fprintf(r.io.Out, "updated ccswitch %s → %s\n", vc.Current, vc.Latest)
	if target.Managed {
		fmt.Fprintf(r.io.Out, "installed to %s — it now shadows your package-managed copy, and ccswitch manages updates from here on.\n", target.Dest)
		if !r.updater.Shadows(target) {
			fmt.Fprintf(r.io.Err, "note: %s is not ahead of your package-managed copy on PATH — put it first so the new version is used.\n", filepath.Dir(target.Dest))
		}
	}
	return nil
}

func (r *runner) completionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "completions bash|zsh|fish",
		Short:       "Generate shell completions",
		Args:        cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		ValidArgs:   []string{"bash", "zsh", "fish"},
		Annotations: map[string]string{"skipDiscovery": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			root := cmd.Root()
			switch args[0] {
			case "bash":
				return root.GenBashCompletionV2(r.io.Out, true)
			case "zsh":
				return root.GenZshCompletion(r.io.Out)
			default:
				return root.GenFishCompletion(r.io.Out, true)
			}
		},
	}
}

// completeAccounts offers emails and aliases. It must stay quiet on errors —
// its output is evaluated by the shell.
func (r *runner) completeAccounts(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	st, err := r.app.Store.LoadState()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, acc := range st.Accounts {
		if acc.Email != "" {
			out = append(out, acc.Email)
		}
		if acc.Alias != "" {
			out = append(out, acc.Alias)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
