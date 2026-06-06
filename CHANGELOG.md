# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- **Loading screen** — the dashboard now launches straight into a cyberpunk
  "decrypt" splash while the first scan runs in the background, so the terminal
  no longer looks frozen during the multi-second search and enrichment. The
  brand word resolves left-to-right out of scrambled glyphs and the dashboard
  replaces the splash the moment data lands. The TUI now also opens even when
  the search returns nothing (an empty dashboard) and surfaces a failed initial
  scan as a status line you can retry with `r`, instead of blocking before
  launch.
- **Age anti-forgetting nudge** — each row shows how long a PR has been open
  (time since it was created, not last updated), with the color escalating from
  muted to dim past 7 days and to yellow past 21 days, so PRs that have sat
  untouched draw the eye. The detail overlay shows the same age.
- **PR detail overlay** — the `d` key opens a centered panel on the selected
  PR showing its full description, the complete reviewer breakdown (approved /
  changes / commented / requested) and its CI · merge · Jira status. It's
  purely presentational — it reuses already-enriched data, so it issues no
  GitHub calls. Inside the overlay, `↑`/`↓` flip to the previous/next PR so you
  can review details without returning to the listing, `enter`/`o` opens the
  current PR in the browser, and any other key closes it. The description is
  wrapped and capped with a `… (N more lines)` indicator so a long body never
  dominates the panel.

### Changed
- **Simpler navigation** — moving the selection is now done with the `↑`/`↓`
  arrow keys; the `j`/`k` vi keys were removed. Jumping to the top/bottom of the
  list with `g`/`G` (or Home/End) still works.
- **Lighter Jira display** — the listing now shows only the Jira ticket's
  status (e.g. `In Review`), dropping the ticket number to cut noise on the
  row. The full ticket number and status moved into the detail overlay (`d`),
  where they get their own labelled `JIRA` line instead of being buried in the
  packed meta line.
- **Row layout polish** — the merge cell moved from a fixed column into the
  flowing tail, so a healthy PR no longer leaves a reserved gap on every clear
  row; indicator blocks are now joined by a uniform ` · ` separator and the
  author is set off from them by a wider gap, making the second line easier to
  scan.

## [1.3.0] - 2026-06-05

### Added
- **"Mine" pane** — the `tab` key toggles between incoming PRs (those waiting
  on others) and the PRs you authored, so you can check on your own work
  without leaving the dashboard.

### Changed
- **Narrow-terminal layout** — below the full four-card width the status cards
  fall back to a 2×2 grid and the header drops its status badges and clock to
  stay on one line, so the dashboard stays usable on slim terminals (with a
  "terminal too small" guard below the minimum).

## [1.2.0] - 2026-06-04

### Added
- **Merge-state column** — surfaces GitHub's `mergeable_state` on the row's
  second line: `conflict` in red when a PR is `dirty` (blocks merge like a
  failing build), a muted `behind` when the head branch trails its base.
  Every other state renders blank and the column collapses to zero width
  when no visible PR is flagged, so it costs nothing on an all-clean set.
- **Optional auto-refresh** — `refresh_interval` config (Go duration; `0` or
  absent disables) arms a `tea.Tick` that rescans through the same path as
  the `r` key, skipping when a scan is already in flight. The footer shows a
  cyan `● auto <interval>`; the setup wizard gained a matching optional step.

## [1.1.0] - 2026-06-02

### Added
- **Homebrew distribution** — goreleaser now publishes a cask to
  `ajardin/homebrew-tap`, so `brew install ajardin/tap/kiroshi` works. The
  post-install hook strips the macOS quarantine attribute from the binary.

## [1.0.0] - 2026-06-02

First stable release. kiroshi surfaces your GitHub pull requests as an
interactive terminal dashboard, classified by what's waiting on whom.

### Added
- **Pull request dashboard** — Bubble Tea TUI with a locked design system
  (see `CLAUDE.md`); plain-text fallback when stdout is not a TTY.
- **Review-state classification** — PRs bucketed into Waiting On You /
  Waiting On Others / Ready To Ship / In Flight from requested reviewers and
  submitted reviews; configurable `min_reviews` threshold.
- **Enriched rows** — per-PR diff stats (`+N -M`), aggregated CI status
  (failure > pending > success > none precedence), and an approval marker
  for PRs you've approved, laid out in scannable aligned columns.
- **Optional Jira integration** — resolves the issue key from a PR's branch,
  title, or body and shows the ticket status, colored by status category
  (Jira Cloud, REST v3). Degrades to an omitted cell when unconfigured or
  unreachable.
- **Setup wizard** — `kiroshi -init` (and the auto-fallback on a TTY with no
  config) walks through GitHub token, search query, min-reviews, and the
  optional Jira credentials, validating them live.
- **Interactive controls** — navigate (`j`/`k`), jump (`g`/`G`), open in
  browser (`o`/`enter`), rescan (`r`), text filter (`f`), sort toggle (`s`),
  approval filter (`a`), and a `?` keybindings overlay.
- **Pull request listing** — issues/search backed by the
  `advanced_search=true` flag so boolean expressions in the query work.
- **GitHub authentication** — PAT loaded from `GITHUB_TOKEN` or the TOML
  config, with a dedicated `ErrInvalidToken` for 401 responses.
- **Config loader** — TOML at the XDG default path, written at mode `0600`,
  with token redaction in structured logs.
- **Parallelised enrichment** — per-PR enrichment runs through an `errgroup`
  worker pool capped at 8, with fail-fast cancellation (Jira excepted: it
  degrades gracefully rather than failing the scan).

### Tooling
- goreleaser release pipeline (Linux/macOS/Windows × amd64/arm64 archives,
  checksums, GitHub changelog) triggered on `v*` tags.
- CI matrix across Linux, macOS, and Windows; `golangci-lint-action` pinned
  to v2.12.2 for local/CI parity; `.pre-commit-config.yaml` mirroring CI.
- README, LICENSE (MIT), CHANGELOG; `make bench` baseline
  (`BenchmarkSearchPullRequests_Enrichment`) and `internal/version` test
  coverage.

[Unreleased]: https://github.com/ajardin/kiroshi/compare/v1.3.0...HEAD
[1.3.0]: https://github.com/ajardin/kiroshi/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/ajardin/kiroshi/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/ajardin/kiroshi/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/ajardin/kiroshi/releases/tag/v1.0.0
