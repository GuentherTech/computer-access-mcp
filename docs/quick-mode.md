# Quick mode reference

These settings apply to `computer-access-mcp quick`. Quick mode uses temporary Cloudflare URLs and saves nothing. The desktop agent saves its services and tokens, and can use a hosted relay for URLs that never change.

## Command-line flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `-port` | `0` | HTTP port on `127.0.0.1`. Zero selects a free port. |
| `-no-tunnel` | `false` | Runs the local HTTP server without starting cloudflared. |
| `-workdir` | `.` | Default working directory for commands. Must exist. |
| `-pwsh` | `pwsh` | PowerShell 7 executable name or path. |
| `-cloudflared` | `cloudflared` | Cloudflared executable name or path. |
| `-computer-use` | `false` | Adds Cua Driver tools and resources alongside PowerShell. |
| `-cua-driver` | `cua-driver` on PATH | Cua Driver executable path. Requires `-computer-use`. |

`COMPUTER_ACCESS_TOKEN` overrides the generated bearer token. The value must contain at least 32 characters and no whitespace. Generated tokens contain 32 cryptographically random bytes encoded as URL-safe base64. The server prints the token once at startup and does not persist it.

## MCP endpoint

The endpoint is `/mcp`. Every request requires `Authorization: Bearer <token>`. Tokens in query parameters are not accepted. Invalid or missing credentials return HTTP 401 with a bearer authentication challenge.

The server uses the [official Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk) with stateless Streamable HTTP and JSON responses. It does not create persistent MCP sessions or an SSE stream. Requests with an `Origin` header return HTTP 403. Request bodies are limited to 1 MiB.

## PowerShell tool

The built-in tool is `powershell`. Enabling `-computer-use` adds the forwarded `cua_` tools described in the [computer use reference](reference.md#computer-use-with-cua).

| Parameter | Required | Meaning |
| --- | --- | --- |
| `script` | Yes | PowerShell script, up to 24 KiB of UTF-8 text. |
| `working_directory` | No | Absolute working directory. Defaults to `-workdir`. |
| `timeout_seconds` | No | Integer from 1 to 120. Zero or omission selects 60 seconds. |

Example arguments:

```json
{
	"script": "Get-ChildItem -LiteralPath .; Get-Content -Raw -LiteralPath './README.md'",
	"working_directory": "C:\\CodingCool\\GuentherTech\\Projects\\computer-access-mcp",
	"timeout_seconds": 30
}
```

The response contains `stdout`, `stderr`, `exit_code`, `timed_out`, `truncated`, and `duration_ms`. It appears in both MCP structured content and a text content block. Failed commands set MCP `isError` to `true`.

Each call starts a fresh PowerShell 7 process with `-NoProfile` and `-NonInteractive`. Variables, directory changes, and shell state do not persist across calls. The default error action is `Stop`. Unhandled PowerShell errors return exit code 1. An explicit `exit` or a nonzero final native program exit code is reflected in `exit_code`.

The server captures up to 128 KiB for each of stdout and stderr, then drains and discards further output. The `truncated` field reports discarded output. Text is returned as UTF-8.

Up to four commands execute concurrently. Additional calls wait until a slot is free. The command timeout starts after a slot becomes available. On timeout or request cancellation, the server terminates the PowerShell process and its descendants. Windows uses `taskkill /T /F`. Unix uses a process group. Processes intentionally detached into another process group on Unix can outlive the call. `timed_out` is true only when `timeout_seconds` expires, not when the client disconnects or the service's credentials change.

A script can start a background process and exit. If that process shares the console, for example `Start-Process -NoNewWindow`, the call returns about 2 seconds after the script exits with the output captured up to that point. Later output from the background process is discarded. `Start-Process -WindowStyle Hidden` returns immediately.

The server removes `COMPUTER_ACCESS_TOKEN` from the command's environment. This does not isolate commands from the account or prevent them from accessing other secrets available to that account.

## Client instructions and permissions

The MCP initialization response directs clients to use `powershell` for command-line operations on this PC, including file reads, writes, searches, program execution, and system inspection. When enabled, the `cua_` tools provide graphical interaction.

These are instructions for the client model. They do not prevent a client from invoking its other tools. The server itself provides no separate file tools, filesystem sandbox, command allowlist, or per-command approval dialog. PowerShell runs with the permissions of the account that launched the server.

## Cloudflare behavior

The server starts `cloudflared tunnel` with an empty temporary config and `--no-autoupdate`. The empty config prevents an existing named-tunnel config from overriding the Quick Tunnel. Cloudflared rewrites the origin Host header to the local listener address so the SDK's localhost protection accepts tunneled requests. Cloudflared logs go to stderr. The server extracts the assigned `https://*.trycloudflare.com` hostname and prints its `/mcp` endpoint to stdout.

Startup fails if no hostname appears within 60 seconds. If cloudflared exits later, the server shuts down. Ctrl+C cancels active commands, stops cloudflared, and closes the HTTP listener. The server does not install a service or configure startup persistence.

[Cloudflare Quick Tunnels](https://developers.cloudflare.com/tunnel/get-started/quick-tunnels/) use temporary hostnames, require no account, and have no uptime guarantee. Quick Tunnels do not support SSE, which is why this server uses JSON responses. A client that requires a persistent SSE connection is incompatible with this transport configuration.

The client must be able to resolve the generated hostname. Some DNS resolvers block new or tunnel-related domains. A `no such host` error during the live test means the test PC's resolver cannot resolve the hostname. The optional `TEST_CLOUDFLARE_DNS` environment variable selects a DNS server for test HTTP requests only, for example `1.1.1.1`. It does not change system DNS settings or affect the server executable.
