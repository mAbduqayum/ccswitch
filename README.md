# ccswitch

[![ci](https://github.com/mAbduqayum/ccswitch/actions/workflows/ci.yml/badge.svg)](https://github.com/mAbduqayum/ccswitch/actions/workflows/ci.yml)

Switch between multiple Claude Code accounts. `ccswitch` snapshots the OAuth
credentials of each account you log into and swaps them on demand — no
re-login, no network calls, tokens never leave your machine.

> Work in progress — sections below are filled in as milestones land.

## Install

TODO

## Usage

TODO

## Development

The reproducible path is Nix with flakes + direnv (`direnv allow`), then
`just` for all tasks. No Nix? A system `go` (version in `go.mod`) is enough.

## License

[MIT](LICENSE)
