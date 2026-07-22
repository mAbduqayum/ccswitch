# ccswitch

[![ci](https://github.com/mAbduqayum/ccswitch/actions/workflows/ci.yml/badge.svg)](https://github.com/mAbduqayum/ccswitch/actions/workflows/ci.yml)

Switch between multiple Claude Code accounts. `ccswitch` snapshots the OAuth
credentials of each account you log into and swaps them on demand — no
re-login, no network calls, tokens never leave your machine.

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
shell completions included are on the
[releases page](https://github.com/mAbduqayum/ccswitch/releases).

## Quick start

1. Log into Claude Code with your first account (`claude /login`), then run
   `ccswitch` — it notices the login and offers to add it.
2. Log into your second account (`claude /logout`, `claude /login`), run
   `ccswitch` again, accept the prompt.
3. From now on `ccswitch switch` toggles between them.

## Usage

Running `ccswitch` with no arguments opens the TUI:

| Key | Action |
| --- | --- |
| `↑`/`↓`, `j`/`k` | move |
| `enter` | switch to the selected account |
| `r` | rename (set/clear alias) |
| `d` | remove (with confirmation) |
| `q`, `ctrl+c` | quit |

The TUI watches `~/.claude` and picks up logins made in other terminals
while it is open. That relies on file events: profile-only config changes
and macOS Keychain updates don't emit any, so those show up on the next
run instead (a login always rewrites the credentials file, so logins are
still noticed).

Everything is also scriptable:

```sh
ccswitch list                # table of accounts (alias: ls)
ccswitch list --json         # metadata only — token values never appear
ccswitch status              # active account + where state lives
ccswitch switch              # next account in rotation
ccswitch switch work         # by alias, email, list number, or uuid
ccswitch alias 2 personal    # name an account ("" clears)
ccswitch remove personal     # forget an account (+ its snapshots); -y skips the prompt
ccswitch doctor              # health checks; exit 1 on failures
ccswitch completions zsh     # bash | zsh | fish
```

Accounts are addressed by list number, email, alias, or account uuid.
`--json` output is stable metadata (email, alias, plan, token status,
expiry classification) and by construction never contains token values.

One quirk to know about in pty harnesses (CI, expect scripts): on a
pseudo-terminal that never answers terminal queries, startup pauses ~5 s —
bubbletea v1 probes the terminal background at import time. Piped
invocations without a tty skip the probe entirely.

## Auto-discovery

Every invocation starts with a read-only look at the live login:

- **Unknown account** → ccswitch offers to add it (y/N prompt in a terminal,
  a notice on stderr otherwise — pipelines never block).
- **Known account** → its snapshot silently refreshes when the live tokens
  are newer (Claude Code rotates refresh tokens), and the active marker and
  stored email/profile heal if they drifted. An older live file never
  overwrites a fresher snapshot.

So switching A→B→A round-trips through re-logins done outside ccswitch
without ever losing a rotated refresh token.

## How it works

Claude Code keeps its login in two places: the OAuth credentials
(`~/.claude/.credentials.json` on Linux/WSL, Keychain on macOS) and an
`oauthAccount` profile inside its config JSON (`$CLAUDE_CONFIG_DIR/.claude.json`,
`~/.claude/.claude.json`, or `~/.claude.json`). A switch:

1. snapshots the live credentials into the current account's slot — **before**
   anything else, so token rotation is never lost;
2. atomically writes the target's snapshot as the live credentials (0600);
3. patches only the `oauthAccount` key in the config — every other value
   passes through untouched (only top-level key order and whitespace
   normalize);
4. updates its own active marker.

ccswitch's state lives in `$XDG_DATA_HOME/ccswitch` (default
`~/.local/share/ccswitch`, mode 0700). Snapshots are stored as the raw bytes
Claude Code wrote — never re-encoded. Concurrent instances are serialized
with a file lock. There are **zero network calls**: ccswitch never talks to
the OAuth provider, only to your filesystem.

## macOS (experimental)

On macOS, Claude Code may store credentials in the Keychain instead of a
plaintext file. ccswitch shells out to `security` for that case (a plaintext
`~/.claude/.credentials.json` wins when present). This path is exercised in
tests against a fake `security`, but has seen less real-world use — reports
welcome.

## Development

Nix + direnv dev shell, `just` for tasks, tests always in temp dirs — see
[CONTRIBUTING.md](CONTRIBUTING.md). Never point experiments at your real
`~/.claude`; every path is injectable.

## License

[MIT](LICENSE)
