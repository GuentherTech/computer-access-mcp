# Development

## Build

Install Go 1.25 or later on Windows, then build:

```powershell
go build -o dist/computer-access-mcp.exe .
```

That build opens a console window. For the desktop build without a console, run `make build`. The checked-in Windows resource file supplies the icon, so normal builds need no resource compiler.

## Build the installer

You need GNU Make, [Inno Setup 6](https://jrsoftware.org/isinfo.php), and cloudflared on the build PC. Then run:

```powershell
make installer
```

This writes `dist/computer-access-setup.exe`. It contains the desktop app, cloudflared, a Start menu shortcut, and the standard Inno Setup uninstaller. PowerShell 7 is not bundled. Users install it separately.

The Makefile finds cloudflared on PATH and looks for Inno Setup in its default install folder. Override either path if yours differ:

```powershell
make installer ISCC="C:/Tools/Inno/ISCC.exe" CLOUDFLARED="C:/Tools/cloudflared.exe"
```

The installer definition is [installer/setup.iss](../installer/setup.iss). There are no PowerShell build, install, or uninstall scripts.

## Version and icon

The application version lives in `assets/app.rc`. After changing it or the icon artwork, run `make icons` to regenerate the icon and Windows resources. This step needs GNU windres. The installer reads its version from the built EXE.

## Test

```powershell
make test
```

This runs `go test ./...` and `go vet ./...`. The PowerShell tests need `pwsh`. The tests cover PowerShell through both the Cloudflare and relay paths, independent services, credential rotation, restart persistence, request cancellation, and authentication.

Set `TEST_CLOUDFLARE=1` to include the live Cloudflare tests. They start a real Quick Tunnel and need internet access.

## Run a relay locally

```powershell
.\dist\computer-access-mcp.exe relay
.\dist\computer-access-mcp.exe agent -relay http://127.0.0.1:8788
```

The relay listens on `127.0.0.1:8788` and saves its registry to `relay-data/registry.json`. See [Railway deployment](deploy.md) for a hosted relay.
