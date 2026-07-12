# Contributing to ccswitch

Thanks for your interest in improving ccswitch!

## Development environment

The reproducible path is Nix with flakes + [direnv]/[nix-direnv]: the
`flake.nix` dev shell provides the Go toolchain, `gopls`, `golangci-lint`,
`gofumpt`, `delve`, `gh`, and `goreleaser`.

```sh
direnv allow      # one-time; auto-enters the dev shell on cd
```

No Nix? A system `go` (see the version in `go.mod`) is enough — the codebase
has no hidden dependencies. The flake just pins versions.

## Workflow

Tasks run through [`just`](https://github.com/casey/just):

```sh
just run              # run the TUI
just build            # produces ./ccswitch
just fmt              # gofumpt -w .
just lint             # golangci-lint run
just test             # go test -race ./...
just cover            # tests + coverage summary
just check            # lint + test
```

Run `just fmt lint test` before opening a PR. CI runs lint and govulncheck
(Linux) and the test suite with `-race` on Linux and macOS; all must pass.

Never run tests or manual experiments against your real `~/.claude` —
everything is injectable; use a temp `HOME`/`Env` (see AGENTS.md).

## Nix package

`flake.nix` exports `packages.default` (buildGoModule). After any change to
`go.mod`/`go.sum`, refresh the vendor hash:

1. Set `vendorHash = nixpkgs.lib.fakeHash;` in `flake.nix`.
2. Run `nix build` and copy the correct hash from the mismatch error.
3. Paste it back into `vendorHash`.

## Commits

Commit messages follow [Conventional Commits]. The `commit-msg` git hook
(installed via [lefthook]) enforces one of these types:

```
new | feat | fix | docs | style | ref | perf | test | chore | build | ci | revert
```

Example: `fix: keep rotation order stable after removing an account`.

## Pull requests

1. Branch off `main`.
2. Keep changes focused; update the README/docs when behavior or config changes.
3. Reference the issue you're closing (`Closes #NN`).
4. Make sure CI is green.

[direnv]: https://direnv.net/
[nix-direnv]: https://github.com/nix-community/nix-direnv
[Conventional Commits]: https://www.conventionalcommits.org/
[lefthook]: https://github.com/evilmartians/lefthook
