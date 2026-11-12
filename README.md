# PlugIt

> Turn any REST API into a curated, safe MCP server from your terminal.

PlugIt reads an OpenAPI 3.0/3.1 or Swagger 2.0 spec and serves the operations you pick as
[MCP](https://modelcontextprotocol.io) tools over stdio, for clients such as Claude Desktop,
Claude Code, Cursor and VS Code.

- **Local only.** One binary. No accounts, no hosted backend, no LLM calls, no telemetry.
- **Safe by default.** Read-only mode, private-network blocking, origin-bound credentials
  from the OS keyring or env vars, and redacted logs.
- **Curated.** Pick, rename and describe tools in a terminal UI with a live token budget.

> **Status:** early development (pre-v0.1.0). Nothing here is usable yet.

## Build

```sh
go build ./cmd/plugit
./plugit version
```

## License

[Apache-2.0](LICENSE)
