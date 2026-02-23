# mcpshim

`mcpshim` is a daemon + CLI bridge that turns remote MCP servers into local command workflows.

Website: https://mcpshim.dev
Repository: https://github.com/mcpshim/mcpshim

- `mcpshimd` keeps the Unix socket and MCP registry alive
- `mcpshim` configures servers and invokes tools
- server registration is config-driven
- aliases let you call `notion <tool> --flag value` style commands

## The problem

Remote MCP servers are useful, but each one can require different auth patterns, transport details, and invocation conventions. Wiring all of that directly into every script or agent loop makes command workflows brittle.

There is also a context pollution problem for LLM agents: every additional MCP tool definition consumes prompt context before any real work happens.

## The solution

`mcpshimd` centralizes MCP lifecycle concerns (connection/session, auth flow, discovery, retries), while `mcpshim` provides a consistent CLI surface over a Unix socket.

Instead of pushing endless raw MCP tool metadata into the model context, `mcpshim` registers MCP capabilities as simple local commands. Agents can invoke only what they need, when they need it, with lower context overhead.

## Quick start

```bash
cp configs/mcpshim.example.yaml ~/.config/mcpshim/config.yaml
mcpshimd
mcpshim servers
mcpshim tools
mcpshim call --server notion --tool search --query "roadmap"
```

Path defaults:

| Resource | Default Location                    | Override                        |
| -------- | ----------------------------------- | ------------------------------- |
| Config   | `~/.config/mcpshim/config.yaml`     | `--config`, `$MCPSHIM_CONFIG`   |
| Socket   | `$XDG_RUNTIME_DIR/mcpshim.sock`     | `mcpshimd --socket ...`         |
| Database | `~/.local/share/mcpshim/mcpshim.db` | `server.db_path` in YAML config |

All paths follow XDG defaults where applicable.

Install from source:

```bash
go install github.com/mcpshim/mcpshim/cmd/mcpshimd@latest
go install github.com/mcpshim/mcpshim/cmd/mcpshim@latest
```

Validate config:

```bash
mcpshim validate
mcpshim validate --config /path/to/config.yaml
```

Daemon flags:

| Flag        | Description               |
| ----------- | ------------------------- |
| `--config`  | Path to config YAML       |
| `--socket`  | Override unix socket path |
| `--debug`   | Enable debug logging      |
| `--version` | Print version and exit    |

## Register MCPs

```bash
mcpshim add --name notion --alias notion --transport http --url https://example.com/mcp
mcpshim set auth --server notion --header "Authorization=Bearer $NOTION_MCP_TOKEN"
mcpshim reload
```

OAuth-only MCPs can be configured with URL only:

```bash
mcpshim add --name notion --alias notion --transport http --url https://mcp.notion.com/mcp
```

When a server returns `401` and no `Authorization` header is configured, `mcpshimd` automatically attempts OAuth:

- starts a temporary local callback server
- opens the authorization URL in your browser
- exchanges code for token and saves it in SQLite (`oauth_tokens` table)
- retries the MCP request automatically

You can pre-authorize explicitly:

```bash
mcpshim login --server notion
```

For cross-device login (browser on a different machine):

```bash
mcpshim login --server notion --manual
```

This prints the authorization URL. Open it anywhere, complete auth, then paste the final redirect URL (or just the `code`) back into the CLI prompt.

## Dynamic flags

CLI flags are converted automatically to MCP tool arguments:

```bash
mcpshim call --server notion --tool search --query "projects" --limit 10 --archived false
```

## Call history

Every `mcpshim call` is recorded by `mcpshimd` with timestamp, server/tool, args, status, and duration.

History rows are stored in SQLite (`call_history` table).

```bash
mcpshim history
mcpshim history --server notion --limit 20
mcpshim history --server notion --tool search --limit 100
```

> Tip: JSON output is automatic when stdout is not a terminal. Use `--json` to force JSON output in interactive sessions.

## IPC protocol

`mcpshim` talks to `mcpshimd` over a Unix socket using JSON messages with an `action` field.

```json
{"action":"status"}
{"action":"servers"}
{"action":"tools","server":"notion"}
{"action":"inspect","server":"notion","tool":"search"}
{"action":"call","server":"notion","tool":"search","args":{"query":"roadmap"}}
{"action":"history","server":"notion","limit":20}
{"action":"add_server","name":"notion","alias":"notion","url":"https://mcp.notion.com/mcp","transport":"http"}
{"action":"set_auth","name":"notion","headers":{"Authorization":"Bearer ..."}}
{"action":"reload"}
```

## Lightweight aliases

Generate shell functions:

```bash
eval "$(mcpshim script)"
notion search --query "projects" --limit 10
```

Install executable wrapper scripts instead:

```bash
mcpshim script --install --dir ~/.local/bin
notion search --query "projects" --limit 10
```
