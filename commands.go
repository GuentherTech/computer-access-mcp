package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/GuentherTech/computer-access-mcp/internal/link"
	"github.com/GuentherTech/computer-access-mcp/internal/persist"
	"github.com/GuentherTech/computer-access-mcp/internal/relay"
)

// Distributors set this with -ldflags "-X main.defaultRelayURL=https://...".
var defaultRelayURL string

func runAgent(args []string) error {
	path, err := defaultConfigPath()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("agent", flag.ContinueOnError)
	configPath := flags.String("config", path, "Persistent agent configuration")
	relayURL := flags.String("relay", defaultRelayURL, "Relay URL, required only on first setup")
	port := flags.Int("port", 17877, "Local management port")
	open := flags.Bool("open", true, "Show the desktop management window; false starts in the tray")
	headless := flags.Bool("headless", false, "Run without a desktop window or tray")
	mode := flags.String("mode", "", "Connection mode: cloudflare or relay")
	cloudflared := flags.String("cloudflared", "", "Optional cloudflared executable path")
	workdir := flags.String("workdir", home, "Default working folder for PowerShell")
	pwsh := flags.String("pwsh", "pwsh", "PowerShell 7 executable")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *port < 1 || *port > 65535 {
		return errors.New("management port must be between 1 and 65535")
	}
	if *mode != "" && *mode != "cloudflare" && *mode != "relay" {
		return errors.New("mode must be cloudflare or relay")
	}
	a, err := openAgent(*configPath)
	if err != nil {
		// A second invocation activates the running native window.
		var saved agentConfig
		if *open && persist.Load(*configPath, &saved, true) == nil && len(saved.ManagementToken) >= 40 {
			r, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/api/show", *port), nil)
			r.Header.Set("Authorization", "Bearer "+saved.ManagementToken)
			if response, requestErr := (&http.Client{Timeout: 3 * time.Second}).Do(r); requestErr == nil {
				response.Body.Close()
				if response.StatusCode == 204 {
					return nil
				}
			}
		}
		return err
	}
	defer a.close()
	if err := registerInstallerMutex(); err != nil {
		return err
	}
	if *relayURL != "" {
		base, err := link.BaseURL(*relayURL)
		if err != nil {
			return err
		}
		if a.snapshot().RelayURL != base {
			if err := a.mutate(func(c *agentConfig) error {
				if c.RelayURL != "" {
					return errors.New("relay address is already saved; keeping it preserves your URLs")
				}
				c.RelayURL = base
				c.Mode = "relay"
				return nil
			}); err != nil {
				return err
			}
		}
	}
	if *mode != "" || *cloudflared != "" {
		if err := a.mutate(func(c *agentConfig) error {
			if *mode != "" {
				c.Mode = *mode
			}
			if *cloudflared != "" {
				c.Cloudflared = *cloudflared
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if key := os.Getenv("COMPUTER_ACCESS_ENROLLMENT_KEY"); key != "" {
		if err := a.mutate(func(c *agentConfig) error { c.EnrollmentKey = key; return nil }); err != nil {
			return err
		}
	}
	dir, err := filepath.Abs(*workdir)
	if err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("working folder must be a directory")
	}
	a.builtinToken = link.NewSecret()
	a.builtinHandler = newHandler(a.builtinToken, &powerShell{lookup: func() (string, error) { return findPowerShell(*pwsh) }, workdir: dir, slots: make(chan struct{}, 4)})
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	a.quit = cancel
	server := &http.Server{Handler: a.uiHandler(a.snapshot().ManagementToken, listener.Addr().String()), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer server.Close()
	connectionDone := make(chan struct{})
	go func() { defer close(connectionDone); a.connectionManager(ctx) }()
	defer func() { cancel(); <-connectionDone }()
	fmt.Printf("\nComputer access agent\nMode: %s\nSaved configuration: %s\n\n", a.snapshot().Mode, *configPath)
	for _, s := range a.view().Services {
		if s.URL != "" {
			fmt.Printf("%s\nURL: %s\nPassword: %s\n\n", s.Name, s.URL, s.Password)
		}
	}
	fmt.Println("Passwords use Bearer Token authentication. Press Ctrl+C to stop the agent.")
	if !*headless {
		go func() {
			select {
			case <-ctx.Done():
			case <-done:
				cancel()
			}
		}()
		return runDesktopGUI(ctx, a, *open)
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func findPowerShell(binary string) (string, error) {
	path, err := exec.LookPath(binary)
	if err != nil && runtime.GOOS == "windows" && binary == "pwsh" {
		// A just-installed PowerShell may not be on the installer's inherited PATH.
		return exec.LookPath(filepath.Join(os.Getenv("ProgramFiles"), "PowerShell", "7", "pwsh.exe"))
	}
	return path, err
}

func runRelay(args []string) error {
	options, err := parseRelayOptions(args)
	if err != nil {
		return err
	}
	relayServer, err := relay.Open(options.data)
	if err != nil {
		return err
	}
	defer relayServer.Close()
	relayServer.MaxDevices = options.maxDevices
	relayServer.EnrollmentKey = os.Getenv("RELAY_ENROLLMENT_KEY")
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	listener, err := net.Listen("tcp", options.listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: relayServer, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer server.Close()
	fmt.Printf("Computer access relay listening on %s\nPersistent database: %s\n", listener.Addr(), options.data)
	if options.publicURL != "" {
		fmt.Println("Public address:", options.publicURL)
	}
	if relayServer.EnrollmentKey == "" {
		fmt.Println("New device enrollment is open. Set RELAY_ENROLLMENT_KEY to restrict it.")
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
