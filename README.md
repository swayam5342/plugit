# PlugIt

> Turn any REST API into a curated, safe MCP server from your terminal.

[![CI](https://github.com/swayam5342/plugit/actions/workflows/ci.yml/badge.svg)](https://github.com/swayam5342/plugit/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/swayam5342/plugit)](https://github.com/swayam5342/plugit/releases/latest)
[![Go Reference](https://pkg.go.dev/badge/github.com/swayam5342/plugit.svg)](https://pkg.go.dev/github.com/swayam5342/plugit)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

**Website and live demo:** [swayam5342.github.io/plugit](https://swayam5342.github.io/plugit/)

![PlugIt: add an API, pick and inspect tools, lint, try a call, install into Claude Desktop](demo/demo.gif)

PlugIt reads an OpenAPI 3.0/3.1 or Swagger 2.0 spec (or a Postman v2 collection) and serves the operations you pick as
[MCP](https://modelcontextprotocol.io) tools over stdio, for Claude Desktop, Claude Code,
OpenAI Codex, Cursor, VS Code (GitHub Copilot), Windsurf, Gemini CLI, LM Studio and any other
stdio MCP client.

- **Local only.** One binary. No accounts, no hosted backend, no LLM calls, no telemetry.
- **Safe by default.** Read-only mode, private-network blocking, credentials bound to the
  base URL you pin, secrets only from the OS keyring or env vars, and redaction everywhere.
- **Curated.** Pick, rename and describe tools in a terminal UI with a live token budget, so
  a 1,000-operation API doesn't flood the model's context. Keep only the response fields
  you need, and lint every description for instructions hidden in a spec.
- **Observable.** A live call inspector, per-tool stats and a one-shot `try` command, all
  with secrets masked.

> **Status:** v0.1.0, the first release. The CLI and the `plugit.yaml` format may still change
> before 1.0; config changes come with a version bump and an automatic migration.

## Install

```sh
go install github.com/swayam5342/plugit/cmd/plugit@latest
```

This needs Go (any recent version fetches the 1.26.7 toolchain plugit asks for) and puts
`plugit` in `~/go/bin` (`%USERPROFILE%\go\bin` on Windows), which should be on your `PATH`.
Without Go, download the archive for your platform from the
[latest release](https://github.com/swayam5342/plugit/releases/latest) and put `plugit` on
your `PATH`. Builds exist for Windows, macOS and Linux on amd64 and arm64.

Release archives on GitHub come with SHA-256 checksums, a cosign signature, SBOMs and
build provenance. See [Verifying a release](#verifying-a-release).

## Try it in 30 seconds: `plugit run`

`plugit run` serves a spec with nothing saved. It's read-only (GET, HEAD and OPTIONS only)
and refuses to start if the selection is bigger than `--max-tools` (40 by default).

Add it to your MCP client's config, for example Claude Desktop's
`claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "petstore": {
      "command": "/full/path/to/plugit",
      "args": ["run", "https://petstore.swagger.io/v2/swagger.json"]
    }
  }
}
```

Restart the client, and ask Claude to "find available pets using petstore".

- Narrow big APIs with `--tags`, `--methods` and `--paths` (prefix or glob).
- Credentials are references, never values: `--header "X-Api-Key=env:PETS_KEY"` with
  `"env": {"PETS_KEY": "…"}` in the client config, or `keyring:service/key`. Whenever a
  credential is set, `--base-url` is required too, so a spec can't point your key at
  another host.

## Curate and install: `add`, `install`

```sh
plugit add github --prefix gh https://raw.githubusercontent.com/github/rest-api-description/main/descriptions/api.github.com/api.github.com.json
#   confirm the base URL to pin, then pick tools in the terminal UI:
#   space toggle · / filter · r rename · e describe · p tool policy · m API mode · s save
plugit auth github                          # store the token in the OS keyring (hidden input)
plugit try github "GET /repos/{owner}/{repo}/issues" --arg owner=me --arg repo=x   # one test call, secrets redacted
plugit install github                       # pick clients from a list (detected ones are marked)
plugit install github --client codex,vscode # or name them
```

| `--client` | Where the entry goes |
|---|---|
| `claude-desktop` | `claude_desktop_config.json` |
| `claude-code` | runs `claude mcp add --scope user …` |
| `codex` | runs `codex mcp add …` (OpenAI Codex CLI, IDE extension and app share it) |
| `cursor` | `~/.cursor/mcp.json` |
| `vscode` (or `copilot`) | VS Code's user `mcp.json` |
| `windsurf` | `~/.codeium/windsurf/mcp_config.json` |
| `gemini` | `~/.gemini/settings.json` |
| `lm-studio` | `~/.lmstudio/mcp.json` |

For JSON configs, `install` shows a diff, backs the file up and asks before writing. Only
plugit's own entry changes, and it never contains a secret. Claude Code and Codex own their
configs, so plugit runs their CLI instead, or prints the command if the CLI isn't on PATH.
Any other stdio MCP client works too: point it at `plugit serve <name>`.

### Policy modes

| `policy.mode` | Read tools (GET, HEAD, OPTIONS) | Write tools (POST, PUT, PATCH, DELETE) |
|---|---|---|
| `read-only` (default) | offered | not offered; the model never sees them |
| `confirm-writes` | offered | offered; the client asks you before each call (refused if it can't ask) |
| `full` | offered | offered and run without asking |

Switch with `m` in the picker (`plugit add`, or `plugit edit <name>` later). Moving to `full`
takes a second press. Then reconnect your client (in Claude Code, `/mcp`); no reinstall is
needed, because the client's entry is just `plugit serve <name>`. Per tool, `p` adds
`confirm` or `deny` on top, which can only make a tool stricter.

Already have a working `plugit run` command line? Turn it into a saved config:

```sh
plugit add pets https://petstore.swagger.io/v2/swagger.json --from-run --tags pet --base-url https://petstore.swagger.io/v2
```

### OAuth2

APIs that use OAuth2 get `auth.type: oauth2` in their `plugit.yaml`:

```yaml
auth:
  type: oauth2
  flow: device_code              # or client_credentials
  token_url: https://auth.example.com/oauth/token
  device_url: https://auth.example.com/oauth/device/code
  client_id: my-client-id
  scopes: [read]
  # secret: keyring:plugit/my-client   # the client secret, required for client_credentials
```

Then `plugit auth <name>`: for the device flow it shows a URL and a code to approve in the
browser and keeps the refresh token in the OS keyring; for client credentials it stores the
client secret and checks it. Access tokens stay in memory, go only to the pinned base URL, and
are redacted everywhere.

### Over HTTP instead of stdio

For clients that connect to a URL, serve on loopback:

```sh
plugit serve github --http :8931
#   listening on http://127.0.0.1:8931/mcp; the token is in the file it names
claude mcp add --transport http github http://127.0.0.1:8931/mcp \
  --header "Authorization: Bearer $(cat <token file>)"
```

Only loopback addresses are accepted, every request needs the token, and browser requests
from other origins are refused. `--new-token` replaces the token.

## Commands

| Command | What it does |
|---|---|
| `plugit run <spec> [flags]` | Serve a spec on stdio with nothing saved (read-only by default) |
| `plugit add <name> <spec>` | Fetch, pin the base URL, pick tools, save |
| `plugit edit <name> [--toolset T]` | Reopen the picker |
| `plugit auth <name>` | Store or rotate the credential in the OS keyring |
| `plugit try <name> <tool> [--arg k=v]` | One call, printing the redacted request and the result (no `--arg` on a terminal: a Try it form) |
| `plugit logs [name]` | Live inspector of the calls MCP clients made (`--plain`, `-f`, `--json` when piped) |
| `plugit stats [name]` | Calls, error rate, p50/p95 latency and token budget per tool |
| `plugit lint <name>` | Quality and safety checks, including hidden instructions in descriptions (`--json`, exit 1 on errors) |
| `plugit serve <name> [--toolset T]` | The MCP server (what clients run) |
| `plugit install / uninstall <name> [--client C,…]` | Add to or remove from one or more MCP clients |
| `plugit sync <name>` | Re-fetch the spec, review what changed, adopt it (overrides kept, base URL never changed) |
| `plugit list`, `plugit remove <name>` | Manage saved APIs |
| `plugit doctor` | Check configs, credentials and client installs, and flag relaxed security settings |

Configs live in your OS config directory (`%AppData%\plugit`, `~/Library/Application
Support/plugit` or `~/.config/plugit`), or wherever `--config-dir` points. Each API has a
hand-editable `apis/<name>/plugit.yaml`; unknown keys and literal secrets are rejected.

## Security model

PlugIt treats the spec, the model's tool arguments and the API's responses as untrusted.

- **Read-only by default.** Write tools need `policy.mode: confirm-writes` or `full`; per-tool
  `confirm` and `deny` can only tighten that. Change the mode with `m` in `plugit add` or
  `plugit edit <name>` (switching to `full` asks you to press `m` twice), then reconnect your
  client; no reinstall is needed.
- **The host never comes from the model or the spec.** You pin `base_url`; credentials are
  attached only to requests for exactly that origin; redirects stay on it.
- **SSRF guard.** The resolved IP is checked at connect time: loopback and private ranges
  need `allow_private: true`, and cloud metadata addresses are always blocked.
- **Secrets** come from the OS keyring or env vars only, never from config files or the command
  line, and are redacted (raw, percent-encoded and base64) from logs, errors and every tool
  result.
- **Strict arguments.** Validated against the schema; unknown arguments, `..` path segments and
  CR/LF in headers are rejected; auth and hop-by-hop headers are never tool parameters.
- **Untrusted specs are parsed in a child process** with limits on size, `$ref` depth, aliases
  and time; external `$ref`s are never fetched.

Report vulnerabilities privately: see [SECURITY.md](SECURITY.md).

## Verifying a release

```sh
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/swayam5342/plugit/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --ignore-missing -c checksums.txt
gh attestation verify plugit_<version>_<os>_<arch>.tar.gz --repo swayam5342/plugit
```

## Build from source

```sh
go build -o plugit ./cmd/plugit
go test -race ./...
```

## License

[Apache-2.0](LICENSE)
