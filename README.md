# kiroshi

A terminal dashboard that classifies your GitHub pull requests by who is
expected to act next: **Waiting On You**, **Waiting On Others**,
**Ready To Ship**, or **In Flight**.

Built as a CLI with an optional Bubble Tea TUI. Plain-text output is
available for pipes, CI, and any non-TTY context.

## Install

```bash
go install github.com/ajardin/kiroshi/cmd/kiroshi@latest
```

Requires Go 1.24 or newer. Pre-built binaries for Linux, macOS, and
Windows ship with each tagged release on GitHub.

## Configure

The fastest way to create the config is the interactive wizard:

```bash
kiroshi -init
```

It prompts for the token, search query, and `min_reviews`, validates the
token against GitHub live, and writes the file (mode `0600`). kiroshi also
launches the wizard automatically the first time you run it on a terminal
with no config present. To write the file by hand instead:

kiroshi reads a TOML file from
`$XDG_CONFIG_HOME/kiroshi/config.toml`
(or `~/.config/kiroshi/config.toml` when `XDG_CONFIG_HOME` is unset).

```toml
# ~/.config/kiroshi/config.toml

# Personal access token used to call the GitHub REST API.
# Can also be supplied via the GITHUB_TOKEN environment variable, which
# takes precedence over this field. Required scopes:
#   - repo        (read pull requests in private repos)
#   - read:org    (resolve org membership for the search query)
github_token = "ghp_xxxxxxxxxxxxxxxxxxxx"

# Any valid GitHub issues/search query. The advanced_search backend is
# forced on automatically so boolean expressions work as expected.
search = "is:pr is:open author:@me archived:false"

# Minimum number of non-author APPROVED reviews required before kiroshi
# classifies a pull request as Ready To Ship. Defaults to 2.
min_reviews = 2
```

The token field is redacted from structured logs (see
`config.Config.LogValue`).

## Run

```bash
kiroshi                       # interactive TUI when stdout is a terminal
kiroshi -init                 # interactively create the config file and exit
kiroshi -no-tui               # plain text, always
kiroshi -config ./my.toml     # override the config path
kiroshi -verbose              # debug-level slog output on stderr
kiroshi -version              # print build metadata and exit
```

When stdout is not a TTY (pipe, file, CI), the TUI is skipped
automatically — TTY detection lives in `cli.isTerminal`.

### Keybindings (TUI)

| Key  | Action                  |
| ---- | ----------------------- |
| j/k  | navigate up / down      |
| o    | open selected PR in browser |
| r    | rescan from GitHub      |
| f    | filter the visible list |
| s    | cycle sort order        |
| q    | quit                    |

## Development

```bash
make build     # compile ./bin/kiroshi
make test      # go test -race -count=1 ./...
make bench     # baseline benchmarks (enrichment hot path)
make cover     # coverage report
make lint      # golangci-lint v2
make fmt       # gofmt + goimports via golangci-lint
```

Install the pre-commit hooks once to mirror the CI checks locally:

```bash
pip install pre-commit && pre-commit install
```

To preview the TUI without launching a real session:

```bash
go test -v -run TestPreview ./internal/tui
```

(Use `rtk proxy go test ...` if you have rtk installed and want raw
output instead of token-filtered.)

## Architecture

`internal/cli` parses flags and wires the GitHub client to either the
TUI or plain-text renderer. `internal/gh` is a narrow wrapper around
[`go-github`](https://github.com/google/go-github) that adds REST
enrichment (review state, CI checks, diff stats) in parallel across PRs.
`internal/tui` is a custom Bubble Tea model — see `CLAUDE.md` for the
locked color palette, bucket semantics, and CI-state aggregation rules.

## License

[MIT](LICENSE)
