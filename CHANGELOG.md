# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- **PR detail overlay** — the `d` key opens a centered panel on the selected
  PR showing its full description, the complete reviewer breakdown (approved /
  changes / commented / requested) and its CI · merge · Jira status. It's
  purely presentational — it reuses already-enriched data, so it issues no
  GitHub calls — and any key dismisses it. The description is wrapped and
  capped with a `… (N more lines)` indicator so a long body never dominates
  the panel.

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

[Unreleased]: https://github.com/ajardin/kiroshi/compare/v1.2.0...HEAD
[1.2.0]: https://github.com/ajardin/kiroshi/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/ajardin/kiroshi/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/ajardin/kiroshi/releases/tag/v1.0.0
