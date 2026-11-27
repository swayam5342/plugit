<div align="center">

<img src="site/public/icon.svg" width="88" height="88" alt="PlugIt">

# PlugIt

**Plug any REST API into your AI.**

`plugit` turns an OpenAPI 3.0/3.1, Swagger 2.0 or Postman v2 spec into a curated, safe [MCP](https://modelcontextprotocol.io) server. You pick and polish the tools in a terminal UI, and PlugIt serves them over stdio to Claude Desktop, Claude Code, OpenAI Codex, Cursor, VS Code (GitHub Copilot), Windsurf, Gemini CLI, LM Studio and any other MCP client.

[![CI](https://img.shields.io/github/actions/workflow/status/swayam5342/plugit/ci.yml?style=flat-square&logo=github&logoColor=white&label=CI&labelColor=0d3a55&color=4cb3f0)](https://github.com/swayam5342/plugit/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/swayam5342/plugit?style=flat-square&logo=github&logoColor=white&labelColor=0d3a55&color=4cb3f0)](https://github.com/swayam5342/plugit/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.26%2B-4cb3f0?style=flat-square&logo=go&logoColor=white&labelColor=0d3a55)](go.mod)
[![Go Reference](https://img.shields.io/badge/pkg.go.dev-reference-4cb3f0?style=flat-square&logo=go&logoColor=white&labelColor=0d3a55)](https://pkg.go.dev/github.com/swayam5342/plugit)
[![Platform](https://img.shields.io/badge/platform-Windows%20%7C%20macOS%20%7C%20Linux-4cb3f0?style=flat-square&labelColor=0d3a55)](#install)
[![License](https://img.shields.io/badge/license-Apache--2.0-4cb3f0?style=flat-square&labelColor=0d3a55)](LICENSE)

[![M8ven Score](https://m8ven.ai/badge/mcp/swayam5342-plugit-1nub27)](https://m8ven.ai/mcp/swayam5342-plugit-1nub27?s=readme)
[![swayam5342/plugit MCP server](https://glama.ai/mcp/servers/swayam5342/plugit/badges/score.svg)](https://glama.ai/mcp/servers/swayam5342/plugit)

[**Website & live demo**](https://plugit.swayam.li/) &nbsp;&middot;&nbsp; [Install](#install) &nbsp;&middot;&nbsp; [Quick start](#try-it-in-30-seconds-plugit-run) &nbsp;&middot;&nbsp; [Commands](#commands) &nbsp;&middot;&nbsp; [Security](#security-model)

<br>

![PlugIt: add an API, pick and inspect tools, lint, try a call, install into Claude Desktop](demo/demo.gif)

</div>

<br>

## Table of Contents

| | |
| :--- | :--- |
| **Overview** | [Why PlugIt](#why-plugit) |
| **Setup** | [Install](#install) &middot; [Try it in 30 seconds](#try-it-in-30-seconds-plugit-run) &middot; [Curate and install](#curate-and-install-add-install) &middot; [Supported clients](#supported-clients) |
| **Configure** | [Policy modes](#policy-modes) &middot; [OAuth2](#oauth2) &middot; [Over HTTP instead of stdio](#over-http-instead-of-stdio) |
| **Reference** | [Commands](#commands) &middot; [Security model](#security-model) |
| **Release** | [Verifying a release](#verifying-a-release) &middot; [Build from source](#build-from-source) &middot; [License](#license) |

<br>

## Why PlugIt

| | | |
| :---: | :--- | :--- |
| 1 | **Local only** | One binary. No accounts, no hosted backend, no LLM calls, no telemetry. |
| 2 | **Safe by default** | Read-only mode, private-network blocking, credentials bound to the base URL you pin, secrets only from the OS keyring or env vars, and redaction everywhere. |
| 3 | **Curated** | Pick, rename and describe tools in a terminal UI with a live token budget, so a 1,000-operation API doesn't flood the model's context. Keep only the response fields you need, and lint every description for instructions hidden in a spec. |
| 4 | **Observable** | A live call inspector, per-tool stats and a one-shot `try` command, all with secrets masked. |

> [!NOTE]
> **Status:** v0.1.0, the first release. The CLI and the `plugit.yaml` format may still change
> before 1.0; config changes come with a version bump and an automatic migration.

<br>

## Install

| Method | How |
| :--- | :--- |
| **Go** | `go install github.com/swayam5342/plugit/cmd/plugit@latest` |
| **Prebuilt binary** | Download the archive for your platform from the [latest release](https://github.com/swayam5342/plugit/releases/latest) and put `plugit` on your `PATH`. Builds exist for Windows, macOS and Linux on amd64 and arm64. |

```sh
go install github.com/swayam5342/plugit/cmd/plugit@latest
```

`go install` needs Go (any recent version fetches the 1.26.7 toolchain plugit asks for) and puts
`plugit` in `~/go/bin` (`%USERPROFILE%\go\bin` on Windows), which should be on your `PATH`.

> [!TIP]
> Release archives on GitHub come with SHA-256 checksums, a cosign signature, SBOMs and
> build provenance. See [Verifying a release](#verifying-a-release).

<br>

## Try it in 30 seconds: `plugit run`

`plugit run` serves a spec with nothing saved. It's read-only (GET, HEAD and OPTIONS only)
and refuses to start if the selection is bigger than `--max-tools` (40 by default).

Add it to your MCP client's config, for example Claude Desktop's `claude_desktop_config.json`:

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

| Flag | Use |
| :--- | :--- |
| `--tags`, `--methods`, `--paths` | Narrow big APIs (`--paths` takes a prefix or glob). |
| `--header "X-Api-Key=env:PETS_KEY"` | Pass a credential as a reference, never a value: set `"env": {"PETS_KEY": "…"}` in the client config, or use `keyring:service/key`. |
| `--base-url` | Required whenever a credential is set, so a spec can't point your key at another host. |

<br>

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

Already have a working `plugit run` command line? Turn it into a saved config:

```sh
plugit add pets https://petstore.swagger.io/v2/swagger.json --from-run --tags pet --base-url https://petstore.swagger.io/v2
```

### Supported clients

| `--client` | Where the entry goes |
| :--- | :--- |
| `claude-desktop` | `claude_desktop_config.json` |
| `claude-code` | runs `claude mcp add --scope user …` |
| `codex` | runs `codex mcp add …` (OpenAI Codex CLI, IDE extension and app share it) |
| `cursor` | `~/.cursor/mcp.json` |
| `vscode` (or `copilot`) | VS Code's user `mcp.json` |
| `windsurf` | `~/.codeium/windsurf/mcp_config.json` |
| `gemini` | `~/.gemini/settings.json` |
| `lm-studio` | `~/.lmstudio/mcp.json` |

> [!NOTE]
> For JSON configs, `install` shows a diff, backs the file up and asks before writing. Only
> plugit's own entry changes, and it never contains a secret. Claude Code and Codex own their
> configs, so plugit runs their CLI instead, or prints the command if the CLI isn't on PATH.
> Any other stdio MCP client works too: point it at `plugit serve <name>`.

<br>

## Policy modes

| `policy.mode` | Read tools (GET, HEAD, OPTIONS) | Write tools (POST, PUT, PATCH, DELETE) |
| :--- | :--- | :--- |
| `read-only` (default) | offered | not offered; the model never sees them |
| `confirm-writes` | offered | offered; the client asks you before each call (refused if it can't ask) |
| `full` | offered | offered and run without asking |

Switch with `m` in the picker (`plugit add`, or `plugit edit <name>` later). Moving to `full`
takes a second press. Then reconnect your client (in Claude Code, `/mcp`); no reinstall is
needed, because the client's entry is just `plugit serve <name>`.

> [!TIP]
> Per tool, `p` adds `confirm` or `deny` on top of the API mode. These can only make a tool stricter.

### OAuth2

APIs that use OAuth2 get `auth.type: oauth2` in their `plugit.yaml`:

<details open>
<summary><b>plugit.yaml</b></summary>

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

</details>

Then run `plugit auth <name>`:

| Flow | What happens |
| :--- | :--- |
| `device_code` | Shows a URL and a code to approve in the browser, and keeps the refresh token in the OS keyring. |
| `client_credentials` | Stores the client secret and checks it. |

Access tokens stay in memory, go only to the pinned base URL, and are redacted everywhere.

### Over HTTP instead of stdio

For clients that connect to a URL, serve on loopback:

```sh
plugit serve github --http :8931
#   listening on http://127.0.0.1:8931/mcp; the token is in the file it names
claude mcp add --transport http github http://127.0.0.1:8931/mcp \
  --header "Authorization: Bearer $(cat <token file>)"
```

> [!IMPORTANT]
> Only loopback addresses are accepted, every request needs the token, and browser requests
> from other origins are refused. `--new-token` replaces the token.

<br>

## Commands

| Command | What it does |
| :--- | :--- |
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

Each API has a hand-editable `apis/<name>/plugit.yaml`; unknown keys and literal secrets are rejected.
Configs live in your OS config directory, or wherever `--config-dir` points:

| OS | Config directory |
| :--- | :--- |
| Windows | `%AppData%\plugit` |
| macOS | `~/Library/Application Support/plugit` |
| Linux | `~/.config/plugit` |

<br>

## Security model

PlugIt treats the spec, the model's tool arguments and the API's responses as untrusted.

| Guard | What it means |
| :--- | :--- |
| **Read-only by default** | Write tools need `policy.mode: confirm-writes` or `full`; per-tool `confirm` and `deny` can only tighten that. Change the mode with `m` in `plugit add` or `plugit edit <name>` (switching to `full` asks you to press `m` twice), then reconnect your client; no reinstall is needed. |
| **The host never comes from the model or the spec** | You pin the `endpoint`; credentials are attached only to requests for exactly that origin; redirects stay on it. |
| **SSRF guard** | The resolved IP is checked at connect time: loopback and private ranges need `allow_private: true`, and cloud metadata addresses are always blocked. |
| **No credentials over plain http, no risky paths** | A config that would send a credential to a remote host over `http://` doesn't load, and spec paths with spaces, `..` or backslashes are skipped. `network.allow_insecure: true` (`--allow-insecure`) is the one switch for both. |
| **Secrets** | Come from the OS keyring or env vars only, never from config files or the command line, and are redacted (raw, percent-encoded and base64) from logs, errors and every tool result. |
| **Strict arguments** | Validated against the schema; unknown arguments, `..` path segments and CR/LF in headers are rejected; auth and hop-by-hop headers are never tool parameters. |
| **Isolated spec parsing** | Untrusted specs are parsed in a child process with limits on size, `$ref` depth, aliases and time; external `$ref`s are never fetched. |

> [!WARNING]
> Report vulnerabilities privately: see [SECURITY.md](SECURITY.md).

<br>

## Verifying a release

```sh
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/swayam5342/plugit/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --ignore-missing -c checksums.txt
gh attestation verify plugit_<version>_<os>_<arch>.tar.gz --repo swayam5342/plugit
```

<br>

## Build from source

```sh
go build -o plugit ./cmd/plugit
go test -race ./...
```

<br>

## License

[Apache-2.0](LICENSE)
