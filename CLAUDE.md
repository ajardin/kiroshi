# kiroshi — Claude working notes

CLI + TUI that surfaces GitHub pull requests.
Go 1.25, Bubble Tea v1, lipgloss, golangci-lint v2.

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
- `internal/jira` — optional Jira Cloud client (REST v3, HTTP Basic). A
  separate package, not folded into `internal/gh`: it's a different service
  with different auth, and keeping it out keeps the go-github wrapper and its
  test harness clean. `gh` imports it (one-directional); `gh.NewWithJira`
  wires it in. Also home to `ExtractKey` (PR → issue key).
- `internal/config`, `internal/version` — self-explanatory.

TTY detection lives in `cli.isTerminal` and uses `os.ModeCharDevice`. Any
non-`*os.File` writer (tests, pipes, CI) falls back to plain text. The
`-no-tui` flag forces text mode regardless.

### Enrichment & feature constraints

**Per-PR enrichment order is load-bearing.** `enrichPullRequest` chains four
GitHub REST calls — `enrichReviewState` → `enrichDetail` → `enrichCIState` →
`enrichJiraStatus` — plus an optional fifth Jira call. `enrichDetail` (one
`PullRequests.Get`) publishes `HeadSHA`, `HeadRef`, `Body`, `MergeState`,
`Additions`, `Deletions` in a single shot; `enrichCIState` needs the head SHA
and `enrichJiraStatus` needs the branch/body, so they must run after it. Net
effect: CI, diff stats, and merge state all ride that one Get at no extra REST
cost. Review state is two extra calls (`ListReviewers` + `ListReviews`).

**Concurrency.** Across PRs, enrichment runs through an `errgroup` worker pool
capped at `enrichConcurrency` (8) — a comfortable margin under GitHub's
secondary rate limit (~100 concurrent requests per token). Raise it if you ever
scan hundreds of PRs at once.

**Rescan cache.** `gh.Client` memoizes review state per PR (`cachedEnrichment`,
mutex-guarded, keyed `owner/repo#number`, validated by `UpdatedAt`): any review
submission, re-request, push or title/body edit bumps `updated_at`, so an
unchanged `UpdatedAt` lets a rescan skip `ListReviewers` + `ListReviews` — half
the GitHub cost with zero staleness. `Get` and check-runs deliberately stay
live: check runs complete and `mergeable_state` flips (base branch moved)
without any PR activity, so caching them would show stale CI/merge cells. Jira
stays live too (different service, own quota). Entries for PRs that leave the
search results are evicted each scan (`pruneCache`). Internal optimization —
no config knob.

**Error semantics.** Per-PR degradation for the GitHub enrichers — an
enricher error marks that one PR `EnrichPartial` and the chain moves on, so
the PR keeps whatever was enriched before the failure and the scan still
lands (missing cells already render as absent). Only two errors stay
fail-fast, checked with `errors.Is` in `enrichPullRequest`: `ErrInvalidToken`
and `ErrRateLimited` doom every subsequent call, so they cancel the errgroup
and surface their actionable messages on the rescan status line — as does a
failure of the search itself. Degradation is surfaced by the header's github
health dot (red while any PR is partial) plus a muted (`colDim`, not red —
warning, not error) status note `N pull request(s) partially enriched`.
Partial PRs are never cached: `storeReviewState` only runs after both review
calls succeed, so the next scan retries them live. Jira keeps its own
degradation: `enrichJiraStatus` swallows every
error (auth, network, 404) and degrades to an omitted cell rather than failing
the scan; it's also a full no-op under `gh.New` (vs `gh.NewWithJira`). Key
extraction is `jira.ExtractKey` (branch → title → body, regex
`[A-Z][A-Z0-9]+-\d+`, first match wins). Config is the
`jira_base_url`/`jira_email`/`jira_token` trio (env override `JIRA_API_TOKEN`);
all three or none.

**Help overlay.** The `?` key sets `Model.mode = modeHelp` (the UI modes —
list, loading, filter, help, detail — are one `uiMode` enum, so they are
mutually exclusive by construction and `handleKey`/`View` both switch on it);
`handleKey` routes to `handleHelpKey`, which dismisses on *any* key (ctrl+c
still quits). `View`
returns `helpView` (a centered `lipgloss.Place` modal) **instead of** the
dashboard — it replaces rather than composites, because lipgloss v1 can't
back-fill a box over already-rendered content (same constraint behind `st()`).
Help-row keys are deliberately **ASCII** (no ↑/↓): arrow glyphs are
ambiguous-width, so lipgloss and the terminal disagree on cell count and the
modal's right border drifts — a box must align all four sides, unlike the
left-anchored row columns (▶/✓/●).

**Detail overlay.** The `d` key sets `Model.mode = modeDetail` (guarded: a
no-op on an empty list); `handleKey` routes to `handleDetailKey`, and `View` returns
`detailView` instead of the dashboard. Unlike `handleHelpKey` (dismiss on any
key), the detail overlay is **navigable**: `↑`/`↓` reuse `moveUp`/`moveDown` to
flip the selection to the previous/next PR without leaving the overlay (the
cursor moves through `visiblePRs` and `detailView` re-reads `visible[m.cursor]`,
so no extra rendering plumbing), `enter`/`o` reuse `openSelected` to open the
current PR in the browser (the overlay stays open — `openSelected` never touches
the mode), `ctrl+c` quits, and any other key closes the overlay. It's **purely presentational** — every
field (`Body`, the four reviewer lists, CI/merge/Jira) is already enriched on
the `PullRequest`, so opening it issues **no** GitHub calls and `internal/gh` is
untouched. The meta line **reuses the row fragments** (`renderDiff`,
`ciFragment`, `mergeFragment`) ` · `-joined — but as a flowing line, not
`renderRow`'s fixed-width columns, so it deliberately does *not* share the
line-2 assembler. **Jira is the exception:** it's *not* in the meta line — it
gets its own labelled row (`JIRA   KEY · Status`, inline-label style like
`renderReviewers`, status colored via `jiraColor`), present only when a key
resolved, so the key + status read at a glance instead of being buried in the
packed meta line. `helpView` and `detailView` share the box chrome via
`modalBox`. Two locked layout choices: (1) `renderReviewers` is **glyph-free**
(colored label words only) — it lives inside the bordered box, where
ambiguous-width glyphs would drift the right border, the same constraint that
keeps help rows ASCII; the lone in-box glyph is `ciFragment`'s `✓/●/✗`, a
single non-width-dominant line. (2) the body is wrapped to `bodyW` and capped at
`maxBodyLines` (10) with a `… (N more lines)` indicator, so a long description
never dominates the panel on tall terminals. Not surfaced as a separate config —
it's a key binding, listed in `helpView` and the footer.

**Auto-refresh.** Optional `refresh_interval` config (Go duration; `0`/absent
disables; no env override). When > 0, `Init` arms an `autoRefreshCmd`
`tea.Tick`; the `autoRefreshMsg` handler re-arms the tick and rescans **through
the same path as the `r` key**, skipping when a scan is already in flight (no
stacking). Footer shows a cyan `● auto <interval>` (`shortDuration`). Not
surfaced in `helpView` — it's config-driven, not a key binding.

**Merge state is read-only by design** — no approve/merge from the TUI. Render
details live in "Color palette" / "Row line-2 layout".

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

Three documented exceptions where colRed leaves its reserved-for-errors
bucket and shows up on a non-error row:

1. **CI cell** — green for passing, red for failing, **cyan for pending**
   (matches "in progress elsewhere"; yellow would collide semantically with
   "Waiting On You"), muted for "no CI". It's a fixed aligned column (see
   "Row line-2 layout"), so the textual `ci:` prefix is dropped — position
   identifies it. See `ciFragment` in `internal/tui/fragments.go`.
2. **Diff cell** — `+N` in green and `-N` in red, like `git diff` and every
   diff viewer users already know. Both sides are always shown (including
   a `+0` or `-0`); an all-zero diff falls back to a muted em-dash. It's a
   real aligned column: `renderDiff` left-pads the `+N` sub-field to the
   widest `+N` in the visible set so the `-M` parts line up under each other
   across rows (see "Row line-2 layout"). See `renderDiff`.
3. **Merge cell** — the word `conflict` in red when GitHub reports the PR's
   `mergeable_state` as `dirty`. A merge conflict blocks merge exactly like a
   failing build, so it earns the same "action required" accent. The only
   other surfaced state, `behind` (head branch behind its base), is a soft
   nudge and stays `colDim` — no new accent. Every other state (clean,
   blocked, unstable, draft, and GitHub's not-yet-computed `unknown`) renders
   blank, so most rows show nothing here. It's a self-describing word (no glyph
   — avoids width ambiguity — and no prefix) rendered as the **first item of the
   flowing tail** (like the Jira cell), present only on flagged rows. It was a
   fixed aligned column once, but reserving the width left a visible gap on every
   clear row for a rarely-present cell; the tail placement drops that gap.
   See `mergeFragment` in `internal/tui/fragments.go` and `MergeState` /
   `normalizeMergeState` in `internal/gh/client.go`.

All three exceptions are deliberate concessions to universal conventions; do
not extend the list without discussion.

A third, green-side concession: the **approval marker** (`approvalFragment`,
a compact `✓` on the row's second line) renders in `colGreen` when the
viewer has approved the PR. Green here means "your approval is in" — the
universal GitHub "approved" convention — even though the row's bucket accent
may be cyan (Waiting On Others) rather than green. It occupies a fixed
one-column slot between the author and diff columns (a blank space when the
viewer hasn't approved), so it keeps the diff/ci columns aligned rather than
shifting them. The "you" word was dropped: position plus the green check
carry the meaning.

The **Jira cell** (`jiraColor`) is a fourth concession, but a
reuse-only one — it introduces no new accent. It colors by the issue's
`statusCategory` with the same semantics as CI: `done` → green (like
"ci: passing"), `indeterminate` → cyan (in progress elsewhere, like
pending), `new`/unknown → `colDim`. There is deliberately **no red**
state — a Jira ticket is never an "error". Unlike CI/diff it is **not** a
fixed column: it's a flowing tail item. In the listing it renders the
**status word alone** (e.g. `In Review`) — the `ABC-123` key is dropped to
cut noise on an already-dense line, and there's no collision risk because the
neighbouring cells are glyphs (CI) or distinct words (`conflict`/`behind`).
The full `KEY · Status` only shows in `detailView`, on its own labelled
`JIRA` row. The cell is **omitted entirely** when the PR references no
resolved ticket — either no key at all, or a lookup that failed (the
enricher leaves all three Jira fields empty in both cases, so the cell can't
tell them apart).

The **age cell** (`ageColor`) is a fifth concession, this one yellow-side:
its color escalates with the PR's age-since-`CreatedAt` — `colMuted` fresh,
`colDim` past `ageStaleAfter` (7d), `colYellow` past `ageForgottenAfter`
(21d). Yellow here is a deliberate reuse of the "needs your attention" accent
even though the row isn't in the WaitingOnYou bucket: a PR that's sat open for
three weeks *does* need a look, so the anti-forgetting nudge earns the accent.
No new color, no red (an old PR isn't an "error"). Both the flowing-tail age
and `detailView`'s meta age share this one helper.

### Row line-2 layout (column alignment)

Each PR row's second line is **fixed-width columns followed by a flowing
tail**, so the high-signal status cells line up vertically and the eye can
scan one column ("which PRs are failing CI?"):

```
@author      ✓ +N   -M  · <ci> · <merge> · <jira Status> · <age>
└ author ─┘   ↑ └─ diff ─┘  └ ci ┘ └─────────── flowing tail ──────────┘
   wide gap  approval slot
```

- **Fixed columns** (still aligned across rows so the eye can scan one column):
  author, the one-col approval slot, diff, ci. Absent diff/ci render a muted `—`
  *in the column* (the placeholder is justified — the column is real). The author
  is set apart from the indicators by a **wider gap** (`authorGap`), and every
  indicator block is joined by a uniform ` · ` (`sep`) — including diff→ci, which
  used to be a bare space. The approval marker stays glued to the diff (no
  separator): it annotates the PR, not a block of its own.
- **Flowing tail** (` · `-separated, present items only): the merge cell
  (`conflict`/`behind`, dropped when not flagged) then the Jira cell — the
  bare **status word** colored via `jiraColor` (the `ABC-123` key is dropped
  here to cut noise; it survives only in `detailView`), dropped when there's
  no key — then the age (`humanAgo`, no `updated`
  prefix). Empty cells are dropped, not shown as `jira: —`/`ci: —` noise.
  Merge leads the tail because it's the most action-worthy; it used to be a
  fixed column but reserving the width left a gap on every clear row.
  The age measures **time since `CreatedAt`** (PR opened), *not* `UpdatedAt` —
  it's an anti-forgetting nudge for PRs that have sat open, so its color
  escalates with age via `ageColor`: `colMuted` while fresh, `colDim` past
  `ageStaleAfter` (7d), `colYellow` past `ageForgottenAfter` (21d). `detailView`'s
  meta line reuses the same `ageColor`/`CreatedAt` pair. See the palette note on
  this yellow reuse.

Column widths are computed **once per render over the full visible set**
(not just the on-screen page, so columns don't jump while scrolling) by
`computeRowCols` → `rowCols`: max `@author` width (capped at `maxAuthorW`,
longer names truncated), the widest `+N` (drives the diff sub-alignment),
total diff width, and ci width.
`renderRow` pads each styled cell to its column width with `padCell`, which
appends **bg-aware** spaces — the selected-row background must reach the
column boundary (same lipgloss back-fill constraint as `st()`; see "Selected
row treatment").

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
of holding position. The visual counterpart is the `✓` approval marker (see
the palette note above) — same data source, so a PR showing the marker is
exactly one that survives `approvalMine`.

## Conventions

- **The changelog is the GitHub release, generated from commits.** There is no
  `CHANGELOG.md` — goreleaser builds the release notes from commit messages
  (`changelog: use: github` in `.goreleaser.yml`), grouped into Features / Fixes
  / Refactors. So the commit message *is* the changelog entry: write conventional
  commit subjects (`feat:`, `fix:`, `refactor:`) with a clear, user-facing
  summary of what changed. `docs:`/`test:`/`chore:` and merge commits are
  filtered out of the notes. Don't author a separate changelog file. To see the
  changelog, check the [releases page](https://github.com/ajardin/kiroshi/releases).
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
  masked. The steps are token → search → min-reviews → **refresh-interval
  (optional)** → **3 optional Jira steps** (base URL → email → token) →
  validating. The refresh-interval step takes a Go duration (`5m`); blank
  disables auto-refresh, a non-duration string keeps you on the step with an
  inline error (mirroring min-reviews validation). The Jira steps are
  skippable: a blank base URL jumps straight to validating, so users
  without Jira never touch email/token. The Jira token step is masked like
  the GitHub one. When Jira *is* configured, the validating step checks the
  credentials live (`jira.Client.Validate` → `GET /rest/api/3/myself`)
  after the GitHub token, via the injected `validateJira` func passed to
  `NewWizardModel`; a failure lands on the shared error step ("validation
  failed") and starting over re-walks the form with the typed values kept.
  The CLI gates the auto-fallback on `errors.Is(err,
  config.ErrNotFound)` so pipes/CI/`-no-tui` still error out instead of
  blocking on a prompt. Token validation and the wizard runner are injected
  through `WithTokenValidator` / `WithWizardRunner` (same test-seam idea as
  `WithTUIRunner`) so tests touch neither the network nor a real terminal.
  `config.Save` writes the file at mode `0600` (it holds both tokens).

## Workflow

Common commands (`make help` lists all targets):

- `make build` — compile to `./bin/kiroshi` (ldflags inject version/commit/date).
- `make test` — `go test -race -count=1 ./...` (the canonical test command).
- `make cover` / `make bench` — coverage report / benchmarks.
- `make fmt` / `make tidy` — `golangci-lint fmt` / `go mod tidy && verify`.
- `make all` — lint + test + build (the pre-push gate).

CLI flags: `-version`, `-verbose`, `-no-tui`, `-init`, `-config <path>`.

- `make lint` runs golangci-lint v2. The repo enables revive's `exported`,
  `var-naming`, and `package-comments` rules — every exported symbol and
  package needs a doc comment.
- `go test -race -count=1 ./...` is the test command. Keep it green.
- To eyeball the TUI: `rtk proxy go test -v -run TestPreview ./internal/tui`
  (rtk filters by default; `proxy` bypasses it so the rendered output
  reaches the terminal).
