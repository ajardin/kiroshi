# kiroshi — Claude working notes

CLI + TUI that surfaces GitHub pull requests.
Go 1.x, Bubble Tea v1, lipgloss, golangci-lint v2.

This file captures decisions that aren't obvious from the code. For *what*
the code does, read the code; for *why* it looks the way it does, read here.

## Architecture

- `internal/cli` — flag parsing, config load, GitHub client wiring, decides
  TUI vs plain-text output. Functional options (`WithGitHubClient`,
  `WithTUIRunner`) exist purely as test seams.
- `internal/tui` — custom Bubble Tea model. We deliberately do **not** use
  `bubbles/list`: the mockup needed pixel-level control over selected-row
  highlighting, status cards, and the footer that the list bubble couldn't
  give us cleanly.
- `internal/gh` — GitHub client. Uses an `advancedSearchTransport`
  RoundTripper to hit the v2 advanced search endpoint (the classic search
  endpoint can't express the queries we need).
- `internal/config`, `internal/version` — self-explanatory.

TTY detection lives in `cli.isTerminal` and uses `os.ModeCharDevice`. Any
non-`*os.File` writer (tests, pipes, CI) falls back to plain text. The
`-no-tui` flag forces text mode regardless.

## TUI design system

### Color palette (locked)

Defined once in `internal/tui/tui.go`:

| Token           | Hex       | Semantic role                                    |
| --------------- | --------- | ------------------------------------------------ |
| `colYellow`     | `#fbbf24` | brand mark + "Waiting On You" + selected-row bar |
| `colCyan`       | `#38bdf8` | "Waiting On Others" + version + clock chrome     |
| `colGreen`      | `#22c55e` | "Ready To Ship" (universal "go/ship" semantic)   |
| `colRed`        | `#ef4444` | reserved for failures (CI, errors)               |
| `colMuted`      | `#4b5563` | borders, separators, "In Flight" total card      |
| `colDim`        | `#9ca3af` | secondary text (timestamps, "last scan")         |
| `colText`       | `#e5e7eb` | default body text                                |
| `colBright`     | `#fafafa` | emphasized text (selected row title)             |
| `colSelectedBg` | `#1e293b` | full-width background fill for selected row      |

Yellow and cyan are the two brand colors. Green is the only chromatic
addition — accepted because "approved/ready" reads as green by universal
convention (CI, GitHub merge button). Do not introduce a fourth chromatic
accent without a discussion.

### Bucket semantics (locked)

```
WaitingOnYou    = viewer is a requested reviewer who hasn't reviewed yet
WaitingOnOthers = viewer reviewed; at least one other reviewer hasn't
ReadyToShip     = approved by viewer and all required reviewers
InFlight        = total of all PRs in the search (default/unclassified)
```

Phase 1 puts every PR in `InFlight`; later phases will populate the
review-state buckets. Card order in the header is fixed:
`Waiting On You → Waiting On Others → Ready To Ship → In Flight`.

### Selected row treatment

The mockup's subtle highlight wasn't readable enough. The locked treatment:

- Bar character: `┃` (selected) vs `│` (unselected)
- Bar color: yellow when selected (overrides bucket accent)
- Arrow: `▶` (selected) vs `▷` (unselected)
- Title: `colBright` + bold (selected) vs `colText` regular
- Background: full-width `colSelectedBg` fill on both rows of the entry

Implemented via the `st()` helper in `renderRow` because lipgloss can't
back-fill a background across already-rendered SGR resets — every styled
segment must declare its bg up-front.

### Card sizing gotcha

`lipgloss.Width()` does **not** include border characters. For 4 cards in
a 120-col terminal, naively dividing the width gives cards 2 cols wider
than expected, overflowing the layout. `renderCard(label, count, color, totalWidth)`
takes `bodyW := totalWidth - 2` to compensate. Don't undo that.

## Conventions

- **No comments unless the why is non-obvious.** Don't restate what
  well-named code already shows. Do explain hidden constraints, lipgloss
  quirks, or workarounds.
- **Browser launcher must validate URL scheme** (`http`/`https` only)
  before handing to `open`/`xdg-open`/`rundll32`. The `//nolint:gosec`
  annotations in `browser.go` depend on that validation — keep them
  together.
- **Status updates flow through `tea.Cmd`**, which the program loop
  invokes and feeds back through `Update`. Tests don't run that loop, so
  use the `applyCmd` helper in `tui_test.go` to round-trip cmds.
- **Preview test** (`TestPreview`) is a developer aid that prints the
  rendered dashboard. It's skipped unless `-v` is passed. Keep it skipped
  by default — it's not an assertion.

## Workflow

- `make lint` runs golangci-lint v2. The repo enables revive's `exported`,
  `var-naming`, and `package-comments` rules — every exported symbol and
  package needs a doc comment.
- `go test -race -count=1 ./...` is the test command. Keep it green.
- To eyeball the TUI: `rtk proxy go test -v -run TestPreview ./internal/tui`
  (rtk filters by default; `proxy` bypasses it so the rendered output
  reaches the terminal).

## Phase roadmap

- **Phase 1 (current)**: visual shell. All PRs in `InFlight`. Placeholder
  `—` for review state, CI status, diff stats, Jira links.
- **Phase 2**: review-state classification — implement `bucketFor` against
  the GitHub Reviews API to populate the three semantic buckets.
- **Phase 3**: enrich placeholder fields (CI, diff stats, Jira).
- **Phase 4**: `?` help overlay (currently a no-op since the footer
  already shows the keys).
