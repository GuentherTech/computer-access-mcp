package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func run() error {
	args := os.Args[1:]
	mode := "agent"
	if len(args) > 0 && (args[0] == "agent" || args[0] == "relay" || args[0] == "quick") {
		mode = args[0]
		args = args[1:]
	}
	var err error
	switch mode {
	case "relay":
		err = runRelay(args)
	case "quick":
		err = runQuick(args)
	default:
		err = runAgent(args)
	}
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}

func runQuick(args []string) error {
	flags := flag.NewFlagSet("quick", flag.ContinueOnError)
	port := flags.Int("port", 0, "Loopback port; 0 selects a free port")
	noTunnel := flags.Bool("no-tunnel", false, "Serve locally without Cloudflare")
	pwsh := flags.String("pwsh", "pwsh", "PowerShell 7 executable")
	cloudflared := flags.String("cloudflared", "cloudflared", "cloudflared executable")
	workdir := flags.String("workdir", ".", "Default working directory for commands")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *port < 0 || *port > 65535 {
		return errors.New("port must be between 0 and 65535")
	}
	shell, err := exec.LookPath(*pwsh)
	if err != nil {
		return fmt.Errorf("PowerShell 7 not found; install it or set -pwsh: %w", err)
	}
	var tunnelBinary string
	if !*noTunnel {
		tunnelBinary, err = exec.LookPath(*cloudflared)
		if err != nil {
			return fmt.Errorf("cloudflared not found; install it or set -cloudflared: %w", err)
		}
	}
	dir, err := filepath.Abs(*workdir)
	if err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("working directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("working directory must be a directory")
	}
	token := os.Getenv("COMPUTER_ACCESS_TOKEN")
	if token == "" {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return err
		}
		token = base64.RawURLEncoding.EncodeToString(secret)
	}
	if len(token) < 32 || strings.ContainsAny(token, " \t\r\n") {
		return errors.New("COMPUTER_ACCESS_TOKEN must have at least 32 characters and no whitespace")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           newHandler(token, &powerShell{executable: shell, workdir: dir, slots: make(chan struct{}, 4)}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		_ = server.Close()
	}()
	localURL := "http://" + listener.Addr().String()
	publicURL := localURL
	var tunnel *tunnelProcess
	if !*noTunnel {
		fmt.Fprintln(os.Stderr, "Starting Cloudflare tunnel...")
		tunnel, err = startTunnel(ctx, tunnelBinary, localURL, os.Stderr)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		defer tunnel.stop()
		publicURL = tunnel.url
	}
	fmt.Printf("\nComputer Access\n\nServer URL:     %s/mcp\nTransport:      Streamable HTTP\nAuthentication: Bearer Token\nBearer token:   %s\nWorking folder: %s\n\nCommands run with your Windows account's permissions.\nKeep the bearer token private. Press Ctrl+C to stop.\n\n", publicURL, token, dir)
	var tunnelDone <-chan error
	if tunnel != nil {
		tunnelDone = tunnel.done
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-serverDone:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case err := <-tunnelDone:
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("Cloudflare tunnel stopped: %v", err)
	}
}
