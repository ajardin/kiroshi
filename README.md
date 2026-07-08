# kiroshi

A terminal dashboard that classifies your GitHub pull requests by who is
expected to act next: **Waiting On You**, **Waiting On Others**,
**Ready To Ship**, or **In Flight**.

Two panes, toggled with `tab`: **Incoming** (PRs you're reviewing) and
**Mine** (PRs you authored). The Mine pane reuses the same four cards with
author-side labels — **Needs You**, **In Review**, **Ready**, **Draft** —
splitting whatever your search returns by author, with no extra API calls.

Built as a CLI with an optional Bubble Tea TUI. Plain-text output is
available for pipes, CI, and any non-TTY context.

## Install

### Homebrew (macOS)

```bash
brew install ajardin/tap/kiroshi
```

The tap auto-installs, so no separate `brew tap` step is needed. Upgrades
come through `brew upgrade` like any other formula.

### Go

```bash
go install github.com/ajardin/kiroshi/cmd/kiroshi@latest
```

Requires Go 1.25 or newer.

### Pre-built binaries

Archives for Linux, macOS, and Windows (amd64/arm64) ship with every tagged
release on the [releases page](https://github.com/ajardin/kiroshi/releases).
Download, extract, and put the `kiroshi` binary on your `PATH`.

## Configure

The fastest way to create the config is the interactive wizard:

```bash
kiroshi -init
```

It prompts for the token, search query, `min_reviews`, an optional
auto-refresh interval, and optional Jira credentials, validates the token
against GitHub live, and writes the file
(mode `0600`). kiroshi also launches the wizard automatically the first time
you run it on a terminal with no config present.

Re-running `kiroshi -init` with an existing config re-walks the wizard
pre-filled with the current values, so you can add Jira later or tweak the
search query without re-typing everything. On the masked token steps, leaving
the input blank keeps the stored token (it is still re-validated live); typing
a new one replaces it. Entering `-` on the Jira base URL step removes the Jira
configuration entirely. A config file that no longer loads cleanly is never
overwritten — fix or delete it first.

### GitHub token

Create a [fine-grained personal access token](https://github.com/settings/personal-access-tokens) —
kiroshi is strictly read-only, so the token never needs write access:

- **Repository access** — the repositories (or organizations) your search
  query covers.
- **Repository permissions** — **Pull requests: Read-only** and
  **Contents: Read-only** (**Metadata: Read-only** is granted automatically).
- **Organization permissions** — **Members: Read-only** (only needed when
  the search query relies on org membership).

One caveat: a fine-grained PAT only sees the repositories it was explicitly
granted, so `involves:@me` silently returns nothing for repos outside the
grant. If results look incomplete, check the token's repository access first.

The unresolved review-thread count uses the GraphQL API with the same token.
If your token or organization restricts GraphQL, kiroshi silently omits that
cell and everything else keeps working.

If your organization restricts fine-grained PATs, fall back to a classic
token with the `repo` and `read:org` scopes — note that this grants far
broader access (including write) than kiroshi ever uses.

### Config file

To write the file by hand instead of running the wizard: kiroshi reads a
TOML file from
`$XDG_CONFIG_HOME/kiroshi/config.toml`
(or `~/.config/kiroshi/config.toml` when `XDG_CONFIG_HOME` is unset).

```toml
# ~/.config/kiroshi/config.toml

# Personal access token used to call the GitHub REST API — see "GitHub
# token" above for the recommended fine-grained, read-only permission set.
# Can also be supplied via the GITHUB_TOKEN environment variable, which
# takes precedence over this field.
github_token = "github_pat_xxxxxxxxxxxxxxxxxxxx"

# Any valid GitHub issues/search query. The advanced_search backend is
# forced on automatically so boolean expressions work as expected.
# `involves:@me` returns both the PRs you authored and the ones you're asked
# to review; the TUI splits them into two panes (toggle with `tab`).
search = "is:pr is:open involves:@me archived:false"

# Minimum number of non-author APPROVED reviews required before kiroshi
# classifies a pull request as Ready To Ship. Defaults to 2.
min_reviews = 2

# Optional auto-refresh cadence for the TUI, as a Go duration ("30s", "5m",
# "1h"). When set, the dashboard rescans on its own and the footer shows an
# "auto <interval>" indicator. Omit it (or set 0) to refresh only on demand
# with the "r" key.
refresh_interval = "5m"

# Optional terminal bell: when true, a rescan (manual "r" or auto-refresh)
# that moves a pull request into Waiting On You emits an ASCII BEL and a
# "<n> new waiting on you" status note. Your terminal or tmux translates the
# BEL into its configured alert (sound, visual bell, window flag). Defaults
# to false.
notify = true

# Optional Jira Cloud integration. When set, kiroshi extracts the issue key
# from each PR's branch, title, or body (e.g. PROJ-1234) and shows the ticket
# status in the listing. All three fields are required together; leave them
# out to disable Jira. The token is a Jira Cloud API token created at
# https://id.atlassian.com/manage-profile/security/api-tokens and is used with
# HTTP Basic auth (email + token). jira_token can also be supplied via the
# JIRA_API_TOKEN environment variable, which takes precedence.
jira_base_url = "https://your-org.atlassian.net"
jira_email    = "you@your-org.com"
jira_token    = "xxxxxxxxxxxxxxxxxxxx"

# Optional named search profiles for juggling several contexts (work org,
# OSS, a specific team) from one dashboard. The top-level `search` above is
# always the profile named "default" (that name is reserved); each
# [[profiles]] entry adds one more, with a unique non-empty name and a
# non-empty query. Declare as many as you need — one profile is active at a
# time, and the `p` key cycles through them in file order (default → oss →
# team → back to default below), rescanning with the new query; the header
# shows the active profile's name. You can also start on a given profile
# with `-profile <name>`. Omit the blocks entirely to keep a single search.
# Note: [[profiles]] blocks must stay at the end of the file — in TOML,
# every key after a table header belongs to that table.
[[profiles]]
name   = "oss"
search = "is:pr is:open involves:@me user:some-org"

[[profiles]]
name   = "team"
search = "is:pr is:open team-review-requested:acme/core"
```

Both token fields are redacted from structured logs (see
`config.Config.LogValue`).

## Run

```bash
kiroshi                       # interactive TUI when stdout is a terminal
kiroshi -init                 # interactively create or update the config file and exit
kiroshi -no-tui               # plain text, always
kiroshi -profile oss          # start on a named search profile (TUI or plain text)
kiroshi -config ./my.toml     # override the config path
kiroshi -verbose              # debug-level slog output on stderr
kiroshi -version              # print build metadata and exit
```

When stdout is not a TTY (pipe, file, CI), the TUI is skipped
automatically — TTY detection lives in `cli.isTerminal`.

### Keybindings (TUI)

| Key        | Action                          |
| ---------- | ------------------------------- |
| ↓/↑        | move selection down / up        |
| tab        | switch incoming / mine view     |
| g/G        | jump to top / bottom (or home/end) |
| enter / o  | open selected PR in browser     |
| y          | yank selected PR URL to clipboard |
| d          | show selected PR detail overlay |
| r          | rescan from GitHub              |
| f or /     | filter the visible list         |
| s          | cycle sort order                |
| a          | cycle approval filter           |
| p          | cycle search profiles (when configured) |
| ?          | toggle the keybindings overlay  |
| q / esc    | quit                            |

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
enrichment (review state, CI checks, diff stats) in parallel across PRs,
plus one batched GraphQL query per ~20 PRs that counts unresolved review
threads (shown as `N unresolved` in the listing and detail overlay — the
REST API doesn't expose thread resolution).
`internal/tui` is a custom Bubble Tea model — see `CLAUDE.md` for the
locked color palette, bucket semantics, and CI-state aggregation rules.

## License

[MIT](LICENSE)
