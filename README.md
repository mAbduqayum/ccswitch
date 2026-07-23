# ccswitch

[![ci](https://github.com/mAbduqayum/ccswitch/actions/workflows/ci.yml/badge.svg)](https://github.com/mAbduqayum/ccswitch/actions/workflows/ci.yml)

Switch between multiple Claude Code accounts. `ccswitch` snapshots each
account's OAuth credentials and swaps them on demand — no re-login, no network
calls, tokens never leave your machine.

```
     #  ACCOUNT          ALIAS     PLAN  TOKEN
  ────────────────────────────────────────────────
  ▶  1  you@work.com     work      max   ok
     2  you@home.com     personal  pro   ok
```

## Install

**Nix** (flakes):

```sh
nix run github:mAbduqayum/ccswitch          # try it
nix profile install github:mAbduqayum/ccswitch
```

**Go**:

```sh
go install github.com/mAbduqayum/ccswitch/cmd/ccswitch@latest
```

**Release archives** — prebuilt binaries for Linux/macOS (amd64/arm64) with
shell completions are on the
[releases page](https://github.com/mAbduqayum/ccswitch/releases).

## Quick start

1. Log into Claude Code with your first account (`claude /login`), then run
   `ccswitch` — it notices the login and offers to add it.
2. Log into a second account (`claude /logout`, `claude /login`) and run
   `ccswitch` again.
3. From now on `ccswitch switch` toggles between them.

## Usage

Run `ccswitch` with no arguments for the TUI:

| Key | Action |
| --- | --- |
| `↑`/`↓`, `j`/`k` | move |
| `enter` | switch to the selected account |
| `r` | rename (set/clear alias) |
| `d` | remove (with confirmation) |
| `q`, `ctrl+c` | quit |

The TUI watches `~/.claude` and picks up logins made in other terminals. That
relies on file events, so profile-only config changes and macOS Keychain
updates show up on the next run instead.

Everything is also scriptable:

```sh
ccswitch list                # table of accounts (alias: ls)
ccswitch list --json         # metadata only — token values never appear
ccswitch status              # active account + where state lives
ccswitch switch              # next account in rotation
ccswitch switch work         # by alias, email, list number, or uuid
ccswitch alias 2 personal    # name an account ("" clears)
ccswitch remove personal     # forget an account (-y skips the prompt)
ccswitch doctor              # health checks; exit 1 on failures
ccswitch warm                # run claude once as each account; exit 1 on failures
ccswitch update              # self-update to the latest release
ccswitch update --check      # report if an update is available, install nothing
ccswitch completions zsh     # bash | zsh | fish
```

`update` is the only command that reaches the network, and only when you run
it. It downloads the matching release archive, verifies its SHA-256 against
`checksums.txt`, and atomically replaces the running binary. When ccswitch was
installed by a package manager (Nix, Homebrew) the binary can't be overwritten
in place; `update` asks first and, if you agree, installs a self-managed copy
to `~/.local/bin/ccswitch`.

## Keeping idle accounts alive

An account you don't switch to for months can have its refresh token expire
from disuse. `ccswitch warm` exercises every account in turn — switching to
it and running `claude --model haiku --print hi` once — so Claude Code
refreshes each account's credentials and ccswitch banks the result:

```sh
ccswitch warm                       # every account, haiku, 30s each
ccswitch warm --timeout 2m --json   # slower links, machine-readable report
```

ccswitch still makes no network call itself; the `claude` binary does. An
account that fails (offline, needs a re-login) is reported without stopping
the rest, the exit status is non-zero if any failed, and the account that was
active is restored at the end.

Scheduling is left to the OS — a systemd timer or a cron entry such as
`0 4 * * 0 ccswitch warm` is enough. One caveat: warm swaps the live
credential file from account to account as it runs, so don't run it while an
interactive `claude` session is open under a different account.

## Auto-discovery

Every invocation starts with a read-only look at the live login:

- **Unknown account** → ccswitch offers to add it (y/N in a terminal, a notice
  on stderr otherwise — pipelines never block).
- **Known account** → its snapshot silently refreshes when the live tokens are
  newer (Claude Code rotates refresh tokens). An older live file never
  overwrites a fresher snapshot.

So switching A→B→A round-trips through re-logins done outside ccswitch without
ever losing a rotated refresh token.

## How it works

Claude Code keeps its login in two places: the OAuth credentials
(`~/.claude/.credentials.json` on Linux/WSL, Keychain on macOS) and an
`oauthAccount` profile inside its config JSON. A switch:

1. snapshots the live credentials into the current account's slot — **before**
   anything else, so token rotation is never lost;
2. atomically writes the target's snapshot as the live credentials (0600);
3. patches only the `oauthAccount` key in the config, leaving every other value
   untouched;
4. updates its own active marker.

ccswitch's state lives in `$XDG_DATA_HOME/ccswitch` (default
`~/.local/share/ccswitch`, mode 0700). Snapshots are the raw bytes Claude Code
wrote. Concurrent instances are serialized with a file lock.

## macOS (experimental)

On macOS, Claude Code may store credentials in the Keychain instead of a
plaintext file. ccswitch shells out to `security` for that case (a plaintext
`~/.claude/.credentials.json` wins when present). This path has seen less
real-world use — reports welcome.

## Development

Nix + direnv dev shell, `just` for tasks, tests always in temp dirs — see
[CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
