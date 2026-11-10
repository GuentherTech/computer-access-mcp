package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"time"
)

var tunnelURLPattern = regexp.MustCompile(`https://[a-z0-9]+(?:-[a-z0-9]+)*\.trycloudflare\.com\b`)

type tunnelProcess struct {
	url      string
	done     chan error
	cancel   context.CancelFunc
	finished chan struct{}
}

func (t *tunnelProcess) stop() {
	t.cancel()
	<-t.finished
}

func startTunnel(parent context.Context, binary, localURL string, logs io.Writer) (*tunnelProcess, error) {
	origin, err := url.Parse(localURL)
	if err != nil {
		return nil, fmt.Errorf("parse tunnel origin: %w", err)
	}
	ctx, cancel := context.WithCancel(parent)
	// An empty config isolates the Quick Tunnel from any named-tunnel config
	// in the user's home directory.
	config, err := os.CreateTemp("", "computer-access-cloudflared-*.yml")
	if err != nil {
		cancel()
		return nil, err
	}
	configName := config.Name()
	if _, err := config.WriteString("{}\n"); err != nil {
		_ = config.Close()
		_ = os.Remove(configName)
		cancel()
		return nil, err
	}
	if err := config.Close(); err != nil {
		_ = os.Remove(configName)
		cancel()
		return nil, err
	}
	// Keep the origin Host local so the SDK's DNS rebinding protection stays on.
	cmd := exec.CommandContext(ctx, binary, "tunnel", "--config", configName, "--no-autoupdate", "--url", localURL, "--http-host-header", origin.Host)
	configureProcess(cmd)
	cmd.WaitDelay = 2 * time.Second
	reader, writer := io.Pipe()
	cmd.Stdout, cmd.Stderr = writer, writer
	if err := cmd.Start(); err != nil {
		cancel()
		_ = reader.Close()
		_ = writer.Close()
		_ = os.Remove(configName)
		return nil, fmt.Errorf("start cloudflared: %w", err)
	}
	t := &tunnelProcess{cancel: cancel, done: make(chan error, 1), finished: make(chan struct{})}
	urls := make(chan string, 1)
	var once sync.Once
	logsDone := make(chan struct{})
	go func() {
		defer close(logsDone)
		defer reader.Close()
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), 256<<10)
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Fprintln(logs, line)
			if url := tunnelURLPattern.FindString(line); url != "" {
				once.Do(func() { urls <- url })
			}
		}
	}()
	go func() {
		err := cmd.Wait()
		_ = writer.Close()
		<-logsDone
		_ = os.Remove(configName)
		t.done <- err
		close(t.finished)
	}()
	timer := time.NewTimer(60 * time.Second)
	defer timer.Stop()
	select {
	case t.url = <-urls:
		return t, nil
	case err := <-t.done:
		t.stop()
		return nil, fmt.Errorf("cloudflared exited before publishing a URL: %v", err)
	case <-timer.C:
		t.stop()
		return nil, errors.New("Cloudflare did not publish a tunnel URL within 60 seconds; see cloudflared logs")
	case <-ctx.Done():
		t.stop()
		return nil, ctx.Err()
	}
}
