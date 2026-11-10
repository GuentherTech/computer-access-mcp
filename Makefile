GO ?= go
ISCC ?= C:/Program Files (x86)/Inno Setup 6/ISCC.exe
CLOUDFLARED ?= $(shell where.exe cloudflared.exe)
WINDRES ?= windres

export CGO_ENABLED := 0

.PHONY: build test installer icons

build:
	$(GO) build -trimpath -ldflags "-s -w -H=windowsgui" -o dist/computer-access-mcp.exe .

test:
	$(GO) test ./...
	$(GO) vet ./...

installer: build
	$(if $(strip $(CLOUDFLARED)),,$(error cloudflared.exe is not on PATH; set CLOUDFLARED to its full path))
	"$(ISCC)" /Qp "/DCloudflaredPath=$(CLOUDFLARED)" installer/setup.iss

# Only needed after changing the icon or Windows version resource.
icons:
	$(GO) run ./internal/iconbuild
	$(WINDRES) --input assets/app.rc --output resource_windows_amd64.syso --output-format coff --target pe-x86-64 --include-dir assets
