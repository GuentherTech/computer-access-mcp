package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/GuentherTech/computer-access-mcp/internal/link"
)

// directHandler exposes the same authenticated service routes without a relay.
func (a *agent) directHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !strings.HasPrefix(r.URL.Path, "/s/") {
		http.NotFound(w, r)
		return
	}
	urlID, suffix, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/s/"), "/")
	if !link.ValidID(urlID) {
		http.NotFound(w, r)
		return
	}
	config := a.snapshot()
	if config.Mode != "cloudflare" {
		http.Error(w, "Direct access is off", 503)
		return
	}
	for _, service := range config.Services {
		if service.URLID != urlID {
			continue
		}
		if r.Header.Get("Upgrade") != "" {
			http.Error(w, "WebSocket services are not supported", 501)
			return
		}
		// Replace any caller-supplied routing headers before local validation.
		r.Header.Set("X-Relay-Service-ID", service.ID)
		r.Header.Set("X-Relay-URL-ID", urlID)
		r.URL.Path = "/" + suffix
		r.URL.RawPath = ""
		ctx, cancel := context.WithTimeout(r.Context(), 130*time.Second)
		defer cancel()
		a.serveService(w, r.WithContext(ctx))
		return
	}
	http.NotFound(w, r)
}

func findCloudflared(binary string) (string, error) {
	if binary == "" || binary == "cloudflared" {
		if exe, err := os.Executable(); err == nil {
			for _, name := range []string{"cloudflared.exe", "cloudflared"} {
				if found, err := exec.LookPath(filepath.Join(filepath.Dir(exe), name)); err == nil {
					return found, nil
				}
			}
		}
		binary = "cloudflared"
	}
	return exec.LookPath(binary)
}

func (a *agent) cloudflareLoop(ctx context.Context) {
	defer a.setStatus(false, nil)
	for ctx.Err() == nil {
		c := a.snapshot()
		if !c.Enabled {
			a.setStatus(false, nil)
			select {
			case <-ctx.Done():
				return
			case <-a.wake:
				continue
			}
		}
		binary, err := findCloudflared(c.Cloudflared)
		if err != nil {
			a.setStatus(false, errors.New("cloudflared was not found. Install it or choose its executable in Connection settings."))
			select {
			case <-ctx.Done():
				return
			case <-a.wake:
				continue
			case <-time.After(3 * time.Second):
				continue
			}
		}
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			a.setStatus(false, err)
			return
		}
		connectionCtx, cancel := context.WithCancel(ctx)
		server := &http.Server{Handler: http.HandlerFunc(a.directHandler), ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 32 << 10, BaseContext: func(net.Listener) context.Context { return connectionCtx }}
		serverDone := make(chan error, 1)
		go func() { serverDone <- server.Serve(listener) }()
		a.setStatus(false, nil)
		tunnel, err := startTunnel(connectionCtx, binary, "http://"+listener.Addr().String(), io.Discard)
		if err == nil {
			a.mu.Lock()
			a.publicURL = tunnel.url
			a.mu.Unlock()
			a.setStatus(true, nil)
			active := true
			for active {
				select {
				case <-ctx.Done():
					active = false
				case <-tunnel.done:
					err = errors.New("Cloudflare disconnected. Reconnecting with a new address.")
					active = false
				case <-serverDone:
					err = errors.New("The local service listener stopped.")
					active = false
				case <-a.wake:
					if !a.snapshot().Enabled {
						active = false
					}
				}
			}
			tunnel.stop()
		}
		cancel()
		server.Close()
		a.mu.Lock()
		a.publicURL = ""
		a.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			a.setStatus(false, errors.New("Could not keep the Cloudflare tunnel connected. Check your connection; retrying automatically."))
		}
		select {
		case <-ctx.Done():
			return
		case <-a.wake:
		case <-time.After(2 * time.Second):
		}
	}
}

// Switching connection mode restarts only the transport, not saved services.
func (a *agent) connectionManager(ctx context.Context) {
	for ctx.Err() == nil {
		c := a.snapshot()
		child, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			if c.Mode == "cloudflare" {
				a.cloudflareLoop(child)
			} else {
				a.connectLoop(child)
			}
		}()
		ticker := time.NewTicker(250 * time.Millisecond)
		changed := false
		for !changed {
			select {
			case <-ctx.Done():
				changed = true
			case <-done:
				changed = true
			case <-ticker.C:
				latest := a.snapshot()
				changed = latest.Mode != c.Mode || latest.Cloudflared != c.Cloudflared || (c.Mode == "relay" && latest.RelayURL != c.RelayURL) || (c.Mode == "cloudflare" && latest.Enabled != c.Enabled)
			}
		}
		ticker.Stop()
		cancel()
		<-done
	}
}
