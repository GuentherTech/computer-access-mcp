# Computer access

Computer access lets an AI assistant work on your Windows PC. It gives the assistant one tool, `powershell`, and the assistant uses it to read and edit files, run programs, and check on the machine.

It gives you a URL and a token. You paste both into any MCP client that supports Streamable HTTP with bearer token authentication, and the assistant can reach your PC from anywhere.

## Get started

1. Install [PowerShell 7](https://learn.microsoft.com/powershell/scripting/install/installing-powershell-on-windows) if you don't have it.
2. Run `computer-access-setup.exe`. It installs for your Windows account only, needs no admin rights, and includes cloudflared. Tick **Start at login** if you want the app running all the time.
3. Open **Computer access** from the Start menu. It creates your first service.
4. In your MCP client, add a server with the service's URL and token. Choose Streamable HTTP as the transport and Bearer Token as the authentication.

Closing the window keeps the app running in the system tray. Use the tray menu to reopen it, turn access off, or quit. Quit before you upgrade or uninstall.

## How you connect

The app offers two ways to reach your PC, and you pick one under **Settings**.

**Cloudflare tunnel.** This is the default and needs no setup. The app starts a free Cloudflare Quick Tunnel, with no account or domain. The downside is that Cloudflare assigns a new address every time the tunnel restarts, such as after a reboot, a dropped connection, or turning access off and on. When that happens, copy the new URL into your client. The token stays the same.

**Hosted relay.** Use our deployment at [computer-acesss.gtinc.ca](https://computer-acesss.gtinc.ca) for stable service URLs. You can also [deploy your own relay](docs/deploy.md).

### Connect to our hosted relay

1. Open **Settings** in the desktop app and select **Hosted relay**.
2. Set **Relay URL** to `https://computer-acesss.gtinc.ca`.
3. Leave **Enrollment key** blank. This deployment has no enrollment key configured.
4. Select **Save**, then **Start** if access is stopped.
5. Copy the service's URL and token into your MCP client using **Streamable HTTP** and **Bearer Token** authentication.

Each service still requires its generated bearer token. The enrollment key only controls registration of new devices.

## Services

A service is one MCP endpoint with its own URL and token. All services run PowerShell as you. Separate services let you give each assistant its own credentials, so you can cut one off without touching the others.

In the app you can add services, rename them, and turn each one on or off. **Stop** turns off all of them. Rotating a URL or token cuts off the old one immediately, and deleting a service revokes both.

## Limits

- Each call starts a fresh PowerShell process. Variables and the current folder don't carry over between calls.
- A call can run for at most 2 minutes. For longer work, the assistant starts a background process and checks on it later.
- Up to four commands run at once, across all services. Others wait their turn.
- The desktop app runs on Windows only.
- Nothing keeps a history of the commands that ran.

## Try it without installing

`computer-access-mcp quick` starts a temporary server in a console window. It prints a URL and token and stops when you press Ctrl+C. It saves nothing, and it needs cloudflared on your PATH. See [Quick mode](docs/quick-mode.md).

## More documentation

- [Reference](docs/reference.md) covers command-line flags, how credentials work, and limits.
- [Quick mode](docs/quick-mode.md) explains the `powershell` tool in detail.
- [Deploy the relay](docs/deploy.md) covers setting up a hosted relay on Railway.
- [Development](docs/development.md) covers building from source, building the installer, and running tests.
