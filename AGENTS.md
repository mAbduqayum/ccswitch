# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

Task runner is `just` (recipes in `justfile`):

- `just run [args]` — run ccswitch (no args = TUI)
- `just build` — produces `./ccswitch`
- `just test` — `go test -race ./...`
- `just fmt` — `gofumpt -w .`
- `just lint` — `golangci-lint run`
- `just tidy` — `go mod tidy`
- `just check` — lint + test

Single test: `go test ./internal/app -run TestName`.

Dev shell is provided by `flake.nix` (Go toolchain, gopls, golangci-lint, gofumpt, delve, gh, goreleaser) and auto-loads via direnv. `lefthook.yml` runs `gofumpt`, `go vet`, and `golangci-lint --new-from-rev=HEAD~1` on pre-commit, plus a conventional-commits regex on commit-msg (`new|feat|fix|docs|style|ref|perf|test|chore|build|ci|revert`).

## Architecture

ccswitch swaps Claude Code's on-disk OAuth state between accounts. It makes
**zero network calls** and must **never print, log, or marshal token values**.

- `internal/atomicio` — atomic file writes (temp + rename, 0600 umask-safe), 0700 dirs.
- `internal/claude` — where Claude Code keeps its state: `Env` path resolution
  (`$CLAUDE_CONFIG_DIR` → `~/.claude/.claude.json` → `~/.claude.json`), the
  `CredentialStore` platform interface (Linux file / darwin Keychain via
  injectable exec runner), token-free credential parsing, and the surgical
  `oauthAccount` config patch that must preserve every other key.
- `internal/store` — ccswitch's own state under `$XDG_DATA_HOME/ccswitch`:
  `state.json` (schema v1; account list order = rotation order), raw
  per-account credential/profile snapshots, flock around mutations.
- `internal/app` — orchestration shared by CLI and TUI: discovery (auto-add
  new logins, silently refresh known snapshots when live tokens are newer),
  the switch algorithm (snapshot current → restore target → patch profile →
  update active marker), doctor checks.
- `internal/cli` — cobra command tree; all IO injectable (`IO` struct) so
  prompts are testable without a TTY.
- `internal/tui` — bubbletea model; modes list/confirm-add/confirm-remove/
  rename; fsnotify watch on `~/.claude` re-runs discovery.

Every filesystem path flows from an injected `Env`/store dir — tests always
run in `t.TempDir()`, never against real `~/.claude`.

Invariant to preserve at all costs: the switch algorithm snapshots the live
credentials into the current account's slot **before** restoring the target,
so refresh-token rotation is never lost.
