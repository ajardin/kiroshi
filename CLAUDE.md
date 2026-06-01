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

Two documented exceptions where colRed leaves its reserved-for-errors
bucket and shows up on a non-error row:

1. **CI cell** — green for passing, red for failing, **cyan for pending**
   (matches "in progress elsewhere"; yellow would collide semantically with
   "Waiting On You"), muted for "no CI". See `ciFragment` in
   `internal/tui/tui.go`.
2. **Diff cell** — `+N` in green and `-N` in red, like `git diff` and every
   diff viewer users already know. Both sides are always shown (including
   a `+0` or `-0`) so neighboring columns stay aligned across rows; an
   all-zero diff falls back to a muted em-dash. See `renderDiff`.

Both exceptions are deliberate concessions to universal conventions; do
not extend the list without discussion.

A third, green-side concession: the **approval marker** (`approvalFragment`,
a `✓ you` cell on the row's second line) renders in `colGreen` when the
viewer has approved the PR. Green here means "your approval is in" — the
universal GitHub "approved" convention — even though the row's bucket accent
may be cyan (Waiting On Others) rather than green. The cell is omitted
entirely when the viewer hasn't approved; it is *not* a permanent column, so
unlike the diff cell it has no aligned-placeholder requirement.

### CI state aggregation (locked)

`aggregateCheckRuns` in `internal/gh/client.go` collapses GitHub's check
runs for the PR's head SHA into a single `CIState`:

- **none** — no check runs reported (distinct from success — UI must not
  render a green build for repos that don't run CI).
- **failure** — any completed run with conclusion `failure`, `cancelled`,
  `timed_out`, `action_required`, or `stale`. All five require human
  intervention before merge, so they collapse together.
- **pending** — any run whose status is not `completed` (`queued` or
  `in_progress`), and no failures.
- **success** — everything completed; `success`, `neutral`, and `skipped`
  all count (GitHub doesn't block merge on neutral/skipped).

Precedence is **failure > pending > success > none**. This mirrors what
the GitHub PR page summarizes at the bottom of the conversation tab.

Scope: we only consult the Checks API (Actions + GitHub Apps), **not** the
legacy commit-statuses API. Repos that rely on webhook-only CI (some
self-hosted Buildkite/CircleCI setups) will show `ci: —` here even though
GitHub.com shows a status. Cross that bridge if anyone complains.

### Bucket semantics (locked)

```
WaitingOnYou    = viewer is still expected to act (in requested_reviewers OR
                  has only COMMENTED so far) AND hasn't given a decisive
                  answer (no APPROVED / CHANGES_REQUESTED)
WaitingOnOthers = viewer gave a decisive answer; someone else is still
                  expected to act (requested_reviewers or COMMENTED-only)
ReadyToShip     = >= min_reviews non-author approvals AND no outstanding
                  CHANGES_REQUESTED
InFlight        = anything else (drafts, viewer is the author with no
                  decision pending elsewhere, etc.)
```

Classification order matters and is encoded in `bucketFor`:

1. Drafts → InFlight (never mergeable).
2. ReadyToShip wins over the viewer-as-author check — a user wants to see
   "ready to merge" on their own PRs, since they're the one who clicks
   merge.
3. CHANGES_REQUESTED from any non-author reviewer blocks ReadyToShip even
   if there are enough approvals; this mirrors GitHub's own block-on-
   changes behavior.
4. WaitingOnYou comes before the viewer-as-author short-circuit so that
   if you've been re-requested on your own PR (rare but possible), it
   still shows up as on you.

**Why Commented matters.** Submitting *any* review on GitHub (including a
COMMENTED one) removes you from `requested_reviewers`. But you're still on
the hook: GitHub's Reviewers panel keeps you listed with a re-request
icon, and "you commented and never approved" is exactly the case the user
hits most. So `summarizeReviews` produces a separate `Commented` list and
`bucketFor` treats it as "still pending" for both the WaitingOnYou and
WaitingOnOthers branches.

The `min_reviews` threshold is project-configurable (`config.MinReviews`,
default `2`). It only counts approving reviews from logins *other than the
author*. Per reviewer:

- APPROVED / CHANGES_REQUESTED win unconditionally (latest decisive
  review sticks).
- COMMENTED only fills the state if there's no decisive review yet for
  that reviewer.
- DISMISSED clears any prior state — a later COMMENTED can then surface
  the reviewer in `Commented`.

Card order in the header is fixed:
`Waiting On You → Waiting On Others → Ready To Ship → In Flight`.

### Selected row treatment

The mockup's subtle highlight wasn't readable enough. The locked treatment:

- Bar character: `┃` (selected) vs `│` (unselected)
- Bar color: **always the bucket accent**, never overridden by the selection
  state. An earlier version forced yellow on the selected bar; that
  collided with the "Waiting On You" yellow card and made every selected
  row look like it was waiting on the viewer. Selection is signalled by
  the bar *character* + arrow + title weight + background fill instead.
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

### Sort toggle

The `s` key cycles `Model.sort` through three states with distinct
semantics, all sorted client-side in `visiblePRs`:

- `sortDefault` — `UpdatedAt desc` (most recent activity). Coincides with
  GitHub's search API default but is sorted explicitly so the ordering is
  deterministic regardless of API quirks.
- `sortOldestFirst` — `CreatedAt asc` (oldest creations first).
- `sortNewestFirst` — `CreatedAt desc` (newest creations first).

The header appends `· oldest created` / `· newest created` for the two
explicit modes; `sortDefault` shows no suffix. The labels say "created"
on purpose: `sortDefault` ≠ `sortNewestFirst` precisely when a PR has
been updated recently but created long ago (revived by a comment), so
the user needs to know which field is in play.

Two non-obvious bits in `visiblePRs`:

1. **Always copy before sort.** When no filter is active, the local
   `out` aliases `m.prs`. `sort.SliceStable` mutates in place, so
   without `append([]gh.PullRequest(nil), out...)` the underlying
   fixture would get silently reordered.
2. **`sort.SliceStable`, not `sort.Slice`.** Equal timestamps
   (bot-created batches) keep the GitHub API order as a stable
   tiebreaker — the most intuitive secondary sort.

Unlike `f` (filter), `s` does not reset the cursor: the set is
identical, only the order changes. `cycleSort` captures the selected
PR's URL before the toggle and relocates the cursor onto the same PR's
new index. Reset-to-zero would be jarring.

### Approval filter toggle

The `a` key cycles `Model.approval` through three states, applied in
`visiblePRs` after the text filter (the two stack):

- `approvalAll` — no filtering.
- `approvalMine` — only PRs the viewer approved
  (`containsLogin(pr.Approvals, m.login)`).
- `approvalNotMine` — the complement.

The header appends `· approved by you` / `· not approved by you` for the two
explicit modes. `cycleApproval` mirrors `cycleSort`'s cursor-follow logic but
with a twist: the `a` filter *shrinks* the set, so when the previously
selected PR is filtered out it falls back to `clampCursor` (cursor 0) instead
of holding position. The visual counterpart is the `✓ you` row marker (see
the palette note above) — same data source, so a PR showing the marker is
exactly one that survives `approvalMine`.

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
- **Setup wizard.** `kiroshi -init` (and the auto-fallback when no config
  exists on a TTY) runs `tui.WizardModel`, a second Bubble Tea program with
  its own `RunWizard`. It reuses the dashboard palette and the filter's
  hand-rolled text-input pattern (no `bubbles/textinput`); the token step is
  masked. The CLI gates the auto-fallback on `errors.Is(err,
  config.ErrNotFound)` so pipes/CI/`-no-tui` still error out instead of
  blocking on a prompt. Token validation and the wizard runner are injected
  through `WithTokenValidator` / `WithWizardRunner` (same test-seam idea as
  `WithTUIRunner`) so tests touch neither the network nor a real terminal.
  `config.Save` writes the file at mode `0600`.

## Workflow

- `make lint` runs golangci-lint v2. The repo enables revive's `exported`,
  `var-naming`, and `package-comments` rules — every exported symbol and
  package needs a doc comment.
- `go test -race -count=1 ./...` is the test command. Keep it green.
- To eyeball the TUI: `rtk proxy go test -v -run TestPreview ./internal/tui`
  (rtk filters by default; `proxy` bypasses it so the rendered output
  reaches the terminal).

## Phase roadmap

- **Phase 1**: visual shell. ✅ shipped.
- **Phase 2**: review-state classification. ✅ shipped. `bucketFor` queries
  `PullRequests.ListReviewers` (pending requested reviewers) and
  `PullRequests.ListReviews` (review history) for every PR in the search
  result, then summarizes per-reviewer state via `summarizeReviews`. Two
  extra REST calls per PR.
- **Phase 3 (current)**: enrich placeholder fields (CI, diff stats, Jira).
  - CI ✅ shipped. `enrichCIState` reads `Checks.ListCheckRunsForRef`
    against `pr.HeadSHA` and folds the runs through `aggregateCheckRuns`.
  - Diff stats ✅ shipped. `enrichDetail` calls `PullRequests.Get` once and
    fills `HeadSHA`, `Additions`, `Deletions` together — the head SHA is a
    prerequisite for the Checks call, so this was the natural seam. No new
    REST cost on top of CI; the row renders `+N -M` via `renderDiff`.
  - Jira ticket: not started.

  Per-PR enrichment runs in this order via `enrichPullRequest`:
  `enrichReviewState` → `enrichDetail` → `enrichCIState` (the dependency is
  real — CI needs the head SHA published by detail). Four extra REST calls
  per PR. **Across PRs, enrichment is parallelized** through an
  `errgroup` worker pool capped at `enrichConcurrency` (8). Eight is a
  comfortable margin under GitHub's secondary rate limit (~100 concurrent
  requests per token); raise it if you ever need to handle hundreds of PRs
  in one rescan. Error semantics are fail-fast — the first enricher error
  cancels the others and surfaces through the rescan status line.
- **Phase 4**: `?` help overlay. Not started. The footer used to
  advertise `?` as a key hint with a no-op handler; both were removed
  to avoid promising a shortcut that does nothing. When Phase 4 lands,
  add the hint back to `footerView` and wire the handler in
  `handleListKey`.
