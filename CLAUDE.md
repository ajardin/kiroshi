# kiroshi — Claude working notes

CLI + TUI that surfaces GitHub pull requests.
Go 1.26, Bubble Tea v2, lipgloss v1, golangci-lint v2.

This file holds the decisions the code can't explain by itself. Read the code
for *what* it does and this file for *why*. Each reason lives in one place:
here when several files depend on it, in a code comment when it is local.

## Architecture

- `internal/cli` picks TUI or JSON output: JSON whenever stdout or stdin is
  not a character device, or with `-no-tui`. The `With…` options exist only
  as test seams.
- `internal/tui` is a custom Bubble Tea model, deliberately not `bubbles/list`,
  which can't give the pixel-level control the selected row, cards and footer
  need.
- `internal/gh` sets go-github's native `SearchOptions.AdvancedSearch`: the
  classic backend silently drops boolean expressions. Don't bring back the
  old hand-rolled `advancedSearchTransport`.
- `internal/jira` stays out of `gh`: it is another service with its own auth,
  and keeping it apart keeps the go-github test harness clean. `gh` imports
  it, never the reverse.

### JSON output contract

The JSON document **replaced** a human-readable listing, since only one
non-TUI format could be justified and only one was pipeable. So there is no
`-json` flag: it would be a synonym of `-no-tui`.

It carries **every** enriched field: the scan pays 4–5 REST calls per PR
either way. A rendered row must distil, a machine document must not. Two
rules make it a contract, and the tests enforce both:

1. **Names and enum values are stable once released.** Bucket names come from
   `tui.Bucket.String()`, so a new bucket can't serialise as `""`. The
   per-pane display labels (`ON YOU` / `NEEDS YOU`) are not part of it.
2. **Every key is always present.** No `omitempty` anywhere. An unknown is
   `null` and an empty list is `[]`. The Go zero values that would read as
   "empty" are spelled out: `none` for CI (no checks is not a passing build)
   and `clear` for the merge state. `unresolved_threads` is `null` when
   GraphQL failed, and `jira_lookup_failed` separates a failed lookup from no
   ticket.

The JSON classifies through `tui.BucketFor`, so it can never disagree with the
dashboard. It has no `mine`-pane equivalent: consumers split on
`author == login`.

## Enrichment

**Order.** `enrichDetail` makes one `PullRequests.Get` that also carries the
diff stats and merge state at no extra cost, plus the head SHA and
branch/body that `enrichCIState` and `enrichJiraStatus` need. So it must run
before them.

**Concurrency.** An errgroup pool of `enrichConcurrency` (8), well under
GitHub's secondary rate limit (~100 concurrent requests per token). Raise it
if you ever scan hundreds of PRs.

**Rescan cache.** Only the review state is cached, keyed by PR and validated
by `UpdatedAt`: every review, re-request, push or edit bumps it, so a hit
skips half the GitHub cost with zero staleness. `Get` and check runs stay live
because CI completes and `mergeable_state` flips without any PR activity. Jira
stays live too. Leaving the results evicts the entry, so a profile switch-back
re-enriches live: a cost, not a staleness bug. No config knob.

**Failure semantics.** An enricher error marks that PR `EnrichPartial` and the
scan still lands. Only three errors fail the scan: `ErrInvalidToken` and
`ErrRateLimited` (they doom every later call) and a failed search. Partial PRs
are never cached. The degradations show up this way:

- GitHub partial: red github health dot plus a `⚠` warning status (dim, not
  red: the scan landed).
- Jira: never fails the scan. A 404 means the extracted key was a false
  positive, so the cell is omitted without touching health; any other error
  sets `JiraLookupFailed` and the red jira dot.
- Unresolved threads (GraphQL, one aliased request per `threadsBatchSize`
  PRs): some tokens and orgs restrict GraphQL, so any failure leaves
  `ThreadsKnown` false without marking the PR partial. It isn't cached,
  because resolving a thread doesn't reliably bump `updated_at`.

**Jira keys.** `ExtractKey` scans branch → title → body. The regex also
matches `UTF-8` or `SHA-256`, each a silent doomed lookup on every scan, hence
the optional `jira_project_keys` allowlist. It is only valid alongside the
URL/email/token trio (rejected otherwise, because loud beats inert), which is
why `-init` carries it over only when Jira survives the wizard.

## TUI design system

### Color palette (locked)

Defined once in `internal/tui/tui.go`:

| Token           | Hex       | Semantic role                                        |
| --------------- | --------- | ---------------------------------------------------- |
| `colYellow`     | `#fbbf24` | brand mark, Waiting On You, needs-attention nudges   |
| `colCyan`       | `#38bdf8` | Waiting On Others, "in progress elsewhere", chrome   |
| `colGreen`      | `#22c55e` | Ready To Ship, approved, passing, healthy            |
| `colRed`        | `#ef4444` | failures only (see the exceptions)                   |
| `colMuted`      | `#4b5563` | borders, separators, In Flight, anything switched off |
| `colDim`        | `#9ca3af` | secondary text                                       |
| `colText`       | `#e5e7eb` | body text                                            |
| `colBright`     | `#fafafa` | emphasized text (selected row title)                 |
| `colSelectedBg` | `#1e293b` | selected-row background                              |

Yellow and cyan are the brand colors. Green is the only other accent,
accepted because "approved/ready" reads as green everywhere. Discuss before
adding a fourth.

Red stays reserved for failures, with three concessions to universal
conventions:

1. **CI failing.** Pending is cyan, since yellow would read as Waiting On You.
2. **Diff `-N`**, like `git diff`.
3. **Merge `conflict`**, which blocks merge exactly like a failing build.
   `behind` is a soft nudge and stays dim.

A switched-off setting is not a failure: the `auto off` badge is muted.

Accents are reused rather than added:

- The approval `✓` is green even on a cyan row.
- The Jira status follows the CI semantics (done green, in progress cyan,
  otherwise dim) and never goes red: a ticket is never an error.
- The age cell turns yellow past `ageForgottenAfter` (21 days): a PR open that
  long does need a look, even outside Waiting On You.

### Row layout

Line 2 is fixed columns, then a flowing tail:

```
@author      ✓ +N   -M  · <ci> · <merge> · <unresolved> · <jira status> · <age>
└ author ─┘   ↑ └─ diff ─┘  └ ci ┘ └────────────── flowing tail ──────────────┘
   wide gap  approval slot
```

- The fixed columns let the eye scan one column ("which PRs fail CI?"). Their
  widths are computed over the whole visible set rather than the page, so
  they don't jump while scrolling. An absent cell renders `—` in its column.
- The tail holds present cells only. Merge leads it: a fixed column would
  leave a gap on every clear row. The Jira key is dropped to cut noise and
  only the detail overlay shows it.
- The age counts since `CreatedAt`, not `UpdatedAt`: it nudges against
  forgotten PRs.

### lipgloss constraints

- lipgloss can't back-fill a background across already-rendered SGR resets.
  So every segment of a selected row declares the background itself (the `st`
  closure, bg-aware padding), and the overlays **replace** the dashboard
  rather than compositing over it.
- Inside a bordered box, glyphs whose width terminals disagree on (arrows)
  drift the right border. That is why the help keys are ASCII, the branch
  line uses `->` and the reviewer block is glyph-free.
- `lipgloss.Width()` excludes the border, so `renderCard` subtracts 2 from its
  total width. Don't undo that.
- `listAreaHeight` assumes a single-line header, so the header degrades by
  measurement instead of wrapping. The status line is always reserved, so
  neither the footer nor the row count shift when a notification comes and
  goes.

### CI state aggregation (locked)

Precedence is **failure > pending > success > none**, like GitHub's merge gate.
`none` is distinct from success so a repo without CI never shows green. Only
the latest run of each check counts, so a re-run fixes a failure. Only the
Checks API is consulted, not the legacy commit statuses: webhook-only CI (some
self-hosted Buildkite/CircleCI) shows `—`. Cross that bridge if anyone
complains.

### Bucket semantics (locked)

```
WaitingOnYou    = viewer expected to act (requested OR commented only)
                  and no decisive answer (APPROVED / CHANGES_REQUESTED) yet
WaitingOnOthers = viewer answered decisively; someone else still expected
ReadyToShip     = >= min_reviews non-author approvals, no CHANGES_REQUESTED
InFlight        = everything else (drafts, the viewer's own PRs, …)
```

The order in `BucketFor` matters: drafts are never ready; ReadyToShip beats
the viewer-as-author check (the author is the one who merges); any change
request blocks it, like on GitHub; WaitingOnYou beats the author
short-circuit for a re-request on your own PR.

**Why Commented counts.** Any review, COMMENTED included, drops you from
`requested_reviewers`, but GitHub still lists you with a re-request icon and
you still owe a decisive answer. "Commented and never approved" is the case
users hit most.

The mine pane reuses the four buckets (and their colors) with author-side
meanings: changes requested or red CI is on you even with enough approvals.
Its fourth card counts drafts, while incoming's shows the pane total.

### Interaction invariants

- **Modes.** List, loading, filter, help and detail are one `uiMode`, so
  they are exclusive by construction. A rescan is not a mode: the dashboard
  stays up with a spinner in place of the rows.
- **Rescans** all go through `startRescan`: the `r` key, a profile switch
  and auto-refresh. Auto-refresh skips a beat rather than stacking on a scan
  in flight.
- **Profiles.** Each `tui.Profile` carries its own `Refresher` with the query
  baked in, and `cycleProfile` swaps `m.refresh`, so every rescan path follows
  the active profile. A switch resets the text filter (unlike `tab`: panes
  share one result set, profiles don't) and is ignored mid-scan, or an old
  profile's results would land under the new name. A failed switch keeps the
  old results under the new name, the lesser evil next to blanking the
  dashboard. The `default` name is reserved for the top-level `search`.
- **Cursor.** `s` keeps the cursor on the same PR since only the order
  changes; `a` does too when the PR survives the filter. Filters, `tab` and a
  profile switch reset it.
- **Sorting.** `visiblePRs` sorts in place, which is safe because `panePRs`
  always builds a fresh slice. It uses a stable sort, so equal timestamps
  (bot batches) keep the API order. The header labels say "created" because
  the default order is by `UpdatedAt`.
- **Detail overlay.** It issues no GitHub calls, since everything is already
  enriched. `up`/`down`, `enter`/`o` and `y` keep it open; any other key
  closes it.
- **Merge state is read-only by design**: no approve or merge from the TUI.
- **Hand-edit only**: `notify`, `jira_project_keys` and `[[profiles]]`. The
  wizard never asks, and a reconfigure carries them over. `[[profiles]]` must
  sit at the end of the TOML file, which is why `fileConfig.Profiles` is its
  last field.

## Conventions

- **The commit message is the changelog entry.** goreleaser builds the release
  notes from conventional commit subjects (`feat:`, `fix:`, `refactor:`),
  filtering out `docs:`, `test:`, `chore:` and merges. There is no
  `CHANGELOG.md`.
- **Comments explain a why the code can't show**: a hidden constraint, a
  lipgloss quirk, a workaround. Version-migration history belongs in the
  commit message.
- **The browser launcher validates the URL scheme** (`http`/`https`) before
  handing it to the OS. The `//nolint:gosec` annotations in `browser.go`
  depend on that check, so keep them together.
- **Side effects run in `tea.Cmd`s.** Tests don't run the program loop: use
  `applyCmd` in `tui_test.go` to round-trip them.
- **The wizard auto-launches only on `config.ErrNotFound` on a TTY**, so pipes,
  CI and `-no-tui` still fail instead of blocking on a prompt. It validates
  the same trimmed values it saves.

## Workflow

- `make all` is the pre-push gate (lint, test, build); `make help` lists the
  targets.
- `go test -race -count=1 ./...` (`make test`) must stay green.
- revive's `exported` and `package-comments` rules are on: every exported
  symbol and package needs a doc comment.
- To eyeball the TUI: `rtk proxy go test -v -run TestPreview ./internal/tui`.
  `proxy` bypasses rtk's filtering so the render reaches the terminal, and
  the test skips without `-v`.
