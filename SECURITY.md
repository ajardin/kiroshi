# Security Policy

## Supported versions

kiroshi is distributed as tagged releases. Security fixes are applied to the
latest release; please upgrade to the most recent version before reporting.

## Reporting a vulnerability

Please **do not** open a public issue for security vulnerabilities.

Report privately through GitHub's
[private vulnerability reporting](https://github.com/ajardin/kiroshi/security/advisories/new)
(Security → Advisories → Report a vulnerability), or by email to
**info@ajardin.fr**.

Include enough detail to reproduce the issue. You can expect an initial
acknowledgement within a few days. Once a fix is available, a patched release
is tagged and the advisory published.

## Handling of credentials

kiroshi reads a GitHub personal access token from the `GITHUB_TOKEN`
environment variable or the `github_token` config field, and optionally a
Jira API token (`JIRA_API_TOKEN` / `jira_token`). Tokens are redacted from
structured logs (`config.Config.LogValue`) and never logged. The only place
kiroshi ever writes them is the config file created at your request by
`kiroshi -init`, with file mode 0600. If you believe a code path leaks a
token, treat it as a security issue and report it through the channels above.
