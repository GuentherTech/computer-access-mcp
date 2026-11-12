package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// stdioConfig is internal. The desktop app currently exposes only Cua.
type stdioConfig struct {
	Prefix  string
	Command string
	Args    []string
	Dir     string
}

// stdioForwarder owns one subprocess and serializes calls across all services.
// A disconnected process is restarted on the next request. Actions are never replayed.
type stdioForwarder struct {
	config stdioConfig
	ctx    context.Context
	cancel context.CancelFunc
	gate   chan struct{}
	calls  chan struct{}
	build  func(string) *mcp.Server

	// Protected by gate.
	handler http.Handler
	done    <-chan struct{}
	err     error
	retryAt time.Time
}

func newStdioForwarder(config stdioConfig, build func(string) *mcp.Server) *stdioForwarder {
	ctx, cancel := context.WithCancel(context.Background())
	return &stdioForwarder{config: config, ctx: ctx, cancel: cancel, gate: make(chan struct{}, 1), calls: make(chan struct{}, 1), build: build}
}

func (f *stdioForwarder) serveHTTP(w http.ResponseWriter, r *http.Request) {
	handler, err := f.prepare(r.Context())
	if err != nil {
		// The child may include sensitive data in protocol errors. Keep those local.
		w.Header().Set("Retry-After", "2")
		http.Error(w, "Computer use is unavailable. Check the Cua Driver installation and executable in Settings, or disable computer use.", http.StatusServiceUnavailable)
		return
	}
	handler.ServeHTTP(w, r)
}

func (f *stdioForwarder) prepare(ctx context.Context) (http.Handler, error) {
	select {
	case f.gate <- struct{}{}:
		defer func() { <-f.gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.ctx.Done():
		return nil, f.ctx.Err()
	}
	if f.ctx.Err() != nil {
		return nil, f.ctx.Err()
	}
	if f.handler != nil {
		select {
		case <-f.done:
			f.handler = nil
		default:
			return f.handler, nil
		}
	}
	if time.Now().Before(f.retryAt) {
		return nil, f.err
	}
	handler, done, err := f.connect(ctx)
	if err != nil {
		f.err, f.retryAt = err, time.Now().Add(2*time.Second)
		return nil, err
	}
	f.handler, f.done = handler, done
	f.err, f.retryAt = nil, time.Time{}
	return handler, nil
}

func (f *stdioForwarder) connect(requestCtx context.Context) (_ http.Handler, _ <-chan struct{}, err error) {
	// Initialization has a deadline, but the child outlives the initiating request.
	ctx, cancel := context.WithTimeout(requestCtx, 15*time.Second)
	defer cancel()
	stop := context.AfterFunc(f.ctx, cancel)
	defer stop()
	processCtx, stopProcess := context.WithCancel(f.ctx)
	command := exec.CommandContext(processCtx, f.config.Command, f.config.Args...)
	command.Dir = f.config.Dir
	command.WaitDelay = time.Second
	configureProcess(command)
	client := mcp.NewClient(&mcp.Implementation{Name: "computer-access-mcp", Version: "0.1.0"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}})
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: time.Second}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-06-18"})
	if err != nil {
		stopProcess()
		return nil, nil, fmt.Errorf("start MCP subprocess: %w", err)
	}
	closeOnStop := context.AfterFunc(processCtx, func() { _ = session.Close() })
	defer func() {
		if err != nil {
			stopProcess()
			_ = session.Close()
			closeOnStop()
		}
	}()
	server := f.build(fmt.Sprintf("\nForwarded MCP tools have the prefix %q. When upstream instructions or resources name a tool, call it with that prefix. Resource URIs are unchanged.\n%s", f.config.Prefix, session.InitializeResult().Instructions))
	if err = f.register(ctx, server, session); err != nil {
		return nil, nil, err
	}
	done := make(chan struct{})
	go func() {
		_ = session.Wait()
		stopProcess()
		closeOnStop()
		close(done)
	}()
	return mcpTransport(server), done, nil
}

func (f *stdioForwarder) register(ctx context.Context, server *mcp.Server, session *mcp.ClientSession) (err error) {
	// The SDK panics on malformed upstream schemas and URI templates. Treat a bad
	// catalog as an unavailable integration rather than terminating the host.
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("invalid upstream MCP catalog: %v", recovered)
		}
	}()
	caps := session.InitializeResult().Capabilities
	if caps == nil {
		return errors.New("upstream did not advertise capabilities")
	}
	if caps.Tools != nil {
		for tool, listErr := range session.Tools(ctx, nil) {
			if listErr != nil {
				return listErr
			}
			forwarded := *tool
			forwarded.Name = f.config.Prefix + tool.Name
			name := tool.Name
			server.AddTool(&forwarded, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				ctx, cancel := f.callContext(ctx)
				defer cancel()
				if err := f.acquire(ctx); err != nil {
					return nil, err
				}
				defer func() { <-f.calls }()
				return session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: req.Params.Arguments, Meta: forwardMeta(req.Params.Meta), InputResponses: req.Params.InputResponses, RequestState: req.Params.RequestState})
			})
		}
	}
	read := func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		ctx, cancel := f.callContext(ctx)
		defer cancel()
		params := *req.Params
		params.Meta = forwardMeta(params.Meta)
		return session.ReadResource(ctx, &params)
	}
	if caps.Resources != nil {
		for resource, listErr := range session.Resources(ctx, nil) {
			if listErr != nil {
				return listErr
			}
			server.AddResource(resource, read)
		}
		for template, listErr := range session.ResourceTemplates(ctx, nil) {
			if listErr != nil {
				return listErr
			}
			server.AddResourceTemplate(template, read)
		}
	}
	return nil
}

func (f *stdioForwarder) callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := toolRequestContext(ctx)
	ctx, timeout := context.WithTimeout(ctx, 120*time.Second)
	stop := context.AfterFunc(f.ctx, timeout)
	return ctx, func() { stop(); timeout(); cancel() }
}

func (f *stdioForwarder) close() {
	f.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case f.gate <- struct{}{}:
		done := f.done
		<-f.gate
		if done != nil {
			select {
			case <-done:
			case <-ctx.Done():
			}
		}
	case <-ctx.Done():
	}
}

func (f *stdioForwarder) acquire(ctx context.Context) error {
	select {
	case f.calls <- struct{}{}:
		if ctx.Err() != nil {
			<-f.calls
			return ctx.Err()
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Protocol metadata belongs to each hop. Progress notifications are not proxied.
func forwardMeta(meta mcp.Meta) mcp.Meta {
	copy := mcp.Meta{}
	for key, value := range meta {
		if key != "progressToken" && !strings.HasPrefix(key, "io.modelcontextprotocol/") {
			copy[key] = value
		}
	}
	return copy
}

type computerUseConfig struct {
	Enabled    bool   `json:"enabled"`
	Executable string `json:"executable,omitempty"`
}

// computerMCPHost combines the built-in server with the optional Cua subprocess.
// Configuration changes replace the forwarder and cancel its outstanding calls.
type computerMCPHost struct {
	mu       sync.RWMutex
	config   computerUseConfig
	shell    *powerShell
	base     http.Handler
	handler  http.Handler
	upstream *stdioForwarder
}

func newComputerMCPHost(token string, shell *powerShell) *computerMCPHost {
	host := &computerMCPHost{shell: shell, base: mcpTransport(newMCPServer(shell, instructions))}
	host.handler = authenticateMCP(token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host.mu.RLock()
		upstream := host.upstream
		host.mu.RUnlock()
		if upstream == nil {
			host.base.ServeHTTP(w, r)
			return
		}
		upstream.serveHTTP(w, r)
	}))
	return host
}

func (h *computerMCPHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.handler.ServeHTTP(w, r)
}

func (h *computerMCPHost) configure(config computerUseConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.config == config {
		return
	}
	if h.upstream != nil {
		h.upstream.cancel()
		h.upstream = nil
	}
	h.config = config
	if !config.Enabled {
		return
	}
	executable := strings.TrimSpace(config.Executable)
	if executable == "" {
		executable = "cua-driver"
	}
	h.upstream = newStdioForwarder(stdioConfig{Prefix: "cua_", Command: executable, Args: []string{"mcp"}, Dir: h.shell.workdir}, func(extra string) *mcp.Server {
		return newMCPServer(h.shell, instructions+"\nWhen listed, read skill://cua-driver/SKILL.md and its Windows guide with resources/read before computer use.\n"+extra)
	})
}

func (h *computerMCPHost) close() {
	h.mu.Lock()
	upstream := h.upstream
	h.upstream = nil
	h.config = computerUseConfig{}
	h.mu.Unlock()
	if upstream != nil {
		upstream.close()
	}
}
