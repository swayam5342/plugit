# PlugIt

> Turn any REST API into a curated, safe MCP server from your terminal.

PlugIt reads an OpenAPI 3.0/3.1 or Swagger 2.0 spec and serves the operations you pick as
[MCP](https://modelcontextprotocol.io) tools over stdio, for clients such as Claude Desktop,
Claude Code and Cursor.

- **Local only.** One binary. No accounts, no hosted backend, no LLM calls, no telemetry.
- **Safe by default.** Read-only mode, private-network blocking, credentials bound to the
  base URL you pin, secrets only from the OS keyring or env vars, and redaction everywhere.
- **Curated.** Pick, rename and describe tools in a terminal UI with a live token budget, so
  a 1,000-operation API doesn't flood the model's context.

> **Status:** pre-release (v0.1.0 in progress). Expect rough edges and config changes.

## Install

```sh
# macOS (Homebrew)
brew install --cask swayam5342/tap/plugit

# Windows (Scoop)
scoop bucket add plugit https://github.com/swayam5342/scoop-bucket
scoop install plugit

# Anywhere else, from source (Go 1.26.7+), or download an archive from the GitHub releases
go install github.com/swayam5342/plugit/cmd/plugit@latest
```

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
#   space toggle · / filter · r rename · e describe · p policy · s save
plugit auth github                          # store the token in the OS keyring (hidden input)
plugit try github "GET /repos/{owner}/{repo}/issues" --arg owner=me --arg repo=x   # one test call, secrets redacted
plugit install github --client claude-desktop                  # or cursor, claude-code
```

`install` shows a diff of the client config, backs it up and asks before writing. Only
plugit's own entry changes, and it never contains a secret. For Claude Code it runs
`claude mcp add --scope user …` (or prints the command if the `claude` CLI isn't on PATH).

Already have a working `plugit run` command line? Turn it into a saved config:

```sh
plugit add pets https://petstore.swagger.io/v2/swagger.json --from-run --tags pet --base-url https://petstore.swagger.io/v2
```

## Commands

| Command | What it does |
|---|---|
| `plugit run <spec> [flags]` | Serve a spec on stdio with nothing saved (read-only by default) |
| `plugit add <name> <spec>` | Fetch, pin the base URL, pick tools, save |
| `plugit edit <name> [--toolset T]` | Reopen the picker |
| `plugit auth <name>` | Store or rotate the credential in the OS keyring |
| `plugit try <name> <tool> [--arg k=v]` | One call, printing the redacted request and the result |
| `plugit serve <name> [--toolset T]` | The MCP server (what clients run) |
| `plugit install / uninstall <name> --client C` | Add to or remove from Claude Desktop, Cursor or Claude Code |
| `plugit list`, `plugit remove <name>` | Manage saved APIs |
| `plugit doctor` | Check configs, credentials and client installs, and flag relaxed security settings |

Configs live in your OS config directory (`%AppData%\plugit`, `~/Library/Application
Support/plugit` or `~/.config/plugit`), or wherever `--config-dir` points. Each API has a
hand-editable `apis/<name>/plugit.yaml`; unknown keys and literal secrets are rejected.

## Security model

PlugIt treats the spec, the model's tool arguments and the API's responses as untrusted.

- **Read-only by default.** Write tools need `policy.mode: confirm-writes` or `full`; per-tool
  `confirm` and `deny` can only tighten that.
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
