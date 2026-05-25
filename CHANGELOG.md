# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- README, LICENSE (MIT), CHANGELOG.
- `make bench` target and a `BenchmarkSearchPullRequests_Enrichment`
  baseline (50 PRs, simulated 5ms-per-call latency) so future enrichers
  can be compared against a known number.
- `internal/version` test coverage for both the `-ldflags` override and
  the default-vars fallback shape.
- `.pre-commit-config.yaml` mirroring the CI checks (golangci-lint,
  gofmt, `go mod tidy`).
- CI: matrix the `test` job across Linux, macOS, and Windows so every PR
  validates all three target platforms.

### Changed
- CI: pin `golangci-lint-action` to a specific version (v2.12.2) instead
  of `latest`, keeping local and CI linter behavior in sync.

### Refactored
- Centralised the GitHub 401 → `ErrInvalidToken` translation in a single
  `wrapAPIError` helper, removing six duplicated handler blocks in
  `internal/gh/client.go`.

## Released

Pre-release work shipped on `main` before the changelog existed:

- **Parallelised enrichment** — per-PR enrichment runs through an
  `errgroup` worker pool capped at 8, with fail-fast cancellation.
- **Diff stats** — `+N -M` rendered per row, sourced from
  `PullRequests.Get` (folded into the same call that fetches the head
  SHA needed for CI).
- **CI status** — head-SHA check runs are aggregated into a single
  `CIState` via `aggregateCheckRuns` (failure > pending > success >
  none precedence).
- **Review-state classification** — pull requests are bucketed into
  Waiting On You / Waiting On Others / Ready To Ship / In Flight based
  on requested reviewers and submitted reviews; the `min_reviews`
  threshold is configurable.
- **Pull request listing** — issues/search backed by the
  `advanced_search=true` flag so boolean expressions in the query work.
- **GitHub authentication** — PAT loaded from `GITHUB_TOKEN` or the
  TOML config, with a dedicated `ErrInvalidToken` for 401 responses.
- **Config loader** — TOML at the XDG default path, with token
  redaction in structured logs.
- **Visual shell** — Bubble Tea TUI with a locked design system (see
  `CLAUDE.md`), plain-text fallback when stdout is not a TTY.
