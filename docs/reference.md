# Agent and relay reference

## Commands

`computer-access-mcp` defaults to `agent` mode. The subcommands are `agent`, `relay`, and `quick`.

### Agent flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `-mode` | Saved mode, Cloudflare for new agents | `cloudflare` or `relay`. |
| `-cloudflared` | Auto-detect | cloudflared executable path. Searches beside the agent, then PATH. |
| `-relay` | Saved address or build-time default | Relay origin. Setting this selects relay mode unless `-mode` overrides it. HTTPS is required except on loopback. |
| `-config` | User config directory, `ComputerAccess/agent.dat` | Saved agent configuration. |
| `-port` | `17877` | Private control API port on `127.0.0.1`. |
| `-open` | `true` | Shows the local Gio desktop window. False starts in the tray without a window. |
| `-headless` | `false` | Runs without the desktop window or tray. |
| `-workdir` | Current user's home directory | Default PowerShell working folder. |
| `-pwsh` | `pwsh` | PowerShell 7 executable name or path. |
| `-computer-use` | Saved setting, `false` for new agents | Enables Cua tools for every service. Explicit `-computer-use=false` disables them. |
| `-cua-driver` | Saved path, otherwise `cua-driver` on PATH | Cua Driver executable. Supplying a path does not enable computer use. |

`COMPUTER_ACCESS_ENROLLMENT_KEY` supplies an optional relay enrollment key. The key is saved in the private agent configuration. The settings screen can also set it.

`main.defaultRelayURL` is a build-time string set with Go's `-ldflags -X` option. It provides the first-run relay address for distributed agents.

The Windows desktop app uses Gio. Closing its window destroys the main window while the agent and tray remain running. The tray Open action creates the window again. A private authenticated loopback API activates the existing window when another instance starts. The API checks HTTP Host and Origin and serves no management website.

Cloudflare mode runs a Quick Tunnel on the PC and maps `/s/<url-id>` to the built-in MCP server. Its hostname changes whenever the tunnel restarts. The PowerShell MCP server uses JSON responses over Streamable HTTP.

### Relay flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `-listen` | `0.0.0.0:$PORT` when set, otherwise `127.0.0.1:8788` | HTTP listener. Railway terminates public HTTPS. |
| `-public-url` | `RELAY_PUBLIC_URL`, otherwise `https://$RAILWAY_PUBLIC_DOMAIN` | Public relay origin. Required for a non-loopback listener. |
| `-data` | `$RAILWAY_VOLUME_MOUNT_PATH/registry.json`, otherwise `relay-data/registry.json` | Persistent device and route database. |
| `-max-devices` | `1000` | Maximum enrolled devices. |

`RELAY_ENROLLMENT_KEY` restricts enrollment of new devices when set. Existing devices authenticate with their own device secret. Without an enrollment key, any device can enroll until the device limit is reached. The relay has no account or sign-in requirement.

Explicit flags override these environment defaults. When `RAILWAY_SERVICE_ID` is set, the relay requires an attached Railway volume. The repository's `railway.json` configures the native Go build, relay start command, and `/healthz` deployment check. See [Railway deployment](deploy.md).

`GET /healthz` returns `ok`. Control endpoints register device manifests and open authenticated agent WebSocket connections. There is no public device-listing or device-management endpoint.

## Service identities and credentials

Each service has an internal ID, a public URL ID, a random password, an enabled state, and a built-in PowerShell MCP handler. The public address is `<tunnel-or-relay-origin>/s/<url-id>`.

The agent generates and saves service IDs and passwords before exposing services. In Cloudflare mode, only the hostname changes on a tunnel restart. In relay mode, the complete URL remains stable. It does not generate replacements on disconnects, failed enrollment, upgrades, or restarts. A corrupt saved configuration causes an error rather than creating replacement identities.

| Action | URL | Password |
| --- | --- | --- |
| Agent or service off/on | Path preserved; Cloudflare hostname may change | Preserved |
| Agent or relay restart | Relay URL preserved; Cloudflare hostname changes on tunnel restart | Preserved |
| Edit service name | Preserved | Preserved |
| Regenerate password | Preserved | Replaced |
| Regenerate URL | Replaced | Preserved |
| Delete service | Revoked | Revoked |

Clients authenticate with `Authorization: Bearer <service-password>`. Passwords in query strings are not accepted. Each service uses a separate password. The agent validates credentials. In relay mode, the relay validates them too. The agent also validates the current public URL ID, so a stale relay manifest cannot restore a revoked URL or password.

The configured relay origin is fixed after setup because it forms part of every public URL. Changing DNS or moving the relay server while retaining the same public origin does not change service URLs. Deleting the agent configuration discards its identities. Windows DPAPI configuration files are tied to the Windows user and machine; copying the file to a different PC does not migrate it.

## PowerShell services

All services expose the built-in PowerShell MCP server. The public service root maps to `/mcp`. The server uses the `powershell` tool and [execution behavior](quick-mode.md#powershell-tool). MCP instructions direct clients to use PowerShell for command-line work and Cua tools, when enabled, for graphical interaction.

Service names are editable directly in the service panel with Save changes and Cancel. Adding a service requires only a name. Local HTTP destinations and upstream bearer tokens are no longer supported. Existing saved HTTP proxy services are removed on upgrade without changing PowerShell service identities.

## Computer use with Cua

Computer use is an optional device-wide setting. It exposes Cua Driver tools through the same authenticated Streamable HTTP endpoint as PowerShell. Cua Driver is a separate installation; the installer does not bundle or update it. Settings accepts an executable path, without arguments. The agent launches it with the `mcp` argument.

The forwarding implementation connects to a local stdio MCP subprocess. It discovers tools, resources, and resource templates at startup, including paginated catalogs. Tool names receive the `cua_` prefix. Schemas, annotations, image content, structured results, and tool and protocol errors pass through. Resource URIs and contents retain their original values. Upstream instructions are included with an explanation of the tool prefix.

The upstream connection uses MCP `2025-06-18`. Bundled guides are forwarded through standard resource listing and reads when available. The skills extension, prompts, sampling, elicitation, progress notifications, resource subscriptions, and live catalog updates are not forwarded. Arbitrary MCP server configuration is not exposed in the UI.

One subprocess is shared by all services. Discovery starts lazily after authentication. Initialization has a 15-second deadline. Cua tool calls execute one at a time with a 120-second deadline, including queue time. PowerShell retains its separate concurrency limit. Named Cua sessions are shared upstream; separate service credentials do not isolate desktop state.

Changing the computer use setting or executable cancels the old subprocess and its calls. Stopping all services or quitting the app also cancels it. Revoking an individual service cancels that service's requests while other services keep their shared driver. Cua may connect to an existing desktop daemon; this app stops its MCP subprocess, not an independently managed Cua daemon.

If the subprocess exits, its tools and resources are rediscovered on the next request. Failed actions are never replayed. The assistant must inspect the current state before retrying an action whose result was lost. Failed initialization returns HTTP 503 and allows another attempt after two seconds. Disable computer use to restore the PowerShell-only endpoint if Cua is unavailable.

Reconnect the MCP client after enabling or disabling computer use, changing the driver, or upgrading it. Screenshot tasks require a client and model that accept MCP image content. The driver runs with the launching account's desktop permissions and requires a signed-in desktop session.

## Connections and limits

In hosted relay mode, the agent reconnects automatically with a backoff of up to 15 seconds. All services share one outbound WebSocket, with a separate multiplexed stream for each HTTP request. Updates synchronize over an authenticated HTTP control request without reconnecting unrelated services.

The limit is 32 services per device. Hosted relay limits are 16 concurrent public requests per device, 256 concurrent public requests per relay process, 16 MiB public request bodies, and 130 seconds per public request. HTTP responses stream through the relay, including SSE. Public WebSocket service upgrades return HTTP 501. The agent's own outbound WebSocket is supported.

Turning off a service, changing its credentials, or deleting it cancels its active executions. Renaming a service does not. Turning the agent off cancels all service executions and closes the outbound connection. Credentials remain saved. Quitting and restarting restores the saved on/off state.

The relay runs as one process with one active connection per device. A new connection replaces that device's previous connection. Multiple relay replicas require a shared registry and cross-node connection routing, which are not included.

## Storage and trust

The agent uses an OS file lock to prevent concurrent processes from modifying the same configuration. Configuration updates use a temporary file, a disk sync, and replacement of the previous file. Failed saves leave the previous in-memory configuration active.

On Windows, the entire private configuration is encrypted with current-user DPAPI. On Unix, it is a private file with mode `0600` inside a directory created with mode `0700`. The relay stores device credential hashes, service password hashes, public route IDs, enabled states, and device names. It does not store plaintext service passwords.

The relay terminates HTTPS and can see proxied requests, credentials presented by clients, and responses. Its operator is trusted. The application does not add end-to-end encryption through the relay or retain a command audit history.

PowerShell runs with the launching account's permissions. Model instructions do not provide a sandbox. Anyone with a PowerShell service URL and password can execute commands with that account's permissions. The installer does not grant administrator rights to the agent.
