# Computer access

Run the Windows agent, turn it on, and copy a service URL and password into your MCP client. The local Gio desktop app manages built-in PowerShell MCP services. Cloudflare Quick Tunnel starts on the PC without a Cloudflare account, domain, or hosted relay.

Cloudflare assigns a new hostname whenever the tunnel restarts. Service passwords remain saved until you regenerate them. Each service has its own URL path and password.

## Install on Windows

1. Install PowerShell 7 if it is missing.
2. Run `computer-access-setup.exe`. It installs for your Windows account and includes cloudflared.
3. Open **Computer access** from the Start menu.
4. Copy the service URL and token into your MCP client. Select **Streamable HTTP** and **Bearer Token**.

The installer offers **Start at login**. Uninstall through Windows Installed apps. Quit the agent from its tray menu before upgrading or uninstalling. Saved configuration and credentials remain in your user configuration directory.

Closing the window keeps the agent in the system tray without a taskbar entry. Open the tray icon to manage services, turn access off, or quit. Opening the executable again brings up the existing agent's window.

## Manage services

- Select **+ Add** to create another PowerShell MCP service.
- Edit a service name directly in its panel, then select **Save** or **Cancel**.
- Turn each service on or off independently, or use the agent switch to control all services.
- Regenerate a password or URL independently. Deleting a service revokes both.

Every service exposes the built-in `powershell` tool. Local HTTP reverse proxy services are no longer supported. Upgrading removes saved proxy services and preserves PowerShell services and their credentials.

PowerShell commands run with the account that launches the agent. MCP instructions direct clients to perform all PC operations through the PowerShell tool.

## Build from source

On Windows, install Go 1.25 or later. Build with Go:

```powershell
	go build -o dist/computer-access-mcp.exe .
```

For a desktop executable without a console, run `make build`. The checked-in Windows resource supplies the icon; normal builds need no resource compiler.

## Build the installer

Install GNU Make, [Inno Setup 6](https://jrsoftware.org/isinfo.php), and cloudflared on the build PC. Then run:

```powershell
	make installer
```

The output is `dist/computer-access-setup.exe`. It contains the desktop agent, cloudflared, a Start menu shortcut, and the standard Inno Setup uninstaller. PowerShell 7 remains a runtime prerequisite.

The Makefile finds cloudflared on PATH and defaults to Inno Setup's standard installation directory. Override either path when needed:

```powershell
	make installer ISCC="C:/Tools/Inno/ISCC.exe" CLOUDFLARED="C:/Tools/cloudflared.exe"
```

`make test` runs Go tests and vet. `make icons` regenerates the icon and Windows resources after artwork or version changes; it requires GNU windres. Edit the application version in `assets/app.rc`, then run `make icons`. The installer reads that version from the built EXE.

The installer definition is [installer/setup.iss](installer/setup.iss). There are no PowerShell build, install, or uninstall scripts.

## Use an optional hosted relay

The relay is configured for Railway with the root `railway.json`. Attach a persistent volume at `/data`, generate a public domain, and redeploy. See [Railway deployment](docs/deploy.md).

On the agent, open **Settings** and select **Hosted relay**. Enter the Railway HTTPS origin and optional enrollment key. Relay URLs stay assigned across restarts until regenerated.

For a local relay during development:

```powershell
.\dist\computer-access-mcp.exe relay
.\dist\computer-access-mcp.exe agent -relay http://127.0.0.1:8788
```

## Verify the project

```powershell
go test ./...
go vet ./...
```

The tests cover PowerShell through direct and relay gateways, independent services, credential revocation, restart persistence, proxy removal, request cancellation, and authentication. PowerShell tests require `pwsh`. Set `TEST_CLOUDFLARE=1` to include the live Cloudflare test.

See the [reference](docs/reference.md) for flags and limits. The console-only temporary server is available with `computer-access-mcp quick`; see [quick mode](docs/quick-mode.md).
