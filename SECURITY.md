# Security Policy

PlugIt handles API credentials and lets an AI agent call real APIs on your behalf, so we
take security reports seriously.

## Supported versions

Only the latest minor release receives security fixes.

## Reporting a vulnerability

**Please don't open a public issue.** Report it privately through
[GitHub Security Advisories](https://github.com/swayam5342/plugit/security/advisories/new).

Include what you can of:

- the affected version (`plugit version`) and OS,
- a description of the issue and its impact,
- steps or a minimal spec, config or fixture that reproduces it.

We aim to acknowledge reports within 7 days. We follow a **90-day coordinated
disclosure** window, from the report date until public disclosure. We'll agree on an
earlier date with you if a fix ships sooner.

## Scope

In scope, among other things:

- leaks of credentials or secrets (logs, errors, TUI, client configs, process listings),
- SSRF or private-network access while `allow_private` is `false`,
- credentials sent to an origin other than the pinned `base_url`,
- external `$ref` resolution or resource-exhaustion problems from a hostile spec,
- argument injection (path traversal, header CR/LF, unknown argument forwarding),
- bypasses of read-only mode or per-tool `deny`/`confirm` policy.

Out of scope: problems that need an attacker who already runs code as your user, and
the behavior of the upstream APIs you connect PlugIt to.
